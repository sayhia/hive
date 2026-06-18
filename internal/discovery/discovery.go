// Package discovery implements feed discovery (feature F6).
//
// Two network-free building blocks:
//
//   - SearchDirectory — case-insensitive matching against the curated
//     directory of well-known feeds (embedded fallback + dynamically
//     refreshed remote copy; see store.go).
//   - ParseDeepLink — parsing a hive://subscribe?url=<encoded> deep link
//     handed over by the browser extension.
//
// Both stay side-effect-free so they can be unit-tested without the
// filesystem or the network (the store's refresh machinery is opt-in and
// lives in store.go). The live page-scrape half of discovery (fetching a URL
// and running the parser's feed discovery) lives in the discovery service
// command, which orchestrates these pieces.
//
// Ported from papr's src-tauri/src/ingestion/discovery.rs. The custom URL
// scheme changed from papr:// to hive:// for this app.
package discovery

import (
	_ "embed"
	"net/url"
	"strings"
)

// directoryJSON is the curated directory, embedded into the binary at compile
// time so no Wails resource configuration is required.
//
//go:embed feed-directory.json
var directoryJSON []byte

// directoryEntry is one entry in the curated feed directory.
type directoryEntry struct {
	// Title is the display name of the feed.
	Title string `json:"title"`
	// FeedURL is the subscribable feed URL.
	FeedURL string `json:"feedUrl"`
	// SiteURL is the human-facing website the feed belongs to.
	SiteURL string `json:"siteUrl"`
	// Category is a broad category — used to group results in the UI. Authored
	// in the entry's own language, so a directory slice reads natively.
	Category string `json:"category"`
	// Description is a one-line description shown under the title.
	Description string `json:"description"`
	// Lang is the primary language of the feed's content ("en", "zh", "ja").
	// Explore recommends the slice matching the user's chosen UI language.
	Lang string `json:"lang"`
}

// Result is a single discovery result, returned to the frontend. Mirrors the
// frontend's DiscoveryResult (types.ts).
type Result struct {
	// Title is the display name of the feed.
	Title string `json:"title"`
	// FeedURL is the subscribable feed URL — passed straight to add_feed.
	FeedURL string `json:"feedUrl"`
	// SiteURL is the website the feed belongs to, when known.
	SiteURL *string `json:"siteUrl"`
	// Category is the category for directory entries; nil for live page scrapes.
	Category *string `json:"category"`
	// Description is a short description, when known.
	Description *string `json:"description"`
	// Lang is the primary language subtag of a directory entry ("zh", "en");
	// empty for live page scrapes. The hub's language tabs segment on it.
	Lang string `json:"lang"`
	// FromDirectory is true when the result came from the curated directory,
	// false when it was scraped live from a page the user pasted.
	FromDirectory bool `json:"fromDirectory"`
}

// resultFromEntry builds a result from a curated directoryEntry.
func resultFromEntry(e directoryEntry) Result {
	siteURL := e.SiteURL
	category := e.Category
	description := e.Description
	return Result{
		Title:         e.Title,
		FeedURL:       e.FeedURL,
		SiteURL:       &siteURL,
		Category:      &category,
		Description:   &description,
		Lang:          primaryLang(e.Lang),
		FromDirectory: true,
	}
}

// ResultFromScrape builds a result from a feed URL discovered live on a
// scraped page.
func ResultFromScrape(feedURL string, title *string) Result {
	display := feedURL
	if title != nil {
		display = *title
	}
	return Result{
		Title:         display,
		FeedURL:       feedURL,
		SiteURL:       nil,
		Category:      nil,
		Description:   nil,
		FromDirectory: false,
	}
}

// primaryLang returns the primary subtag of a BCP-47-ish language tag,
// lowercased: "zh-CN" and "ZH" both reduce to "zh". Used to match a UI locale
// against the directory's coarser per-language tags.
func primaryLang(tag string) string {
	primary := tag
	if i := strings.IndexAny(tag, "-_"); i >= 0 {
		primary = tag[:i]
	}
	return strings.ToLower(primary)
}

