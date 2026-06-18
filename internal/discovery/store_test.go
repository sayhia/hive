package discovery

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// onlyHost blocks every request that isn't aimed at the test server, so a
// test exercising RefreshAll can't reach the real official CDN — the official
// fetch fails deterministically (the partial-failure path) online or off.
type onlyHost struct{ host string }

func (o onlyHost) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != o.host {
		return nil, errors.New("blocked by test transport")
	}
	return http.DefaultTransport.RoundTrip(r)
}

func testClient(t *testing.T, srvURL string) *http.Client {
	t.Helper()
	u, err := url.Parse(srvURL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: onlyHost{u.Host}}
}

// resetStore clears the swapped-in directory so a test can't leak state into
// the embed-integrity tests (current() falls back to the embed when nil).
func resetStore(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		store.mu.Lock()
		store.merged, store.sources = nil, nil
		store.mu.Unlock()
	})
}

// writeSourceCache drops a payload where Reload expects sourceURL's cache.
func writeSourceCache(t *testing.T, dataDir, sourceURL, payload string) {
	t.Helper()
	cache, _ := sourceFiles(dataDir, sourceURL)
	if err := os.WriteFile(cache, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ── parseDirectory: format compatibility ───────────────────────────

func TestParseDirectoryBareArray(t *testing.T) {
	data := []byte(`[{"title":"A","feedUrl":"https://a.example/rss","siteUrl":"https://a.example","category":"Tech","description":"d","lang":"en"}]`)
	entries, updatedAt, err := parseDirectory(data)
	if err != nil {
		t.Fatalf("bare array should parse: %v", err)
	}
	if len(entries) != 1 || entries[0].Title != "A" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	if updatedAt != "" {
		t.Fatalf("bare array has no updatedAt, got %q", updatedAt)
	}
}

func TestParseDirectoryEnvelope(t *testing.T) {
	data := []byte(`{"version":2,"updatedAt":"2026-07-09","entries":[{"title":"A","feedUrl":"https://a.example/rss","category":"Tech","lang":"en"}]}`)
	entries, updatedAt, err := parseDirectory(data)
	if err != nil {
		t.Fatalf("envelope should parse: %v", err)
	}
	if len(entries) != 1 || entries[0].FeedURL != "https://a.example/rss" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	if updatedAt != "2026-07-09" {
		t.Fatalf("updatedAt not surfaced, got %q", updatedAt)
	}
}

func TestParseDirectoryEmbeddedCopy(t *testing.T) {
	// The embedded JSON must always pass the remote validator, since it is the
	// same document the repository serves as the official directory.
	if _, _, err := parseDirectory(directoryJSON); err != nil {
		t.Fatalf("embedded directory fails validation: %v", err)
	}
}

// ── parseDirectory: rejection paths (keep-last-good depends on these) ──

func TestParseDirectoryRejectsBad(t *testing.T) {
	cases := map[string]string{
		"malformed json":  `{"entries": [`,
		"empty array":     `[]`,
		"empty envelope":  `{"version":2,"entries":[]}`,
		"missing title":   `[{"title":"  ","feedUrl":"https://a.example/rss"}]`,
		"bad feed scheme": `[{"title":"A","feedUrl":"ftp://a.example/rss"}]`,
		"no feed host":    `[{"title":"A","feedUrl":"https:///rss"}]`,
		"html error page": `<html>503</html>`,
	}
	for name, data := range cases {
		if _, _, err := parseDirectory([]byte(data)); err == nil {
			t.Errorf("%s: expected error, got none", name)
		}
	}
}

func TestParseDirectoryRejectsOversize(t *testing.T) {
	if _, _, err := parseDirectory(make([]byte, maxDirectoryBytes+1)); err == nil {
		t.Error("oversized payload should be rejected")
	}
}

// ── Reload: per-source caches → merged swap ────────────────────────

func TestReloadSwapsValidOfficialCache(t *testing.T) {
	resetStore(t)
	dir := t.TempDir()
	writeSourceCache(t, dir, "", `{"version":2,"updatedAt":"2026-01-01","entries":[{"title":"Cached","feedUrl":"https://c.example/rss","category":"Tech","lang":"en"}]}`)
	Reload(dir, nil)
	got := current()
	if len(got) != 1 || got[0].Title != "Cached" {
		t.Fatalf("cache not swapped in: %+v", got)
	}
	if st := CurrentStatus(dir); st.Count != 1 || st.UpdatedAt != "2026-01-01" || st.CheckedAt.IsZero() {
		t.Fatalf("unexpected status: %+v", st)
	}
}

func TestReloadIgnoresBadOfficialCache(t *testing.T) {
	resetStore(t)
	dir := t.TempDir()
	writeSourceCache(t, dir, "", "{corrupt")
	Reload(dir, nil)
	if len(current()) < 40 {
		t.Fatal("bad cache must leave the embedded directory live")
	}
}

func TestReloadMissingCacheFallsBackToEmbed(t *testing.T) {
	resetStore(t)
	Reload(t.TempDir(), nil)
	if len(current()) < 40 {
		t.Fatal("missing cache must leave the embedded directory live")
	}
}

func TestReloadMergesUserSourcesDedupByFeedURL(t *testing.T) {
	resetStore(t)
	dir := t.TempDir()
	writeSourceCache(t, dir, "", `[{"title":"Official","feedUrl":"https://shared.example/rss","category":"Tech","lang":"en"}]`)
	userA := "https://a.example/dir.json"
	userB := "https://b.example/dir.json"
	// userA duplicates the official entry (must lose) and adds one of its own.
	writeSourceCache(t, dir, userA, `{"version":2,"updatedAt":"2026-02-02","entries":[
		{"title":"Shadow","feedUrl":"https://shared.example/rss","category":"Tech","lang":"en"},
		{"title":"ExtraA","feedUrl":"https://a.example/rss","category":"Tech","lang":"en"}]}`)
	// userB has no cache yet — merged silently without it, flagged not-OK.
	Reload(dir, []string{userA, userB})

	got := current()
	if len(got) != 2 {
		t.Fatalf("expected 2 merged entries, got %d: %+v", len(got), got)
	}
	if got[0].Title != "Official" || got[1].Title != "ExtraA" {
		t.Fatalf("official must win the dedup and come first: %+v", got)
	}

	srcs := Sources(dir, []string{userA, userB})
	if len(srcs) != 3 || !srcs[0].Official || srcs[0].Count != 1 {
		t.Fatalf("unexpected sources: %+v", srcs)
	}
	if srcs[1].URL != userA || !srcs[1].OK || srcs[1].Count != 2 || srcs[1].UpdatedAt != "2026-02-02" {
		t.Fatalf("userA status wrong: %+v", srcs[1])
	}
	if srcs[2].URL != userB || srcs[2].OK || srcs[2].Count != 0 {
		t.Fatalf("cacheless userB must be present but not OK: %+v", srcs[2])
	}
}

func TestRemoveSourceCacheDeletesFiles(t *testing.T) {
	dir := t.TempDir()
	u := "https://a.example/dir.json"
	writeSourceCache(t, dir, u, `[{"title":"A","feedUrl":"https://a.example/rss"}]`)
	cache, _ := sourceFiles(dir, u)
	RemoveSourceCache(dir, u)
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatal("cache file should be gone")
	}
}

// ── FetchSource / RefreshAll: fetch → validate → persist, 304, keep-last-good ──

func TestRefreshAllFetchesAndRevalidates(t *testing.T) {
	resetStore(t)
	dir := t.TempDir()
	payload := `{"version":2,"updatedAt":"2026-07-09","entries":[{"title":"Remote","feedUrl":"https://r.example/rss","category":"Tech","lang":"en"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	// The user source fetches and lands in the merge; the official fetch is
	// blocked by the test transport — exactly the partial-failure path
	// RefreshAll must survive (one bad source never blocks the others).
	client := testClient(t, srv.URL)
	changed, err := RefreshAll(context.Background(), client, dir, []string{srv.URL})
	if !changed {
		t.Fatal("first refresh must land the user source")
	}
	if err == nil {
		t.Fatal("blocked official fetch must surface in the joined error")
	}
	found := false
	for _, e := range current() {
		if e.Title == "Remote" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("remote entry missing from merge: %+v", current())
	}

	// Second fetch of the same source sends the saved ETag → 304, unchanged.
	if changed, err := FetchSource(context.Background(), client, dir, srv.URL); err != nil || changed {
		t.Fatalf("revalidation: changed=%v err=%v", changed, err)
	}
}

func TestFetchSourceKeepsLastGoodOnBadRemote(t *testing.T) {
	resetStore(t)
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>oops</html>`))
	}))
	defer srv.Close()

	if changed, err := FetchSource(context.Background(), srv.Client(), dir, srv.URL); err == nil || changed {
		t.Fatalf("bad remote must fail: changed=%v err=%v", changed, err)
	}
	cache, _ := sourceFiles(dir, srv.URL)
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatal("bad payload must not be persisted")
	}
}

func TestFetchSourceKeepsLastGoodOnHTTPError(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if changed, err := FetchSource(context.Background(), srv.Client(), dir, srv.URL); err == nil || changed {
		t.Fatalf("404 must fail: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "directory.json")); !os.IsNotExist(err) {
		t.Fatal("user-source failure must not touch the official cache")
	}
}
