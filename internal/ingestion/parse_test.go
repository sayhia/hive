package ingestion

import (
	"testing"
	"time"

	"hive/internal/models"
)

func TestDetectSourceType(t *testing.T) {
	reddit := []string{
		"https://www.reddit.com/r/rust/.rss",
		"https://reddit.com/r/rust/.rss",
		"https://old.reddit.com/r/rust/top/.rss",
		"https://www.reddit.com/r/rust/",
	}
	for _, u := range reddit {
		if got := DetectSourceType(u); got != models.SourceReddit {
			t.Errorf("DetectSourceType(%q) = %q, want reddit", u, got)
		}
	}
	notReddit := map[string]models.SourceType{
		"https://www.reddit.com/.rss":       models.SourceRSS, // front-page aggregate
		"https://notreddit.com/r/rust/.rss": models.SourceRSS, // lookalike host
		"https://example.com/feed.xml":      models.SourceRSS,
		"https://www.youtube.com/feeds/x":   models.SourceYoutube,
		"https://bsky.app/profile/x":        models.SourceBluesky,
	}
	for u, want := range notReddit {
		if got := DetectSourceType(u); got != want {
			t.Errorf("DetectSourceType(%q) = %q, want %q", u, got, want)
		}
	}
}

func TestResolveURL(t *testing.T) {
	cases := []struct{ href, base, want string }{
		{"https://other.example.com/post/1", "https://feed.example.com/rss", "https://other.example.com/post/1"},
		{"/posts/123", "https://blog.example.com/feed.atom", "https://blog.example.com/posts/123"},
		{"123", "https://blog.example.com/posts/", "https://blog.example.com/posts/123"},
		{"/posts/1", "not a url", "/posts/1"},
	}
	for _, c := range cases {
		if got := resolveURL(c.href, c.base); got != c.want {
			t.Errorf("resolveURL(%q, %q) = %q, want %q", c.href, c.base, got, c.want)
		}
	}
}

func TestClampPublishDate(t *testing.T) {
	past := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if got := clampPublishDate(past); !got.Equal(past) {
		t.Errorf("past date altered: %v != %v", got, past)
	}
	skew := time.Now().UTC().Add(6 * time.Hour)
	if got := clampPublishDate(skew); !got.Equal(skew) {
		t.Errorf("small skew altered: %v != %v", got, skew)
	}
	far := time.Now().UTC().Add(365 * 24 * time.Hour)
	clamped := clampPublishDate(far)
	if !clamped.Before(far) {
		t.Errorf("far future not clamped: %v", clamped)
	}
	if d := time.Since(clamped); d < -5*time.Second || d > 5*time.Second {
		t.Errorf("clamped not near now: %v", clamped)
	}
}

func TestMimeFromURL(t *testing.T) {
	cases := map[string]string{
		"https://cdn.example.com/ep1.mp3":              "audio/mpeg",
		"http://x/y/show.m4a":                          "audio/aac",
		"https://x/clip.MP4":                           "video/mp4",
		"https://x/clip.webm":                          "video/webm",
		"https://traffic.example.com/ep.mp3?token=abc": "audio/mpeg",
		"https://x/ep.m4a#chapter2":                    "audio/aac",
		"https://example.com/article.html":             "",
		"https://example.com/feed":                     "",
		"https://example.com/image.jpg":                "",
	}
	for u, want := range cases {
		if got := mimeFromURL(u); got != want {
			t.Errorf("mimeFromURL(%q) = %q, want %q", u, got, want)
		}
	}
}

func TestUppercaseEnclosureMimeKept(t *testing.T) {
	rss := []byte(`<?xml version="1.0"?>
		<rss version="2.0"><channel><title>Pod</title>
		  <item>
			<title>Ep 1</title>
			<guid>ep1</guid>
			<enclosure url="https://cdn.example.com/ep1.bin" type="AUDIO/MPEG" length="123" />
		  </item>
		</channel></rss>`)
	parsed, err := ParseFeed(rss, "https://example.com/feed")
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Articles) != 1 {
		t.Fatalf("articles = %d, want 1", len(parsed.Articles))
	}
	enc := parsed.Articles[0].Enclosures
	if len(enc) != 1 {
		t.Fatalf("enclosures = %d, want 1 (uppercase audio must survive)", len(enc))
	}
	if enc[0].MimeType == nil || *enc[0].MimeType != "audio/mpeg" {
		t.Errorf("mime = %v, want audio/mpeg", enc[0].MimeType)
	}
	// The audio enclosure refines the feed to a podcast.
	if got := RefineSourceType(models.SourceRSS, parsed, "https://example.com/feed"); got != models.SourcePodcast {
		t.Errorf("RefineSourceType = %q, want podcast", got)
	}
}

