package syncer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// derefOr returns the empty string for a nil *string, otherwise its value, so
// the folderName tests can compare against a plain string.
func derefOr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func cat(id string, label *string) subCat {
	return subCat{ID: id, Label: label}
}

func strptr(s string) *string { return &s }

// --- subCat.folderName: the GReader label → local folder mapping. ---

func TestFolderNamePrefersLabel(t *testing.T) {
	got := cat("user/-/label/Tech", strptr("Tech")).folderName()
	if derefOr(got) != "Tech" {
		t.Errorf("folderName() = %q, want %q", derefOr(got), "Tech")
	}
}

func TestFolderNameFallsBackToLabelID(t *testing.T) {
	// Some servers omit the human label; derive it from the id instead.
	got := cat("user/-/label/科技", nil).folderName()
	if derefOr(got) != "科技" {
		t.Errorf("folderName() = %q, want %q", derefOr(got), "科技")
	}
}

func TestFolderNameSkipsUnnamedCategories(t *testing.T) {
	// A state tag (not a label) or a blank label is not a folder.
	if got := cat("user/-/state/com.google/read", nil).folderName(); got != nil {
		t.Errorf("state tag folderName() = %q, want nil", derefOr(got))
	}
	if got := cat("", strptr("   ")).folderName(); got != nil {
		t.Errorf("blank label folderName() = %q, want nil", derefOr(got))
	}
}

func TestFolderNameSkipsFreshRSSUncategorized(t *testing.T) {
	// FreshRSS's built-in "Uncategorized" label is not a real folder, by either
	// label or id, and regardless of case.
	if got := cat("user/-/label/Uncategorized", strptr("Uncategorized")).folderName(); got != nil {
		t.Errorf("Uncategorized (label) folderName() = %q, want nil", derefOr(got))
	}
	if got := cat("user/-/label/uncategorized", nil).folderName(); got != nil {
		t.Errorf("uncategorized (id) folderName() = %q, want nil", derefOr(got))
	}
}

// --- feedsToPush: the local-only feed selection for push-subscribe. ---

func TestFeedsToPushSelectsOnlyLocalOnlyFeeds(t *testing.T) {
	local := []string{
		"https://a.example/feed",
		"https://b.example/feed",
		"https://c.example/feed",
	}
	server := map[string]struct{}{"https://b.example/feed": {}}
	got := feedsToPush(local, server)
	want := []string{"https://a.example/feed", "https://c.example/feed"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("feedsToPush() = %v, want %v", got, want)
	}
}

func TestFeedsToPushEmptyWhenServerHasEverything(t *testing.T) {
	local := []string{"https://a.example/feed"}
	server := map[string]struct{}{"https://a.example/feed": {}}
	if got := feedsToPush(local, server); len(got) != 0 {
		t.Errorf("feedsToPush() = %v, want empty", got)
	}
}

// --- greaderBase: per-provider API-root normalization. ---

func TestGreaderBaseFreshRSSAppendsSuffix(t *testing.T) {
	got := greaderBase("https://rss.example.com", providerFreshRSS)
	want := "https://rss.example.com/api/greader.php"
	if got != want {
		t.Errorf("greaderBase() = %q, want %q", got, want)
	}
}

func TestGreaderBaseTrimsTrailingSlashBeforeSuffix(t *testing.T) {
	// A trailing slash (and surrounding whitespace) must not produce a double
	// slash before the appended suffix.
	got := greaderBase("  https://rss.example.com/  ", providerFreshRSS)
	want := "https://rss.example.com/api/greader.php"
	if got != want {
		t.Errorf("greaderBase() = %q, want %q", got, want)
	}
}

func TestGreaderBaseFreshRSSIdempotentWhenSuffixPresent(t *testing.T) {
	// If the user already typed the full path, don't append it again.
	got := greaderBase("https://rss.example.com/api/greader.php", providerFreshRSS)
	want := "https://rss.example.com/api/greader.php"
	if got != want {
		t.Errorf("greaderBase() = %q, want %q", got, want)
	}
}

func TestGreaderBaseFreshRSSIdempotentWhenSuffixInMiddle(t *testing.T) {
	// The suffix can appear mid-path (e.g. a reverse proxy mount); still don't
	// append it a second time. Mirrors the Rust `contains("{suffix}/")` branch.
	got := greaderBase("https://rss.example.com/api/greader.php/", providerFreshRSS)
	want := "https://rss.example.com/api/greader.php"
	if got != want {
		t.Errorf("greaderBase() = %q, want %q", got, want)
	}
}

