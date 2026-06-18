package sources

import (
	"testing"

	"hive/internal/models"
)

// feed asserts a Normalized is a KindFeed and returns its (url, sourceType),
// mirroring the Rust test helper of the same name.
func feed(t *testing.T, n Normalized) (string, models.SourceType) {
	t.Helper()
	if n.Kind != KindFeed {
		t.Fatalf("expected Feed, got %+v", n)
	}
	return n.URL, n.SourceType
}

// ── YouTube: channel id directly in the path ──────────────────────

func TestYoutubeChannelURLRewritesToFeed(t *testing.T) {
	url, st := feed(t, NormalizeSource("https://www.youtube.com/channel/UCXuqSBlHAE6Xw-yeJA0Tunw"))
	if url != "https://www.youtube.com/feeds/videos.xml?channel_id=UCXuqSBlHAE6Xw-yeJA0Tunw" {
		t.Fatalf("unexpected url: %s", url)
	}
	if st != models.SourceYoutube {
		t.Fatalf("unexpected source type: %s", st)
	}
}

func TestYoutubeChannelURLWithoutScheme(t *testing.T) {
	url, _ := feed(t, NormalizeSource("youtube.com/channel/UCXuqSBlHAE6Xw-yeJA0Tunw"))
	if !hasSuffix(url, "channel_id=UCXuqSBlHAE6Xw-yeJA0Tunw") {
		t.Fatalf("unexpected url: %s", url)
	}
}

func TestYoutubePlaylistURLRewritesToPlaylistFeed(t *testing.T) {
	url, st := feed(t, NormalizeSource("https://www.youtube.com/playlist?list=PLFgquLnL59alW3xmYiWRaoz0oM3H17Lth"))
	if url != "https://www.youtube.com/feeds/videos.xml?playlist_id=PLFgquLnL59alW3xmYiWRaoz0oM3H17Lth" {
		t.Fatalf("unexpected url: %s", url)
	}
	if st != models.SourceYoutube {
		t.Fatalf("unexpected source type: %s", st)
	}
}

// ── YouTube: vanity URLs that need a page fetch ───────────────────

func TestYoutubeHandleNeedsResolution(t *testing.T) {
	n := NormalizeSource("https://www.youtube.com/@veritasium")
	if n.Kind != KindNeedsYoutubeResolution {
		t.Fatalf("expected NeedsYoutubeResolution, got %+v", n)
	}
	if !contains(n.PageURL, "@veritasium") {
		t.Fatalf("page_url should contain @veritasium, got %s", n.PageURL)
	}
}

func TestYoutubeCAndUserNeedResolution(t *testing.T) {
	for _, u := range []string{
		"https://www.youtube.com/c/Vsauce",
		"https://www.youtube.com/user/Vsauce",
	} {
		n := NormalizeSource(u)
		if n.Kind != KindNeedsYoutubeResolution {
			t.Fatalf("expected NeedsYoutubeResolution for %s, got %+v", u, n)
		}
	}
}

func TestYoutuBeShortLinkNeedsResolution(t *testing.T) {
	n := NormalizeSource("https://youtu.be/dQw4w9WgXcQ")
	if n.Kind != KindNeedsYoutubeResolution {
		t.Fatalf("expected NeedsYoutubeResolution, got %+v", n)
	}
}

func TestYoutubeExistingFeedURLUntouched(t *testing.T) {
	n := NormalizeSource("https://www.youtube.com/feeds/videos.xml?channel_id=UCXuqSBlHAE6Xw-yeJA0Tunw")
	if n.Kind != KindUntouched {
		t.Fatalf("expected Untouched, got %+v", n)
	}
}

// ── Reddit ────────────────────────────────────────────────────────

func TestRedditSubredditRewritesToRSS(t *testing.T) {
	url, st := feed(t, NormalizeSource("https://www.reddit.com/r/rust"))
	if url != "https://www.reddit.com/r/rust/.rss" {
		t.Fatalf("unexpected url: %s", url)
	}
	if st != models.SourceReddit {
		t.Fatalf("unexpected source type: %s", st)
	}
}

func TestRedditTrailingSlashAndNoWWW(t *testing.T) {
	for _, input := range []string{
		"https://www.reddit.com/r/rust/",
		"https://reddit.com/r/rust",
		"reddit.com/r/rust/",
		"https://old.reddit.com/r/rust/",
	} {
		url, _ := feed(t, NormalizeSource(input))
		if url != "https://www.reddit.com/r/rust/.rss" {
			t.Fatalf("input %s: unexpected url: %s", input, url)
		}
	}
}

func TestRedditListingVariantPreserved(t *testing.T) {
	url, _ := feed(t, NormalizeSource("https://www.reddit.com/r/rust/top"))
	if url != "https://www.reddit.com/r/rust/top/.rss" {
		t.Fatalf("unexpected url: %s", url)
	}
}

