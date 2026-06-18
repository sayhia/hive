// Package appstate holds the process-wide shared state every Wails service
// reads from: the database pools, the HTTP client (rebuildable when proxy /
// timeout settings change), and the refresh lock. Mirrors papr's
// src-tauri/src/state.rs AppState.
package appstate

import (
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"hive/internal/db"
)

// State is shared by all services. It is created once at startup and handed to
// each service via a pointer, so they all see the same database and HTTP client.
type State struct {
	DB      *db.DB
	dataDir string

	httpMu sync.RWMutex
	http   *http.Client

	// refreshMu guards against two refresh runs overlapping (manual + scheduled).
	refreshMu sync.Mutex

	deepLinkMu      sync.Mutex
	pendingDeepLink string
}

// New opens the database under dataDir and builds a default HTTP client. The
// caller is responsible for calling Close at shutdown.
func New(dataDir string) (*State, error) {
	database, err := db.Open(filepath.Join(dataDir, "hive.db"))
	if err != nil {
		return nil, err
	}
	return &State{
		DB:      database,
		dataDir: dataDir,
		http:    defaultClient(),
	}, nil
}

// defaultClient is the HTTP client used until network settings are applied.
func defaultClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// Close releases the database pools.
func (s *State) Close() error {
	if s.DB != nil {
		return s.DB.Close()
	}
	return nil
}

// DataDir is the application data directory (where the database lives).
func (s *State) DataDir() string { return s.dataDir }

// HTTP returns the current HTTP client. Safe to call concurrently with
// SetHTTP; the returned client may be swapped out by a later settings change,
// but an in-flight request keeps using the client it started with.
func (s *State) HTTP() *http.Client {
	s.httpMu.RLock()
	defer s.httpMu.RUnlock()
	return s.http
}

// SetHTTP swaps in a freshly built HTTP client (e.g. after the user changes the
// proxy or request timeout), without an app restart.
func (s *State) SetHTTP(c *http.Client) {
	s.httpMu.Lock()
	defer s.httpMu.Unlock()
	s.http = c
}

// TryLockRefresh acquires the refresh lock without blocking. It returns false
// when a refresh is already running, so a second run is skipped rather than
// queued.
func (s *State) TryLockRefresh() bool { return s.refreshMu.TryLock() }

// LockRefresh acquires the refresh lock, blocking until it is free. Used by
// callers (OPML import) that must queue behind an in-flight refresh so their
// freshly added feeds still get fetched.
func (s *State) LockRefresh() { s.refreshMu.Lock() }

// UnlockRefresh releases the refresh lock.
func (s *State) UnlockRefresh() { s.refreshMu.Unlock() }

// SetPendingDeepLink buffers a deep-link URL delivered before the webview is
// ready to receive it (a cold-start launch). The frontend drains it on mount.
func (s *State) SetPendingDeepLink(url string) {
	s.deepLinkMu.Lock()
	defer s.deepLinkMu.Unlock()
	s.pendingDeepLink = url
}

// TakePendingDeepLink returns and clears the buffered deep-link URL, if any.
func (s *State) TakePendingDeepLink() string {
	s.deepLinkMu.Lock()
	defer s.deepLinkMu.Unlock()
	url := s.pendingDeepLink
	s.pendingDeepLink = ""
	return url
}
