// Dynamic loading for the curated feed directory.
//
// The directory used to be a compile-time constant (go:embed only). It is now
// merged from one or more remote sources, each resolving through three layers:
//
//	read path:    per-source disk cache (last good download) → embed (official only)
//	update path:  directory access → TTL expired? → background conditional GETs
//	              → validate → per-source persist → rebuild merge + swap → event
//
// Sources: the official directory (repository copy served through jsDelivr,
// with the embedded JSON as its permanent offline fallback) plus any number of
// user-added directory URLs (settings → subscriptions). The merge keeps the
// official entries first and de-duplicates by feedUrl, first seen wins — a
// community directory can extend the official one but not shadow its entries.
//
// Stale-while-revalidate: readers always get an answer immediately (the swap
// is a pointer write under RWMutex), the network never blocks a search, and
// every failure mode — offline, 404, oversized body, malformed JSON — keeps
// that source's last good copy without touching the others.
//
// A source file may be either a legacy bare array or a versioned envelope
// {"version":2,"updatedAt":...,"entries":[...]} so hosted copies can grow
// fields without breaking older builds.
package discovery

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hive/internal/apperr"
)

// DefaultDirectoryURL is where the official directory is fetched from: the
// repository copy served through jsDelivr's CDN (same zero-server hosting the
// font feature uses). Updating the JSON on main updates every install within
// a TTL — no release needed.
const DefaultDirectoryURL = "https://cdn.jsdelivr.net/gh/sunmking/hive@main/internal/discovery/feed-directory.json"

// RefreshTTL is how long a fetched (or 304-revalidated) source stays fresh
// before the next directory access triggers a background re-check.
const RefreshTTL = 24 * time.Hour

// MaxUserSources caps how many custom directory URLs can be added.
const MaxUserSources = 10

// Validation ceilings for a remote payload. Remotes are never trusted: a
// compromised or corrupted download must not be able to balloon memory or
// wedge discovery, so anything over these limits is rejected wholesale and
// the source's previous copy stays.
const (
	maxDirectoryBytes   = 2 << 20 // 2 MiB
	maxDirectoryEntries = 2000
)

// store holds the live merged directory plus the per-source breakdown from
// the last Reload. merged == nil means "not yet loaded"; current() then falls
// back to parsing the embed lazily (preserving the original panic-on-bad-embed
// behaviour, which is a build error).
var store struct {
	mu      sync.RWMutex
	merged  []directoryEntry
	sources []SourceStatus
}

// refreshMu serializes refresh attempts; refreshing while a refresh is already
// in flight is a no-op rather than a pile-up.
var refreshMu sync.Mutex

// directoryEnvelope is the versioned remote format. Legacy bare arrays are
// accepted too (see parseDirectory).
type directoryEnvelope struct {
	Version   int              `json:"version"`
	UpdatedAt string           `json:"updatedAt"`
	Entries   []directoryEntry `json:"entries"`
}

// SourceStatus describes one directory source as of the last Reload.
type SourceStatus struct {
	// URL is the source's remote URL; empty for the official source.
	URL string
	// Official marks the built-in source (embed fallback, always first).
	Official bool
	// Count is the source's own entry count, before cross-source dedup.
	Count int
	// UpdatedAt is the source envelope's authored date ("" for bare arrays).
	UpdatedAt string
	// CheckedAt is the source cache file's mtime (zero when never fetched;
	// a 304 revalidation re-touches it).
	CheckedAt time.Time
	// OK is false when a user source has no readable, valid cache — its
	// entries are simply absent from the merge until a refresh succeeds.
	OK bool
}

// Status describes the merged directory for the settings UI.
type Status struct {
	Count     int
	UpdatedAt string    // the OFFICIAL source's authored date
	CheckedAt time.Time // the official cache's mtime; zero when never fetched
}

// current returns the live merged entries, falling back to the embedded copy.
func current() []directoryEntry {
	store.mu.RLock()
	merged := store.merged
	store.mu.RUnlock()
	if merged != nil {
		return merged
	}
	entries, _, err := parseDirectory(directoryJSON)
	if err != nil {
		// A bundled asset that won't parse is a build error, not a runtime
		// condition. Surface it loudly so it's caught in tests/CI.
		panic(apperr.Wrap("directoryInvalid", err))
	}
	return entries
}

// CurrentStatus reports the merged directory's status.
func CurrentStatus(dataDir string) Status {
	store.mu.RLock()
	sources := store.sources
	count := len(store.merged)
	store.mu.RUnlock()
	st := Status{Count: count}
	if len(sources) > 0 {
		st.UpdatedAt = sources[0].UpdatedAt
		st.CheckedAt = sources[0].CheckedAt
	}
	if st.Count == 0 { // never reloaded — fall back to the embed view
		st.Count = len(current())
		if fi, err := os.Stat(officialCachePath(dataDir)); err == nil {
			st.CheckedAt = fi.ModTime()
		}
	}
	return st
}