func TestRedditPostPermalinkNotTreatedAsFeed(t *testing.T) {
	// A comment permalink must not become a bogus subreddit feed.
	n := NormalizeSource("https://www.reddit.com/r/rust/comments/abc123/some_title/")
	if n.Kind != KindUntouched {
		t.Fatalf("expected Untouched, got %+v", n)
	}
}

func TestRedditHomePageUntouched(t *testing.T) {
	n := NormalizeSource("https://www.reddit.com/")
	if n.Kind != KindUntouched {
		t.Fatalf("expected Untouched, got %+v", n)
	}
}

// ── Mastodon ──────────────────────────────────────────────────────

func TestMastodonProfileRewritesToRSS(t *testing.T) {
	url, st := feed(t, NormalizeSource("https://mastodon.social/@Gargron"))
	if url != "https://mastodon.social/@Gargron.rss" {
		t.Fatalf("unexpected url: %s", url)
	}
	if st != models.SourceMastodon {
		t.Fatalf("unexpected source type: %s", st)
	}
}

func TestMastodonPreservesCustomInstance(t *testing.T) {
	url, _ := feed(t, NormalizeSource("https://hachyderm.io/@nova"))
	if url != "https://hachyderm.io/@nova.rss" {
		t.Fatalf("unexpected url: %s", url)
	}
}

func TestMastodonAlreadyRSSUntouched(t *testing.T) {
	n := NormalizeSource("https://mastodon.social/@Gargron.rss")
	if n.Kind != KindUntouched {
		t.Fatalf("expected Untouched, got %+v", n)
	}
}

func TestMastodonDeepPathUntouched(t *testing.T) {
	// `/@user/123` is a single post, not a profile.
	n := NormalizeSource("https://mastodon.social/@Gargron/109")
	if n.Kind != KindUntouched {
		t.Fatalf("expected Untouched, got %+v", n)
	}
}

// ── Non-matching input ────────────────────────────────────────────

func TestPlainFeedURLUntouched(t *testing.T) {
	n := NormalizeSource("https://blog.rust-lang.org/feed.xml")
	if n.Kind != KindUntouched {
		t.Fatalf("expected Untouched, got %+v", n)
	}
}

func TestEmptyInputUntouched(t *testing.T) {
	n := NormalizeSource("   ")
	if n.Kind != KindUntouched {
		t.Fatalf("expected Untouched, got %+v", n)
	}
}

func TestGarbageInputUntouched(t *testing.T) {
	n := NormalizeSource("not a url at all")
	if n.Kind != KindUntouched {
		t.Fatalf("expected Untouched, got %+v", n)
	}
}

// ── rsshub:// expansion ───────────────────────────────────────────

func TestExpandRSSHubMapsRouteOntoInstance(t *testing.T) {
	got := ExpandRSSHub("rsshub://github/issue/DIYgod/RSSHub", DefaultRSSHubInstance)
	wantStr := "https://rsshub.app/github/issue/DIYgod/RSSHub"
	if got == nil || *got != wantStr {
		t.Fatalf("expected %q, got %v", wantStr, deref(got))
	}
}

func TestExpandRSSHubHonoursCustomSelfHostedInstance(t *testing.T) {
	got := ExpandRSSHub("rsshub://bilibili/user/video/2267573", "https://rss.example.com/")
	wantStr := "https://rss.example.com/bilibili/user/video/2267573"
	if got == nil || *got != wantStr {
		t.Fatalf("expected %q, got %v", wantStr, deref(got))
	}
}

func TestExpandRSSHubIsCaseInsensitiveAndTrimsSlashes(t *testing.T) {
	// Scheme casing varies; leading route slashes and instance trailing slashes
	// must not produce a doubled `//`.
	got := ExpandRSSHub("  RSSHub:///telegram/channel/awesomeDIYgod  ", "https://rsshub.app")
	wantStr := "https://rsshub.app/telegram/channel/awesomeDIYgod"
	if got == nil || *got != wantStr {
		t.Fatalf("expected %q, got %v", wantStr, deref(got))
	}
}

func TestExpandRSSHubRejectsNonRSSHubAndEmptyRoute(t *testing.T) {
	if got := ExpandRSSHub("https://rsshub.app/foo", DefaultRSSHubInstance); got != nil {
		t.Fatalf("expected nil, got %v", *got)
	}
	if got := ExpandRSSHub("rsshub://", DefaultRSSHubInstance); got != nil {
		t.Fatalf("expected nil, got %v", *got)
	}
	if got := ExpandRSSHub("rss", DefaultRSSHubInstance); got != nil {
		t.Fatalf("expected nil, got %v", *got)
	}
}

// ── channelId extraction ──────────────────────────────────────────

func TestExtractChannelIDFromJSONKey(t *testing.T) {
	html := `<html><script>var x = {"channelId":"UCXuqSBlHAE6Xw-yeJA0Tunw","foo":1};</script></html>`
	assertChannelID(t, ExtractChannelID(html), "UCXuqSBlHAE6Xw-yeJA0Tunw")
}