func TestGreaderBaseMinifluxHasNoSuffix(t *testing.T) {
	// Miniflux serves the GReader API straight off the server root, so its
	// suffix is empty — only trimming should happen.
	got := greaderBase("https://miniflux.example.com/", providerMiniflux)
	want := "https://miniflux.example.com"
	if got != want {
		t.Errorf("greaderBase() = %q, want %q", got, want)
	}
}

// --- providerFromSetting: persisted-setting → provider mapping. ---

func TestProviderFromSetting(t *testing.T) {
	cases := []struct {
		in   *string
		want provider
	}{
		{nil, providerFreshRSS},                    // missing → FreshRSS (older installs)
		{strptr(""), providerFreshRSS},             // empty → FreshRSS
		{strptr("freshrss"), providerFreshRSS},     // explicit FreshRSS
		{strptr("  miniflux  "), providerMiniflux}, // trimmed → Miniflux
		{strptr("unknown"), providerFreshRSS},      // unknown → FreshRSS
	}
	for _, c := range cases {
		if got := providerFromSetting(c.in); got != c.want {
			t.Errorf("providerFromSetting(%v) = %v, want %v", derefOr(c.in), got, c.want)
		}
	}
}

func TestProviderStringRoundTrips(t *testing.T) {
	if providerFreshRSS.String() != "freshrss" {
		t.Errorf("providerFreshRSS.String() = %q", providerFreshRSS.String())
	}
	if providerMiniflux.String() != "miniflux" {
		t.Errorf("providerMiniflux.String() = %q", providerMiniflux.String())
	}
}

// fetchStreamItems must follow continuation pages, carry the xt exclude
// filter, and prefer canonical over alternate hrefs (papr #105).
func TestFetchStreamItemsFollowsContinuation(t *testing.T) {
	var gotXT []string
	var gotC []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/reader/api/0/stream/contents/") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		gotXT = append(gotXT, r.URL.Query().Get("xt"))
		c := r.URL.Query().Get("c")
		gotC = append(gotC, c)
		w.Header().Set("Content-Type", "application/json")
		if c == "" {
			fmt.Fprint(w, `{"items":[
				{"id":"i1","canonical":[{"href":"https://a.example/1"}],"alternate":[{"href":"https://alt.example/1"}]},
				{"id":"i2","alternate":[{"href":"https://a.example/2"}]}
			],"continuation":"page2"}`)
			return
		}
		fmt.Fprint(w, `{"items":[{"id":"i3","canonical":[{"href":"https://a.example/3"}]}]}`)
	}))
	defer srv.Close()

	sess := &session{base: srv.URL, auth: "tok"}
	items, err := fetchStreamItems(context.Background(), srv.Client(), sess, readingList, readTag)
	if err != nil {
		t.Fatal(err)
	}
	urls := make([]string, len(items))
	for i, it := range items {
		urls[i] = it.url
	}
	want := []string{"https://a.example/1", "https://a.example/2", "https://a.example/3"}
	if len(urls) != len(want) {
		t.Fatalf("items = %v, want %v", urls, want)
	}
	for i := range want {
		if urls[i] != want[i] {
			t.Fatalf("items = %v, want %v (canonical preferred, pages concatenated)", urls, want)
		}
	}
	if len(gotC) != 2 || gotC[0] != "" || gotC[1] != "page2" {
		t.Fatalf("continuation params = %v, want [\"\" page2]", gotC)
	}
	for _, xt := range gotXT {
		if xt != readTag {
			t.Fatalf("xt = %q, want %q on every page", xt, readTag)
		}
	}
}

// A pathological server that always returns a continuation must not loop us
// forever — paging stops at the cap.
func TestFetchStreamItemsCapsPages(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"items":[{"id":"x","canonical":[{"href":"https://a.example/x"}]}],"continuation":"again"}`)
	}))
	defer srv.Close()

	sess := &session{base: srv.URL, auth: "tok"}
	if _, err := fetchStreamItems(context.Background(), srv.Client(), sess, starredTag, ""); err != nil {
		t.Fatal(err)
	}
	if pages != 30 {
		t.Fatalf("pages fetched = %d, want the 30-page cap", pages)
	}
}