// Sources reports the per-source breakdown from the last Reload, reloading
// first if none happened yet.
func Sources(dataDir string, userURLs []string) []SourceStatus {
	store.mu.RLock()
	s := store.sources
	store.mu.RUnlock()
	if s == nil {
		Reload(dataDir, userURLs)
		store.mu.RLock()
		s = store.sources
		store.mu.RUnlock()
	}
	return append([]SourceStatus(nil), s...)
}

// ── payload validation ─────────────────────────────────────────────

// parseDirectory decodes and validates a directory payload — either the
// versioned envelope or the legacy bare array. It enforces the size/count
// ceilings and per-entry invariants (non-empty title, http(s) feed URL) so a
// bad remote can never be swapped in. The second return is the envelope's
// updatedAt ("" for a bare array).
func parseDirectory(data []byte) ([]directoryEntry, string, error) {
	if len(data) > maxDirectoryBytes {
		return nil, "", errDirectory("payload exceeds size limit")
	}
	trimmed := bytes.TrimLeftFunc(data, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' })
	var entries []directoryEntry
	updatedAt := ""
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var env directoryEnvelope
		if err := json.Unmarshal(data, &env); err != nil {
			return nil, "", err
		}
		entries = env.Entries
		updatedAt = env.UpdatedAt
	} else if err := json.Unmarshal(data, &entries); err != nil {
		return nil, "", err
	}
	if len(entries) == 0 {
		return nil, "", errDirectory("no entries")
	}
	if len(entries) > maxDirectoryEntries {
		return nil, "", errDirectory("too many entries")
	}
	for _, e := range entries {
		if strings.TrimSpace(e.Title) == "" {
			return nil, "", errDirectory("entry missing title")
		}
		u, err := url.Parse(e.FeedURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, "", errDirectory("entry has invalid feed URL")
		}
	}
	return entries, updatedAt, nil
}

type errDirectory string

func (e errDirectory) Error() string { return "directory: " + string(e) }

// ── per-source cache files ─────────────────────────────────────────

// The official source keeps the original directory.json / directory.etag
// names (P1 caches stay valid); user sources hash their URL into the name.
func officialCachePath(dataDir string) string { return filepath.Join(dataDir, "directory.json") }

func sourceFiles(dataDir, sourceURL string) (cache, etag string) {
	if sourceURL == "" {
		return officialCachePath(dataDir), filepath.Join(dataDir, "directory.etag")
	}
	sum := sha1.Sum([]byte(sourceURL))
	base := "directory-" + hex.EncodeToString(sum[:5])
	return filepath.Join(dataDir, base+".json"), filepath.Join(dataDir, base+".etag")
}

// RemoveSourceCache deletes a removed user source's cache files.
func RemoveSourceCache(dataDir, sourceURL string) {
	cache, etag := sourceFiles(dataDir, sourceURL)
	_ = os.Remove(cache)
	_ = os.Remove(etag)
}

// ── reload: disk → merged swap ─────────────────────────────────────

// Reload rebuilds the live directory from every source's disk cache (the
// official source falls back to the embed) and swaps it in atomically, along
// with the per-source statuses. Cheap enough to run on startup and after any
// source add/remove/refresh; a stale or corrupt cache just drops that source
// from the merge (official: falls back to the embed) without breaking
// discovery.
func Reload(dataDir string, userURLs []string) {
	type src struct {
		st      SourceStatus
		entries []directoryEntry
	}
	srcs := make([]src, 0, len(userURLs)+1)

	official := src{st: SourceStatus{Official: true, OK: true}}
	if data, err := os.ReadFile(officialCachePath(dataDir)); err == nil {
		if entries, updatedAt, perr := parseDirectory(data); perr == nil {
			official.entries, official.st.UpdatedAt = entries, updatedAt
		}
	}
	if official.entries == nil {
		entries, updatedAt, err := parseDirectory(directoryJSON)
		if err != nil {
			panic(apperr.Wrap("directoryInvalid", err))
		}
		official.entries, official.st.UpdatedAt = entries, updatedAt
	}
	if fi, err := os.Stat(officialCachePath(dataDir)); err == nil {
		official.st.CheckedAt = fi.ModTime()
	}
	official.st.Count = len(official.entries)
	srcs = append(srcs, official)

	for _, u := range userURLs {
		s := src{st: SourceStatus{URL: u}}
		cache, _ := sourceFiles(dataDir, u)
		if data, err := os.ReadFile(cache); err == nil {
			if entries, updatedAt, perr := parseDirectory(data); perr == nil {
				s.entries, s.st.UpdatedAt, s.st.OK = entries, updatedAt, true
			}
		}
		if fi, err := os.Stat(cache); err == nil {
			s.st.CheckedAt = fi.ModTime()
		}
		s.st.Count = len(s.entries)
		srcs = append(srcs, s)
	}

	seen := make(map[string]bool)
	merged := []directoryEntry{}
	statuses := make([]SourceStatus, 0, len(srcs))
	for _, s := range srcs {
		for _, e := range s.entries {
			if seen[e.FeedURL] {
				continue
			}
			seen[e.FeedURL] = true
			merged = append(merged, e)
		}
		statuses = append(statuses, s.st)
	}

	store.mu.Lock()
	store.merged = merged
	store.sources = statuses
	store.mu.Unlock()
}

