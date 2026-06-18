package discovery

import (
	"strings"
	"testing"
)

// ── bundled directory integrity ───────────────────────────────────

func TestDirectoryParsesAndIsSubstantial(t *testing.T) {
	dir := current()
	if len(dir) < 40 {
		t.Fatalf("expected a sizeable directory, got %d", len(dir))
	}
	// Every entry must have the required, non-empty fields.
	for _, e := range dir {
		if e.Title == "" {
			t.Errorf("entry missing title")
		}
		if !strings.HasPrefix(e.FeedURL, "http") {
			t.Errorf("bad feed url: %s", e.FeedURL)
		}
		if e.Category == "" {
			t.Errorf("entry missing category: %s", e.Title)
		}
		if e.Lang == "" {
			t.Errorf("entry missing lang: %s", e.Title)
		}
	}
}

func TestDirectoryCuratesEachSupportedLanguage(t *testing.T) {
	// Every UI locale the app ships must have its own curated slice, or
	// Explore would silently fall back to English for that user.
	dir := current()
	for _, lang := range []string{"en", "zh", "ja"} {
		n := 0
		for _, e := range dir {
			if primaryLang(e.Lang) == lang {
				n++
			}
		}
		if n < 10 {
			t.Errorf("expected a curated %s slice, got %d", lang, n)
		}
	}
}

func TestDirectoryHasNoDuplicateFeedURLs(t *testing.T) {
	dir := current()
	seen := map[string]bool{}
	for _, e := range dir {
		if seen[e.FeedURL] {
			t.Errorf("duplicate feed url: %s", e.FeedURL)
		}
		seen[e.FeedURL] = true
	}
}

// ── SearchDirectory ──────────────────────────────────────────────

// langCount returns the count of directory entries in one primary-language slice.
func langCount(lang string) int {
	n := 0
	for _, e := range current() {
		if primaryLang(e.Lang) == lang {
			n++
		}
	}
	return n
}

func TestEmptyQueryReturnsWholeDirectory(t *testing.T) {
	// Language is an ordering preference, not a filter: the gallery browses
	// every entry regardless of the UI locale.
	total := len(current())
	if got := len(SearchDirectory("", "en")); got != total {
		t.Errorf("empty en: got %d, want %d", got, total)
	}
	if got := len(SearchDirectory("   ", "en")); got != total {
		t.Errorf("blank en: got %d, want %d", got, total)
	}
	if got := len(SearchDirectory("", "zh")); got != total {
		t.Errorf("empty zh: got %d, want %d", got, total)
	}
	if got := len(SearchDirectory("", "ko")); got != total {
		t.Errorf("uncurated locale must still browse everything: got %d, want %d", got, total)
	}
}

func TestPreferredLanguageSortsFirst(t *testing.T) {
	// The zh slice leads for a zh locale; every other entry follows.
	zhFirst := SearchDirectory("", "zh")
	nZh := langCount("zh")
	if len(zhFirst) < nZh {
		t.Fatalf("expected at least the zh slice, got %d", len(zhFirst))
	}
	zhTitles := map[string]bool{}
	for _, e := range current() {
		if primaryLang(e.Lang) == "zh" {
			zhTitles[e.Title] = true
		}
	}
	for i, r := range zhFirst[:nZh] {
		if !zhTitles[r.Title] {
			t.Errorf("position %d should be a zh entry, got %s", i, r.Title)
		}
	}
	for _, r := range zhFirst[nZh:] {
		if zhTitles[r.Title] {
			t.Errorf("zh entry sorted after the zh block: %s", r.Title)
		}
	}
}

func TestPrimarySubtagIsMatched(t *testing.T) {
	// "zh-CN" / "ZH" must produce the same ordering as "zh".
	base := SearchDirectory("", "zh")
	for _, tag := range []string{"zh-CN", "ZH"} {
		got := SearchDirectory("", tag)
		if len(got) != len(base) || got[0].Title != base[0].Title {
			t.Errorf("%s: ordering differs from zh", tag)
		}
	}
}

