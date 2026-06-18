// Package sources implements multi-source URL normalization (feature F5).
//
// Users paste many kinds of links — YouTube channels, subreddits, Mastodon
// profiles — that are not themselves subscribable feed documents. This package
// recognizes those patterns and rewrites them to the real feed URL, reporting
// the resulting models.SourceType.
//
// Everything here is a *pure* function so it can be unit-tested without the
// network. The one case that genuinely needs a page fetch — resolving a
// YouTube `@handle` / `/c/` / `/user/` vanity URL to a channel id — is split
// into a pure HTML-extraction function (ExtractChannelID); the network fetch
// itself stays in the caller (add_feed).
package sources

import (
	"fmt"
	"net/url"
	"strings"

	"hive/internal/models"
)

// NormalizedKind is the discriminant for Normalized — which branch of the Rust
// `Normalized` enum a result represents.
type NormalizedKind int

const (
	// KindFeed: the URL was recognized and rewritten to a directly-subscribable
	// feed. The result's URL and SourceType are set.
	KindFeed NormalizedKind = iota
	// KindNeedsYoutubeResolution: the URL is a YouTube vanity link (`@handle`,
	// `/c/Name`, `/user/Name`, `youtu.be/...`) whose channel id can only be
	// learned by fetching the page HTML. The caller fetches PageURL, then calls
	// ExtractChannelID + YoutubeFeedURL.
	KindNeedsYoutubeResolution
	// KindUntouched: not a recognized special source — hand back to the normal
	// feed / auto-discovery flow untouched.
	KindUntouched
)

// Normalized is the outcome of running a pasted string through NormalizeSource.
//
// It mirrors the Rust `Normalized` enum: for KindFeed the URL and SourceType
// fields are set (the real feed URL to fetch and store, and the classified
// source type); for KindNeedsYoutubeResolution the PageURL field is set (the
// page to fetch in order to extract the channel id); KindUntouched carries no
// payload.
type Normalized struct {
	Kind       NormalizedKind
	URL        string
	SourceType models.SourceType
	PageURL    string
}

// NormalizeSource inspects a user-pasted URL/string and, if it matches a known
// source pattern, rewrites it to the subscribable feed URL. Non-matching input
// is returned as KindUntouched so the caller's existing discovery flow handles
// it.
func NormalizeSource(input string) Normalized {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return Normalized{Kind: KindUntouched}
	}

	// Tolerate a missing scheme (`youtube.com/@x`) — prepend https:// so the
	// URL parser can do host/path matching.
	withScheme := normalizeQueryURL(trimmed)
	u, err := url.Parse(withScheme)
	if err != nil {
		return Normalized{Kind: KindUntouched}
	}
	host := strings.ToLower(u.Hostname())
	path := u.EscapedPath()

	if strings.HasSuffix(host, "youtube.com") || host == "youtu.be" || strings.HasSuffix(host, ".youtu.be") {
		return normalizeYoutube(host, path, u)
	}
	if host == "reddit.com" || strings.HasSuffix(host, ".reddit.com") {
		if feed := normalizeReddit(path); feed != nil {
			return Normalized{Kind: KindFeed, URL: *feed, SourceType: models.SourceReddit}
		}
	}
	if feed := normalizeMastodon(withScheme, path); feed != nil {
		return Normalized{Kind: KindFeed, URL: *feed, SourceType: models.SourceMastodon}
	}

	return Normalized{Kind: KindUntouched}
}

// YoutubeFeedURL builds the canonical YouTube channel feed URL for a `UC…`
// channel id.
func YoutubeFeedURL(channelID string) string {
	return fmt.Sprintf("https://www.youtube.com/feeds/videos.xml?channel_id=%s", channelID)
}

// DefaultRSSHubInstance is the public RSSHub instance used when the user hasn't
// configured their own.
const DefaultRSSHubInstance = "https://rsshub.app"