// ── refresh: remote → per-source persist → reload ──────────────────

// FetchSource performs one conditional GET → validate → persist cycle for a
// single source, WITHOUT touching the live directory (call Reload after).
// Returns whether a new copy was written; a 304 is (false, nil). Any failure
// keeps the source's last good copy on disk and reports why.
func FetchSource(ctx context.Context, client *http.Client, dataDir, sourceURL string) (bool, error) {
	remote := sourceURL
	if remote == "" {
		remote = DefaultDirectoryURL
	}
	cacheFile, etagFile := sourceFiles(dataDir, sourceURL)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, remote, nil)
	if err != nil {
		return false, err
	}
	if etag, err := os.ReadFile(etagFile); err == nil && len(etag) > 0 {
		req.Header.Set("If-None-Match", strings.TrimSpace(string(etag)))
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		// Still fresh — push the next check out a full TTL.
		now := time.Now()
		_ = os.Chtimes(cacheFile, now, now)
		return false, nil
	case http.StatusOK:
		// fall through
	default:
		return false, errDirectory("unexpected status " + resp.Status)
	}

	// +1 so a body exactly at the limit passes and one past it fails.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDirectoryBytes+1))
	if err != nil {
		return false, err
	}
	if _, _, err := parseDirectory(data); err != nil {
		return false, err
	}

	// Persist atomically (tmp + rename) so a crash mid-write can't leave a
	// truncated cache.
	tmp := cacheFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, cacheFile); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	if etag := resp.Header.Get("ETag"); etag != "" {
		_ = os.WriteFile(etagFile, []byte(etag), 0o644)
	} else {
		_ = os.Remove(etagFile)
	}
	return true, nil
}

// RefreshAll synchronously refreshes every source (official + userURLs),
// regardless of TTL — the settings "refresh now" button. Sources fail
// independently: one bad source never blocks the others. When any source
// landed a new copy the merge is rebuilt and swapped. Returns whether the
// live directory changed, plus the joined per-source errors (nil when every
// source succeeded or 304'd).
func RefreshAll(ctx context.Context, client *http.Client, dataDir string, userURLs []string) (bool, error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()

	changedAny := false
	var errs []error
	if changed, err := FetchSource(ctx, client, dataDir, ""); err != nil {
		errs = append(errs, err)
	} else if changed {
		changedAny = true
	}
	for _, u := range userURLs {
		if changed, err := FetchSource(ctx, client, dataDir, u); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", u, err))
		} else if changed {
			changedAny = true
		}
	}
	if changedAny {
		Reload(dataDir, userURLs)
	}
	return changedAny, errors.Join(errs...)
}

// RefreshIfStale kicks off a background RefreshAll when any source's cache is
// older than RefreshTTL (or missing). Non-blocking beyond the stats; safe to
// call on every directory access. onUpdate runs only after a new merge was
// swapped in (the caller emits "directory-updated" there). Errors are
// swallowed — a background revalidation has no one to report to.
//
// Freshness is each cache file's mtime: a 200 rewrites it, a 304 re-touches
// it, so both outcomes push the next check a full TTL out — across restarts.
func RefreshIfStale(ctx context.Context, client *http.Client, dataDir string, userURLs []string, onUpdate func()) {
	fresh := func(sourceURL string) bool {
		cache, _ := sourceFiles(dataDir, sourceURL)
		fi, err := os.Stat(cache)
		return err == nil && time.Since(fi.ModTime()) < RefreshTTL
	}
	stale := !fresh("")
	for _, u := range userURLs {
		if stale {
			break
		}
		stale = !fresh(u)
	}
	if !stale {
		return
	}
	go func() {
		if changed, _ := RefreshAll(ctx, client, dataDir, userURLs); changed && onUpdate != nil {
			onUpdate()
		}
	}()
}