func TestSearchMatchesTitleCaseInsensitively(t *testing.T) {
	hits := SearchDirectory("hacker news", "en")
	found := false
	for _, r := range hits {
		if r.Title == "Hacker News" {
			found = true
		}
	}
	if !found {
		t.Error("expected a Hacker News hit")
	}
	upper := SearchDirectory("HACKER NEWS", "en")
	if len(hits) != len(upper) {
		t.Errorf("case-insensitive mismatch: %d vs %d", len(hits), len(upper))
	}
}

func TestSearchCrossesLanguages(t *testing.T) {
	// The whole point of preference-not-filter: a zh user finds English feeds.
	hits := SearchDirectory("hacker news", "zh")
	found := false
	for _, r := range hits {
		if r.Title == "Hacker News" {
			found = true
		}
	}
	if !found {
		t.Error("a zh locale must still find Hacker News")
	}
}

func TestSearchMatchesCategory(t *testing.T) {
	hits := SearchDirectory("science", "en")
	if len(hits) < 3 {
		t.Errorf("expected several Science feeds, got %d", len(hits))
	}
	for _, r := range hits {
		if !r.FromDirectory {
			t.Errorf("result not from directory: %s", r.Title)
		}
	}
	// The category match must surface every entry filed under Science.
	scienceCount := 0
	for _, r := range hits {
		if r.Category != nil && *r.Category == "Science" {
			scienceCount++
		}
	}
	wantScience := 0
	for _, e := range current() {
		if e.Category == "Science" {
			wantScience++
		}
	}
	if scienceCount != wantScience {
		t.Errorf("every Science-category feed should be returned: got %d, want %d", scienceCount, wantScience)
	}
}

func TestSearchMatchesDescription(t *testing.T) {
	// "astronomy" appears only in a description, not a title/category.
	hits := SearchDirectory("astronomy", "en")
	if len(hits) == 0 {
		t.Error("expected at least one description match")
	}
}

func TestSearchNoMatchReturnsEmpty(t *testing.T) {
	if got := SearchDirectory("zzz-no-such-feed-xyz", "en"); len(got) != 0 {
		t.Errorf("expected empty, got %d", len(got))
	}
}

func TestSearchResultsCarryDirectoryMetadata(t *testing.T) {
	hits := SearchDirectory("Rust Blog", "en")
	if len(hits) == 0 {
		t.Fatal("expected a Rust Blog hit")
	}
	r := hits[0]
	if !r.FromDirectory {
		t.Error("expected fromDirectory")
	}
	if r.SiteURL == nil {
		t.Error("expected siteUrl")
	}
	if r.Category == nil {
		t.Error("expected category")
	}
	if !strings.HasPrefix(r.FeedURL, "http") {
		t.Errorf("bad feed url: %s", r.FeedURL)
	}
}

// ── ResultFromScrape ──────────────────────────────────────────────

func TestResultFromScrapeUsesTitleOrFallsBackToURL(t *testing.T) {
	title := "My Blog"
	r := ResultFromScrape("https://example.com/feed.xml", &title)
	if r.Title != "My Blog" {
		t.Errorf("title: got %q", r.Title)
	}
	if r.FromDirectory {
		t.Error("scrape result must not be from directory")
	}
	if r.SiteURL != nil || r.Category != nil || r.Description != nil {
		t.Error("scrape result should have nil site/category/description")
	}

	// With no title, the feed URL is used as the display name.
	r2 := ResultFromScrape("https://example.com/feed.xml", nil)
	if r2.Title != "https://example.com/feed.xml" {
		t.Errorf("fallback title: got %q", r2.Title)
	}
}

// ── LooksLikeURL ────────────────────────────────────────────────