// SearchDirectory performs a case-insensitive search of the curated directory.
//
// lang is a display preference, not a filter: entries whose primary language
// subtag matches the UI locale sort first (a "zh-CN" locale leads with the
// "zh" slice) and the rest follow, each group in the directory's authored
// order. The whole directory stays reachable — a Chinese reader still finds
// and subscribes to "Hacker News". (The original papr-inherited per-language
// filter made cross-language feeds unfindable and capped the gallery at the
// locale's slice, which read as "the directory is mysteriously small".)
//
// An empty query returns every entry (the UI shows it as a browsable
// gallery); otherwise an entry matches when the query is a substring of its
// title, category, or description.
func SearchDirectory(query, lang string) []Result {
	needle := strings.ToLower(strings.TrimSpace(query))
	want := primaryLang(lang)
	// Non-nil empty slice so callers (and JSON) see [] rather than null.
	preferred := []Result{}
	var rest []Result
	for _, e := range current() {
		if needle != "" &&
			!strings.Contains(strings.ToLower(e.Title), needle) &&
			!strings.Contains(strings.ToLower(e.Category), needle) &&
			!strings.Contains(strings.ToLower(e.Description), needle) {
			continue
		}
		if primaryLang(e.Lang) == want {
			preferred = append(preferred, resultFromEntry(e))
		} else {
			rest = append(rest, resultFromEntry(e))
		}
	}
	return append(preferred, rest...)
}

// LooksLikeURL reports whether the query looks like a URL or bare domain — in
// which case the discovery command should ALSO fetch the page and scrape it
// for feeds.
//
// Heuristics: an explicit scheme, or a dotted token with no whitespace whose
// last label looks like a TLD (example.com, blog.example.co.uk/path).
func LooksLikeURL(query string) bool {
	q := strings.TrimSpace(query)
	if q == "" || strings.ContainsFunc(q, isWhitespace) {
		return false
	}
	if strings.Contains(q, "://") {
		return true
	}
	// Bare domain: strip any path/query, require at least one dot, and a final
	// label of 2+ ascii-alpha chars (a plausible TLD).
	host := q
	if i := strings.IndexAny(q, "/?#"); i >= 0 {
		host = q[:i]
	}
	labels := make([]string, 0)
	for _, s := range strings.Split(host, ".") {
		if s != "" {
			labels = append(labels, s)
		}
	}
	if len(labels) < 2 {
		return false
	}
	tld := labels[len(labels)-1]
	if len(tld) < 2 {
		return false
	}
	for _, c := range tld {
		if !isASCIIAlpha(c) {
			return false
		}
	}
	return true
}

// isWhitespace mirrors Rust's char::is_whitespace for the ASCII/Unicode
// whitespace we care about here.
func isWhitespace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f', '', ' ':
		return true
	}
	return false
}

// isASCIIAlpha reports whether r is an ASCII alphabetic character.
func isASCIIAlpha(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// NormalizeQueryURL normalizes a URL-ish query into a fetchable absolute URL by
// adding a scheme when the user typed a bare domain.
func NormalizeQueryURL(query string) string {
	q := strings.TrimSpace(query)
	if strings.Contains(q, "://") {
		return q
	}
	return "https://" + q
}

// ParseDeepLink parses a hive://subscribe?url=<encoded> deep link and returns
// the decoded target feed URL, or nil for anything that is not a well-formed
// subscribe link — wrong scheme, wrong host/action, a missing or empty url
// parameter, or input that does not parse as a URL at all. URL-encoding in the
// url query value is decoded.
//
// (The Rust original used the papr:// scheme; this app uses hive://.)
func ParseDeepLink(raw string) *string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil
	}
	if parsed.Scheme != "hive" {
		return nil
	}
	// Tauri/the extension delivers custom-scheme links as hive://subscribe?...,
	// so the action lands in the host component. Accept it in the path too
	// (hive:///subscribe) for robustness across platforms.
	action := parsed.Host
	if action == "" {
		action = strings.Trim(parsed.Path, "/")
	}
	if action != "subscribe" {
		return nil
	}
	q := parsed.Query()
	if !q.Has("url") {
		return nil
	}
	target := strings.TrimSpace(q.Get("url"))
	if target == "" {
		return nil
	}
	return &target
}