// A CNKI-style feed: no <guid>, and every <link> carries a per-request token
// that changes between fetches. Two fetches of the *same* item must yield the
// *same* guid so (feed_id, guid) dedup collapses them — otherwise every
// refresh re-ingests the whole issue as new articles.
func cnkiLike(token string) []byte {
	return []byte(`<?xml version="1.0"?>
		<rss version="2.0"><channel><title>CNKI</title>
		  <item>
			<title>某某研究进展</title>
			<link>https://kns.cnki.net/kcms2/article/abstract?v=` + token + `</link>
			<pubDate>Tue, 01 Jan 2026 00:00:00 GMT</pubDate>
		  </item>
		</channel></rss>`)
}

func TestGuidlessVolatileURLDedupsByTitleAndDate(t *testing.T) {
	a, err := ParseFeed(cnkiLike("TOKEN_A"), "https://rss.cnki.net/x")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseFeed(cnkiLike("TOKEN_B"), "https://rss.cnki.net/x")
	if err != nil {
		t.Fatal(err)
	}
	if a.Articles[0].GUID != b.Articles[0].GUID {
		t.Errorf("guid changed across fetches: %q vs %q", a.Articles[0].GUID, b.Articles[0].GUID)
	}
	if !a.Articles[0].SyntheticGUID {
		t.Error("composite key must be flagged synthetic for the upsert URL fallback")
	}
	// The URL itself is still captured verbatim (its token differs).
	if *a.Articles[0].URL == *b.Articles[0].URL {
		t.Error("volatile URLs should differ between fetches")
	}
}

func TestGuidlessKeepsDistinctTitlesDistinct(t *testing.T) {
	// Two different articles sharing a publish date must not collapse.
	rss := []byte(`<?xml version="1.0"?>
		<rss version="2.0"><channel><title>CNKI</title>
		  <item><title>甲文章</title>
			<link>https://kns.cnki.net/a?v=1</link>
			<pubDate>Tue, 01 Jan 2026 00:00:00 GMT</pubDate></item>
		  <item><title>乙文章</title>
			<link>https://kns.cnki.net/b?v=2</link>
			<pubDate>Tue, 01 Jan 2026 00:00:00 GMT</pubDate></item>
		</channel></rss>`)
	p, err := ParseFeed(rss, "https://rss.cnki.net/x")
	if err != nil {
		t.Fatal(err)
	}
	if p.Articles[0].GUID == p.Articles[1].GUID {
		t.Error("distinct titles must produce distinct guids")
	}
}

func TestRealGuidKeptAndNotSynthetic(t *testing.T) {
	rss := []byte(`<?xml version="1.0"?>
		<rss version="2.0"><channel><title>Blog</title>
		  <item><title>Post</title><guid>tag:blog,2026:p1</guid>
			<link>https://blog.example.com/p1</link></item>
		</channel></rss>`)
	p, err := ParseFeed(rss, "https://blog.example.com/feed")
	if err != nil {
		t.Fatal(err)
	}
	if p.Articles[0].GUID != "tag:blog,2026:p1" || p.Articles[0].SyntheticGUID {
		t.Errorf("real guid must be kept verbatim and unflagged, got %q synthetic=%v",
			p.Articles[0].GUID, p.Articles[0].SyntheticGUID)
	}
}

func TestGuidlessTitlelessFallsBackToURL(t *testing.T) {
	rss := []byte(`<?xml version="1.0"?>
		<rss version="2.0"><channel><title>Blog</title>
		  <item><link>https://blog.example.com/p2</link>
			<description>text</description></item>
		</channel></rss>`)
	p, err := ParseFeed(rss, "https://blog.example.com/feed")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Articles) != 1 {
		t.Fatalf("articles = %d, want 1", len(p.Articles))
	}
	if p.Articles[0].GUID != "https://blog.example.com/p2" || p.Articles[0].SyntheticGUID {
		t.Errorf("title-less entry should key off its URL unflagged, got %q synthetic=%v",
			p.Articles[0].GUID, p.Articles[0].SyntheticGUID)
	}
}

func TestDiscoverFeeds(t *testing.T) {
	html := `<html><head>
		<link rel="alternate" type="application/rss+xml" href="/feed.xml">
		<link rel="alternate" type="application/atom+xml" href="https://abs.example.com/atom">
		<link rel="alternate" type="text/html" href="/not-a-feed">
		</head></html>`
	got := DiscoverFeeds(html, "https://example.com/blog/")
	if len(got) != 2 {
		t.Fatalf("discovered %d feeds, want 2: %v", len(got), got)
	}
	if got[0] != "https://example.com/feed.xml" {
		t.Errorf("first = %q, want resolved /feed.xml", got[0])
	}
}