// ExpandRSSHub expands an `rsshub://route` short link into a full feed URL on
// instance.
//
// RSSHub documents routes in a scheme-style shorthand —
// `rsshub://github/issue/DIYgod/RSSHub` — that maps onto a normal HTTP feed at
// `<instance>/github/issue/DIYgod/RSSHub`. Once expanded the result is an
// ordinary RSS URL, so the caller can run it through the regular fetch/parse
// flow with no further special-casing.
//
// Returns nil for anything that isn't an `rsshub://` link (so callers fall
// through to the normal flow) or for an empty route. Pure, so it is unit tested
// without the network; the instance setting lookup lives in the caller,
// mirroring how the YouTube network fetch stays out of this package.
func ExpandRSSHub(input, instance string) *string {
	trimmed := strings.TrimSpace(input)
	// Schemes are case-insensitive; users paste `rsshub://` but tolerate any
	// casing. "rsshub://" is pure ASCII, so byte-slicing past it is safe.
	if len(trimmed) < 9 || !strings.EqualFold(trimmed[:9], "rsshub://") {
		return nil
	}
	route := strings.TrimLeft(trimmed[9:], "/")
	if route == "" {
		return nil
	}
	base := strings.TrimRight(strings.TrimSpace(instance), "/")
	result := fmt.Sprintf("%s/%s", base, route)
	return &result
}

// isIDChar reports whether c is one of the URL-safe id characters YouTube uses
// in channel/playlist ids: ASCII alphanumerics plus `_` and `-`.
func isIDChar(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-'
}