func TestExtractChannelIDFromExternalIDKey(t *testing.T) {
	html := `{"metadata":{"externalId":"UCXuqSBlHAE6Xw-yeJA0Tunw"}}`
	assertChannelID(t, ExtractChannelID(html), "UCXuqSBlHAE6Xw-yeJA0Tunw")
}

func TestExtractChannelIDPrefersOwnerOverRecommended(t *testing.T) {
	// A real channel page embeds `channelId` for recommended/sidebar channels
	// *before* the page's own id. The owner-specific `externalId` (and the
	// <head> canonical link) must win so the user does not get subscribed to a
	// recommended channel by mistake.
	html := `<head>
            <link rel="canonical" href="https://www.youtube.com/channel/UCXuqSBlHAE6Xw-yeJA0Tunw">
            </head><body><script>var data = {
              "relatedChannels":[{"channelId":"UCwrongRecommendedAAAAA1"}],
              "metadata":{"externalId":"UCXuqSBlHAE6Xw-yeJA0Tunw"}
            };</script></body>`
	got := ExtractChannelID(html)
	if got == nil || *got != "UCXuqSBlHAE6Xw-yeJA0Tunw" {
		t.Fatalf("owner id must win over a recommended channel's id, got %v", deref(got))
	}
}

func TestExtractChannelIDFallsBackToPlainChannelIDKey(t *testing.T) {
	// When no owner-specific signal exists, the plain `channelId` key is still
	// consulted as a last resort.
	html := `{"channelId":"UCXuqSBlHAE6Xw-yeJA0Tunw"}`
	assertChannelID(t, ExtractChannelID(html), "UCXuqSBlHAE6Xw-yeJA0Tunw")
}

func TestExtractChannelIDFromCanonicalLink(t *testing.T) {
	html := `<head><link rel="canonical" href="https://www.youtube.com/channel/UCXuqSBlHAE6Xw-yeJA0Tunw"></head>`
	assertChannelID(t, ExtractChannelID(html), "UCXuqSBlHAE6Xw-yeJA0Tunw")
}

func TestExtractChannelIDCanonicalAttrsReordered(t *testing.T) {
	html := `<link href="https://www.youtube.com/channel/UCXuqSBlHAE6Xw-yeJA0Tunw" rel="canonical">`
	assertChannelID(t, ExtractChannelID(html), "UCXuqSBlHAE6Xw-yeJA0Tunw")
}

func TestExtractChannelIDCanonicalScanSurvivesLeadingNonTagMatch(t *testing.T) {
	// A `rel="canonical"` substring with no opening `<` before it (it leads the
	// document — a stray token or escaped script text) is not a real tag. The
	// previous scan used `?` on the `rfind('<')`, aborting the whole
	// canonicalChannelID scan on this non-tag occurrence so the genuine
	// `<link rel="canonical">` that follows was never reached.
	html := "rel=\"canonical\" stray leading text >\n            " +
		"<link rel=\"canonical\" href=\"https://www.youtube.com/channel/UCXuqSBlHAE6Xw-yeJA0Tunw\">"
	got := canonicalChannelID(html)
	if got == nil || *got != "UCXuqSBlHAE6Xw-yeJA0Tunw" {
		t.Fatalf("a non-tag leading rel=\"canonical\" must not abort the scan, got %v", deref(got))
	}
}

func TestExtractChannelIDCanonicalHandlesUnterminatedTag(t *testing.T) {
	// A `rel="canonical"` with no closing `>` anywhere after it (truncated /
	// garbled HTML) must not panic — the scan simply stops. The bare
	// `/channel/UC…` substring rule still recovers the id.
	html := "<link rel=\"canonical\" href=\"https://www.youtube.com/channel/UCXuqSBlHAE6Xw-yeJA0Tunw"
	assertChannelID(t, ExtractChannelID(html), "UCXuqSBlHAE6Xw-yeJA0Tunw")
}

func TestExtractChannelIDFromOgURL(t *testing.T) {
	html := `<meta property="og:url" content="http://www.youtube.com/channel/UCXuqSBlHAE6Xw-yeJA0Tunw">`
	assertChannelID(t, ExtractChannelID(html), "UCXuqSBlHAE6Xw-yeJA0Tunw")
}

func TestExtractChannelIDNoneWhenAbsent(t *testing.T) {
	if got := ExtractChannelID("<html>no channel here</html>"); got != nil {
		t.Fatalf("expected nil, got %v", *got)
	}
}

func TestExtractChannelIDRejectsMalformedID(t *testing.T) {
	// Too short — not a real 24-char channel id.
	html := `{"channelId":"UCshort"}`
	if got := ExtractChannelID(html); got != nil {
		t.Fatalf("expected nil, got %v", *got)
	}
}

func TestYoutubeFeedURLBuilder(t *testing.T) {
	if got := YoutubeFeedURL("UCabc"); got != "https://www.youtube.com/feeds/videos.xml?channel_id=UCabc" {
		t.Fatalf("unexpected url: %s", got)
	}
}

// ── helpers ───────────────────────────────────────────────────────

func assertChannelID(t *testing.T, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("expected %q, got %v", want, deref(got))
	}
}

func deref(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