func TestLooksLikeURLDetectsSchemedAndBare(t *testing.T) {
	for _, q := range []string{
		"https://example.com",
		"http://example.com/feed",
		"example.com",
		"blog.example.co.uk/path",
		"news.ycombinator.com",
	} {
		if !LooksLikeURL(q) {
			t.Errorf("expected URL-like: %q", q)
		}
	}
}

func TestLooksLikeURLRejectsPlainSearchTerms(t *testing.T) {
	for _, q := range []string{
		"science news",
		"rust",
		"",
		"   ",
		"version.1", // a dotted token whose last label is not a plausible TLD
	} {
		if LooksLikeURL(q) {
			t.Errorf("expected not URL-like: %q", q)
		}
	}
}

func TestNormalizeQueryURLAddsSchemeWhenMissing(t *testing.T) {
	cases := map[string]string{
		"example.com":        "https://example.com",
		"http://example.com": "http://example.com",
		"  example.com  ":    "https://example.com",
	}
	for in, want := range cases {
		if got := NormalizeQueryURL(in); got != want {
			t.Errorf("NormalizeQueryURL(%q): got %q, want %q", in, got, want)
		}
	}
}

// ── ParseDeepLink ───────────────────────────────────────────────

// deref is a test helper that returns the pointed-to string, or a sentinel
// when nil, so failures read clearly.
func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestDeepLinkBasicSubscribe(t *testing.T) {
	link := ParseDeepLink("hive://subscribe?url=https://example.com/feed.xml")
	if link == nil || *link != "https://example.com/feed.xml" {
		t.Errorf("got %q", deref(link))
	}
}

func TestDeepLinkDecodesPercentEncoding(t *testing.T) {
	link := ParseDeepLink("hive://subscribe?url=https%3A%2F%2Fexample.com%2Ffeed%3Fa%3D1%26b%3D2")
	if link == nil || *link != "https://example.com/feed?a=1&b=2" {
		t.Errorf("got %q", deref(link))
	}
}

func TestDeepLinkAcceptsActionInPath(t *testing.T) {
	// hive:///subscribe?... — action in the path rather than the host.
	link := ParseDeepLink("hive:///subscribe?url=https://example.com/feed.xml")
	if link == nil || *link != "https://example.com/feed.xml" {
		t.Errorf("got %q", deref(link))
	}
}

func TestDeepLinkRejectsWrongScheme(t *testing.T) {
	if got := ParseDeepLink("https://subscribe?url=https://example.com/feed.xml"); got != nil {
		t.Errorf("got %q", deref(got))
	}
	if got := ParseDeepLink("feedly://subscribe?url=https://example.com"); got != nil {
		t.Errorf("got %q", deref(got))
	}
	// The old papr:// scheme is no longer accepted.
	if got := ParseDeepLink("papr://subscribe?url=https://example.com/feed.xml"); got != nil {
		t.Errorf("papr scheme should be rejected now, got %q", deref(got))
	}
}

func TestDeepLinkRejectsUnknownAction(t *testing.T) {
	if got := ParseDeepLink("hive://unsubscribe?url=https://example.com"); got != nil {
		t.Errorf("got %q", deref(got))
	}
}

func TestDeepLinkRejectsMissingURLParam(t *testing.T) {
	if got := ParseDeepLink("hive://subscribe"); got != nil {
		t.Errorf("got %q", deref(got))
	}
	if got := ParseDeepLink("hive://subscribe?foo=bar"); got != nil {
		t.Errorf("got %q", deref(got))
	}
}

func TestDeepLinkRejectsEmptyURLParam(t *testing.T) {
	if got := ParseDeepLink("hive://subscribe?url="); got != nil {
		t.Errorf("got %q", deref(got))
	}
}

func TestDeepLinkRejectsGarbageAndEmpty(t *testing.T) {
	for _, in := range []string{"", "   ", "not a url at all"} {
		if got := ParseDeepLink(in); got != nil {
			t.Errorf("ParseDeepLink(%q): got %q", in, deref(got))
		}
	}
}
