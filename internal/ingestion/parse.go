package ingestion

import (
	"bytes"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/mmcdole/gofeed"

	"hive/internal/apperr"
	"hive/internal/db"
	"hive/internal/models"
	"hive/internal/sanitize"
)

// ParsedFeed is the metadata + articles extracted from a single feed document.
type ParsedFeed struct {
	Title       *string
	SiteURL     *string
	Description *string
	Icon        *string
	Articles    []db.NewArticle
}

// ParseFeed parses raw feed bytes (RSS/Atom/JSON via gofeed). baseURL is the
// feed URL, used to resolve relative entry links. A fresh parser is allocated
// per call because gofeed.Parser is not safe for concurrent use.
func ParseFeed(raw []byte, baseURL string) (*ParsedFeed, error) {
	feed, err := gofeed.NewParser().Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, apperr.Wrap("feedParse", err)
	}

	siteURL := feed.Link
	if siteURL == "" {
		siteURL = baseURL
	}
	base := siteURL

	var articles []db.NewArticle
	for _, item := range feed.Items {
		if a := mapEntry(item, base); a != nil {
			articles = append(articles, *a)
		}
	}

	pf := &ParsedFeed{
		SiteURL:  &siteURL,
		Articles: articles,
	}
	if feed.Title != "" {
		pf.Title = &feed.Title
	}
	if feed.Description != "" {
		pf.Description = &feed.Description
	}
	if feed.Image != nil && feed.Image.URL != "" {
		pf.Icon = &feed.Image.URL
	}
	return pf, nil
}

// resolveURL resolves a possibly-relative href against the feed's base URL. An
// already-absolute href is returned unchanged; an unparseable pair falls back
// to the raw value rather than dropping the link.
func resolveURL(href, base string) string {
	if u, err := url.Parse(href); err == nil && u.IsAbs() {
		return href
	}
	b, err := url.Parse(base)
	if err != nil {
		return href
	}
	ref, err := url.Parse(href)
	if err != nil {
		return href
	}
	return b.ResolveReference(ref).String()
}

// mimeFromURL infers an audio/video MIME type from a media URL's file
// extension, for enclosures whose feed omitted the type attribute. Returns ""
// for unknown or non-media extensions.
func mimeFromURL(rawURL string) string {
	path := rawURL
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	dot := strings.LastIndex(path, ".")
	if dot < 0 {
		return ""
	}
	switch strings.ToLower(path[dot+1:]) {
	case "mp3":
		return "audio/mpeg"
	case "m4a", "aac":
		return "audio/aac"
	case "ogg", "oga", "opus":
		return "audio/ogg"
	case "wav":
		return "audio/wav"
	case "flac":
		return "audio/flac"
	case "mp4", "m4v":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "mov":
		return "video/quicktime"
	default:
		return ""
	}
}

// clampPublishDate clamps a feed-supplied publication date so it never lands in
// the future. A 24-hour grace window absorbs harmless clock skew; anything
// beyond it is clamped to "now", so a misdated entry can't pin itself to the
// top of the newest-first list forever.
func clampPublishDate(date time.Time) time.Time {
	now := time.Now().UTC()
	if date.After(now.Add(24 * time.Hour)) {
		return now
	}
	return date
}

// mediaItem is a flattened MRSS media:content / media:thumbnail entry.
type mediaItem struct {
	url, typ, medium string
	size             int64
}

// mediaContents pulls media:content entries out of an item's extensions.
func mediaContents(item *gofeed.Item) []mediaItem {
	var out []mediaItem
	media, ok := item.Extensions["media"]
	if !ok {
		return out
	}
	for _, ext := range media["content"] {
		m := mediaItem{
			url:    ext.Attrs["url"],
			typ:    ext.Attrs["type"],
			medium: ext.Attrs["medium"],
		}
		if sz, err := strconv.ParseInt(ext.Attrs["fileSize"], 10, 64); err == nil {
			m.size = sz
		}
		if m.url != "" {
			out = append(out, m)
		}
	}
	return out
}