// pathSegments splits a URL path into its non-empty segments.
func pathSegments(path string) []string {
	out := make([]string, 0)
	for _, s := range strings.Split(path, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// isChannelID reports whether id looks like a YouTube channel id (`UC` + 22
// chars).
func isChannelID(id string) bool {
	if len(id) != 24 || !strings.HasPrefix(id, "UC") {
		return false
	}
	for _, c := range id {
		if !isIDChar(c) {
			return false
		}
	}
	return true
}

// isPlaylistID reports whether id looks like a YouTube playlist id (`PL`, `UU`,
// `LL`, `FL`, …).
func isPlaylistID(id string) bool {
	if len(id) < 13 {
		return false
	}
	for _, c := range id {
		if !isIDChar(c) {
			return false
		}
	}
	return strings.HasPrefix(id, "PL") ||
		strings.HasPrefix(id, "UU") ||
		strings.HasPrefix(id, "LL") ||
		strings.HasPrefix(id, "FL") ||
		strings.HasPrefix(id, "OL")
}

func normalizeYoutube(host, path string, u *url.URL) Normalized {
	// Already a feed document — leave it for the normal flow.
	if strings.Contains(path, "/feeds/videos.xml") {
		return Normalized{Kind: KindUntouched}
	}

	// A `playlist?list=PL…` URL has a playlist feed endpoint.
	if strings.HasPrefix(path, "/playlist") {
		if list := u.Query().Get("list"); list != "" {
			if isPlaylistID(list) {
				return Normalized{
					Kind:       KindFeed,
					URL:        fmt.Sprintf("https://www.youtube.com/feeds/videos.xml?playlist_id=%s", list),
					SourceType: models.SourceYoutube,
				}
			}
		}
	}

	segments := pathSegments(path)

	// `youtube.com/channel/UC…` — the channel id is right there in the path.
	if len(segments) >= 2 && segments[0] == "channel" && isChannelID(segments[1]) {
		return Normalized{
			Kind:       KindFeed,
			URL:        YoutubeFeedURL(segments[1]),
			SourceType: models.SourceYoutube,
		}
	}

	// Vanity URLs we can recognize but not resolve without fetching the page:
	//   /@handle      /c/Name      /user/Name
	//   youtu.be/<videoId or handle>
	isVanity := false
	if len(segments) > 0 {
		s := segments[0]
		isVanity = strings.HasPrefix(s, "@") || s == "c" || s == "user"
	}
	isShortHost := host == "youtu.be" || strings.HasSuffix(host, ".youtu.be")
	if isVanity || (isShortHost && len(segments) > 0) {
		return Normalized{
			Kind:    KindNeedsYoutubeResolution,
			PageURL: u.String(),
		}
	}

	return Normalized{Kind: KindUntouched}
}

// normalizeReddit extracts `r/SUBREDDIT` from a Reddit path and builds its
// `.rss` feed URL. Handles `/r/SUB`, `/r/SUB/`, `/r/SUB/top`, `/r/SUB/.rss`,
// etc. Returns nil when the path is not a subreddit feed.
func normalizeReddit(path string) *string {
	segments := pathSegments(path)
	if len(segments) < 2 || segments[0] != "r" {
		return nil
	}
	sub := segments[1]
	// A subreddit name is alphanumeric + underscore; reject anything else so a
	// post permalink (`/r/SUB/comments/...`) does not become a bogus feed.
	if sub == "" || !isAlnumUnderscore(sub) {
		return nil
	}
	if len(segments) < 3 {
		// Bare subreddit (`/r/SUB`, `/r/SUB/`) → its main feed.
		return strptr(fmt.Sprintf("https://www.reddit.com/r/%s/.rss", sub))
	}
	listing := segments[2]
	switch listing {
	// A sub-listing (`/r/SUB/top`) → keep that listing's feed.
	case "hot", "new", "top", "rising":
		return strptr(fmt.Sprintf("https://www.reddit.com/r/%s/%s/.rss", sub, listing))
	default:
		// Already a `.rss` URL, or a `.rss` listing — leave for the caller.
		// Anything else (`/comments/...`, `/wiki/...`) is not a subreddit feed —
		// a post permalink must not become a bogus feed.
		return nil
	}
}

// normalizeMastodon recognizes a Mastodon profile URL (`https://instance/@user`)
// and appends the `.rss` suffix Mastodon exposes for every account's public
// timeline. Returns nil when the path is not a single `@user` profile segment.
func normalizeMastodon(fullURL, path string) *string {
	segments := pathSegments(path)
	// A Mastodon profile is exactly one path segment, starting with `@`.
	if len(segments) != 1 {
		return nil
	}
	handle := segments[0]
	if !strings.HasPrefix(handle, "@") || len(handle) < 2 {
		return nil
	}
	// Already a `.rss` URL — nothing to do.
	if strings.HasSuffix(handle, ".rss") {
		return nil
	}
	// Reuse the input's scheme + host; just swap the path for `/@user.rss`.
	base, err := url.Parse(fullURL)
	if err != nil {
		return nil
	}
	host := base.Hostname()
	if host == "" {
		return nil
	}
	scheme := base.Scheme
	return strptr(fmt.Sprintf("%s://%s/%s.rss", scheme, host, handle))
}

// ExtractChannelID pulls a YouTube channel id (`UC…`) out of a channel page's
// HTML.
//
// Pure and network-free: the caller (add_feed) fetches the page, this parses
// it.
//
// Source order matters for *correctness*, not just coverage. A channel page's
// HTML embeds many `channelId` values — recommended channels, the uploader of
// every thumbnail in the sidebar, comment authors — and they often appear
// *before* the page's own id. Picking the first `"channelId"` blindly would
// subscribe the user to the wrong channel. So the authoritative,
// page-owner-specific signals are tried first:
//
//  1. `"externalId"` / `"externalChannelId"` — YouTube's own metadata keys for
//     *this* channel; never used for sidebar/recommended entries.
//  2. `<link rel="canonical" href=".../channel/UC…">` — the `<head>` canonical
//     URL always points at the page's own channel.
//  3. The `og:url` (or any) bare `/channel/UC…` substring.
//  4. Only as a last resort, the ambiguous plain `"channelId"` JSON key.
//
// Returns nil if no id can be found.
func ExtractChannelID(html string) *string {
	// 1. Owner-specific metadata keys — unambiguous, so tried first.
	for _, key := range []string{`"externalId":"`, `"externalChannelId":"`} {
		if id := findAfter(html, key, '"'); id != nil {
			if isChannelID(*id) {
				return id
			}
		}
	}

	// 2. <link rel="canonical" href="https://www.youtube.com/channel/UC...">
	if id := canonicalChannelID(html); id != nil {
		return id
	}

	// 3. A bare `/channel/UC...` substring anywhere (e.g. og:url meta tag).
	if id := channelIDAfterMarker(html); id != nil {
		return id
	}

	// 4. Last resort: the plain `"channelId"` JSON key. Ambiguous (also used
	//    for recommended channels), so only consulted when nothing above hit.
	if id := findAfter(html, `"channelId":"`, '"'); id != nil {
		if isChannelID(*id) {
			return id
		}
	}

	return nil
}

// channelIDAfterMarker pulls a valid channel id out of the text following the
// first `/channel/` marker in text — used for both bare `/channel/UC…`
// substrings and the `href` of a `<link rel="canonical">` tag.
func channelIDAfterMarker(text string) *string {
	idx := strings.Index(text, "/channel/")
	if idx < 0 {
		return nil
	}
	rest := text[idx+len("/channel/"):]
	var b strings.Builder
	for _, c := range rest {
		if !isIDChar(c) {
			break
		}
		b.WriteRune(c)
	}
	id := b.String()
	if isChannelID(id) {
		return &id
	}
	return nil
}

// findAfter returns the substring of html after the first occurrence of marker,
// up to (but not including) the next end byte. Returns nil when marker or the
// terminating end byte is absent.
func findAfter(html, marker string, end byte) *string {
	idx := strings.Index(html, marker)
	if idx < 0 {
		return nil
	}
	start := idx + len(marker)
	tail := html[start:]
	stop := strings.IndexByte(tail, end)
	if stop < 0 {
		return nil
	}
	out := tail[:stop]
	return &out
}

// canonicalChannelID finds a `<link rel="canonical">` tag and pulls a
// `/channel/UC…` id from its `href`. Order-insensitive about the `rel` / `href`
// attribute positions.
//
// A `rel="canonical"` substring that is not wrapped in a real `<…>` tag — it can
// appear as text inside an embedded `<script>` JSON blob, or lead a malformed
// document — must be skipped rather than aborting the scan: a genuine
// `<link rel="canonical">` may still follow later in the document.
func canonicalChannelID(html string) *string {
	lower := strings.ToLower(html)
	search := 0
	for {
		rel := strings.Index(lower[search:], `rel="canonical"`)
		if rel < 0 {
			break
		}
		abs := search + rel
		// The enclosing <link …> tag: scan forward to '>' and back to '<'.
		relTagEnd := strings.IndexByte(lower[abs:], '>')
		if relTagEnd < 0 {
			// No '>' anywhere after this point — nothing more to scan.
			break
		}
		tagEnd := relTagEnd + abs
		tagStart := strings.LastIndexByte(lower[:abs], '<')
		if tagStart < 0 {
			// No opening '<' before this occurrence — not a real tag; skip it
			// and keep scanning for a later, well-formed canonical link.
			search = tagEnd
			continue
		}
		tag := html[tagStart:tagEnd]
		if id := channelIDAfterMarker(tag); id != nil {
			return id
		}
		search = tagEnd
	}
	return nil
}

// normalizeQueryURL normalizes a URL-ish query into a fetchable absolute URL by
// adding a scheme when the user typed a bare domain. Ported from the Rust
// ingestion::discovery::normalize_query_url that sources.rs depends on.
func normalizeQueryURL(query string) string {
	q := strings.TrimSpace(query)
	if strings.Contains(q, "://") {
		return q
	}
	return "https://" + q
}

// isAlnumUnderscore reports whether every byte of s is ASCII alphanumeric or
// `_`.
func isAlnumUnderscore(s string) bool {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if !((b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_') {
			return false
		}
	}
	return true
}

func strptr(s string) *string { return &s }