// mediaThumbnail returns the first media:thumbnail URL, if any.
func mediaThumbnail(item *gofeed.Item) string {
	if media, ok := item.Extensions["media"]; ok {
		for _, ext := range media["thumbnail"] {
			if u := ext.Attrs["url"]; u != "" {
				return u
			}
		}
	}
	return ""
}

func mapEntry(item *gofeed.Item, base string) *db.NewArticle {
	var urlPtr *string
	if item.Link != "" {
		resolved := resolveURL(item.Link, base)
		urlPtr = &resolved
	}

	// Title / publish date are needed before the guid fallback below: a feed
	// without a stable id keys off them instead of its (possibly volatile) URL.
	title := item.Title
	if title == "" {
		title = "(untitled)"
	}

	var publishedAt *string
	when := item.PublishedParsed
	if when == nil {
		when = item.UpdatedParsed
	}
	if when != nil {
		s := clampPublishDate(when.UTC()).Format(time.RFC3339)
		publishedAt = &s
	}

	// A real, feed-provided <guid>/<id> is the ideal dedup key. Without one,
	// key off title + publish date — both stable across fetches — because the
	// URL is not: feeds whose links carry a per-request token (e.g. CNKI) get
	// a fresh URL every fetch, so URL-keyed dedup re-ingests the whole issue
	// each refresh. \x1f (unit separator) is an illegal XML character, so the
	// composite can never collide with a legitimate guid. Only a title-less
	// entry falls back to the URL; one with neither cannot be deduped at all.
	guid := item.GUID
	synthetic := false
	if strings.TrimSpace(guid) == "" {
		date := ""
		if publishedAt != nil {
			date = *publishedAt
		}
		switch {
		case strings.TrimSpace(item.Title) != "":
			guid = strings.TrimSpace(item.Title) + "\x1f" + date
			synthetic = true
		case urlPtr != nil:
			guid = *urlPtr
		default:
			return nil // no id, no title, no link → cannot dedup, skip
		}
	}

	rawHTML := item.Content
	if rawHTML == "" {
		rawHTML = item.Description
	}
	var contentHTML *string
	if rawHTML != "" {
		s := sanitize.Sanitize(rawHTML, base)
		contentHTML = &s
	}
	bodyText := sanitize.HTMLToText(rawHTML)

	var summary *string
	if item.Description != "" {
		if s := sanitize.HTMLToText(item.Description); s != "" {
			summary = &s
		}
	}

	// Image: media thumbnail → image-typed media content → first body image.
	imageURL := pickImage(item, contentHTML)

	// Enclosures: audio/video from <enclosure> and media:content.
	enclosures := pickEnclosures(item)

	var author *string
	if len(item.Authors) > 0 && item.Authors[0].Name != "" {
		author = &item.Authors[0].Name
	}

	return &db.NewArticle{
		GUID:          guid,
		SyntheticGUID: synthetic,
		URL:           urlPtr,
		Title:         title,
		Author:        author,
		Summary:       summary,
		ContentHTML:   contentHTML,
		BodyText:      bodyText,
		ImageURL:      imageURL,
		PublishedAt:   publishedAt,
		Enclosures:    enclosures,
	}
}

func pickImage(item *gofeed.Item, contentHTML *string) *string {
	if item.Image != nil && item.Image.URL != "" {
		u := item.Image.URL
		return &u
	}
	if u := mediaThumbnail(item); u != "" {
		return &u
	}
	for _, m := range mediaContents(item) {
		if m.medium == "image" || strings.HasPrefix(strings.ToLower(m.typ), "image") {
			u := m.url
			return &u
		}
	}
	if contentHTML != nil {
		return sanitize.FirstImage(*contentHTML)
	}
	return nil
}

func pickEnclosures(item *gofeed.Item) []models.Enclosure {
	var out []models.Enclosure
	add := func(u, declaredType string, size int64) {
		mime := strings.ToLower(strings.TrimSpace(declaredType))
		if mime == "" {
			mime = mimeFromURL(u)
		}
		if !(strings.HasPrefix(mime, "audio") || strings.HasPrefix(mime, "video")) {
			return
		}
		var mimePtr *string
		if mime != "" {
			mimePtr = &mime
		}
		var lenPtr *int64
		if size > 0 {
			lenPtr = &size
		}
		out = append(out, models.Enclosure{URL: u, MimeType: mimePtr, Length: lenPtr})
	}
	for _, enc := range item.Enclosures {
		var size int64
		if enc.Length != "" {
			size, _ = strconv.ParseInt(enc.Length, 10, 64)
		}
		add(enc.URL, enc.Type, size)
	}
	for _, m := range mediaContents(item) {
		add(m.url, m.typ, m.size)
	}
	return out
}

// DetectSourceType detects the source type from a feed/site URL — drives
// differentiated UI.
func DetectSourceType(rawURL string) models.SourceType {
	host := ""
	if u, err := url.Parse(rawURL); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	switch {
	case strings.Contains(host, "youtube.com") || strings.Contains(host, "youtu.be"):
		return models.SourceYoutube
	case strings.Contains(host, "bsky.app") || strings.Contains(host, "bsky.social"):
		return models.SourceBluesky
	default:
		path := ""
		if u, err := url.Parse(rawURL); err == nil {
			path = u.Path
		}
		if isRedditFeed(host, path) {
			return models.SourceReddit
		}
		return models.SourceRSS
	}
}

// isRedditFeed reports whether host+path look like a Reddit subreddit RSS feed.
// The front-page aggregate (/.rss, no /r/) is intentionally excluded.
func isRedditFeed(host, path string) bool {
	isRedditHost := host == "reddit.com" || strings.HasSuffix(host, ".reddit.com")
	if !isRedditHost {
		return false
	}
	for _, seg := range strings.Split(path, "/") {
		if seg != "" {
			return seg == "r"
		}
	}
	return false
}

// RefineSourceType refines the source type once a feed has been parsed (audio
// enclosures → podcast; Mastodon's /@user.rss; self-healing Reddit).
func RefineSourceType(initial models.SourceType, parsed *ParsedFeed, feedURL string) models.SourceType {
	if initial != models.SourceRSS {
		return initial
	}
	for _, a := range parsed.Articles {
		for _, e := range a.Enclosures {
			if e.MimeType != nil && strings.HasPrefix(*e.MimeType, "audio") {
				return models.SourcePodcast
			}
		}
	}
	if strings.Contains(feedURL, "/@") && strings.HasSuffix(feedURL, ".rss") {
		return models.SourceMastodon
	}
	if DetectSourceType(feedURL) == models.SourceReddit {
		return models.SourceReddit
	}
	return models.SourceRSS
}

// DiscoverFeeds finds <link rel="alternate"> feed URLs in a web page's HTML.
func DiscoverFeeds(htmlStr, pageURL string) []string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlStr))
	if err != nil {
		return nil
	}
	var base *url.URL
	if b, err := url.Parse(pageURL); err == nil {
		base = b
	}
	var found []string
	seen := map[string]bool{}
	doc.Find(`link[rel~="alternate"]`).Each(func(_ int, s *goquery.Selection) {
		ty := strings.ToLower(s.AttrOr("type", ""))
		if !(strings.Contains(ty, "rss") || strings.Contains(ty, "atom") || strings.Contains(ty, "json")) {
			return
		}
		href, ok := s.Attr("href")
		if !ok {
			return
		}
		resolved := href
		if base != nil {
			if ref, err := url.Parse(href); err == nil {
				resolved = base.ResolveReference(ref).String()
			}
		}
		if !seen[resolved] {
			seen[resolved] = true
			found = append(found, resolved)
		}
	})
	return found
}

// LooksLikeFeed decides whether bytes look like a feed (vs an HTML page) by
// attempting a parse.
func LooksLikeFeed(raw []byte) bool {
	_, err := gofeed.NewParser().Parse(bytes.NewReader(raw))
	return err == nil
}
