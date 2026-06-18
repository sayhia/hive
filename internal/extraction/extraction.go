// Package extraction pulls the main article content out of a noisy web page
// (Readability algorithm via go-shiori/go-readability) for the "read full text"
// and "read later" features. It mirrors papr's src-tauri/src/extraction.rs.
package extraction

import (
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	readability "github.com/go-shiori/go-readability"

	"hive/internal/apperr"
)

// leadImageSelector matches the metadata elements that may carry a page-level
// lead image, mirroring the Rust LEAD_IMAGE_SELECTORS. goquery uses cascadia,
// which does not support comma-separated grouping with embedded whitespace as a
// single Parse, so the selectors are kept as one comma-separated string on one
// logical line.
const leadImageSelector = `meta[property="og:image"], meta[name="og:image"], meta[property="twitter:image"], meta[name="twitter:image"], meta[itemprop="image"], link[rel="image_src"]`

// Extract pulls the main article HTML out of a full web page using the
// Readability algorithm, and extracts a page-level lead image from metadata.
//
// pageURL is parsed for use as the base when resolving relative URLs (both for
// Readability and for the lead image). It returns the cleaned content HTML and,
// if a usable one is present, a lead image URL. data: URIs and unresolvable or
// non-http(s) relative URLs are skipped (leadImage is nil when none is usable).
//
// This mirrors extract_article + lead_image in papr's extraction.rs.
func Extract(htmlStr, pageURL string) (contentHTML string, leadImage *string, err error) {
	// pageURL may be empty or unparseable; Readability tolerates a nil base.
	parsedURL, _ := url.Parse(pageURL)

	article, err := readability.FromReader(strings.NewReader(htmlStr), parsedURL)
	if err != nil {
		return "", nil, apperr.Wrap("extract", err)
	}

	content := article.Content
	if strings.TrimSpace(content) == "" {
		return "", nil, apperr.Code("noExtractableContent")
	}

	return content, leadImageFrom(htmlStr, pageURL), nil
}

// leadImageFrom pulls a page-level lead image from metadata, resolving relative
// URLs against base. Returns nil when no usable image is found. Mirrors the Rust
// lead_image helper.
func leadImageFrom(htmlStr, base string) *string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlStr))
	if err != nil {
		return nil
	}

	var found *string
	doc.Find(leadImageSelector).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		raw, ok := s.Attr("content")
		if !ok {
			raw, ok = s.Attr("href")
		}
		if !ok {
			return true // keep searching
		}
		raw = strings.TrimSpace(raw)
		if resolved := resolveHTTPURL(raw, base); resolved != nil {
			found = resolved
			return false // stop, found_map equivalent
		}
		return true
	})
	return found
}

// resolveHTTPURL resolves raw against base, returning an absolute http(s) URL.
// It rejects empty strings, data: URIs, and any URL whose final scheme is not
// http or https. Mirrors the Rust resolve_http_url helper.
func resolveHTTPURL(raw, base string) *string {
	if raw == "" || strings.HasPrefix(raw, "data:") {
		return nil
	}

	// Try raw as an absolute URL first; fall back to joining against base.
	resolved, ok := absoluteOrJoined(raw, base)
	if !ok {
		return nil
	}
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return nil
	}
	out := resolved.String()
	return &out
}

// absoluteOrJoined parses raw as an absolute URL; if that yields no scheme, it
// resolves raw relative to base. Mirrors Rust's
// Url::parse(raw).or_else(|| Url::parse(base).and_then(|b| b.join(raw))).
func absoluteOrJoined(raw, base string) (*url.URL, bool) {
	if u, err := url.Parse(raw); err == nil && u.IsAbs() {
		return u, true
	}
	b, err := url.Parse(base)
	if err != nil || !b.IsAbs() {
		return nil, false
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return nil, false
	}
	return b.ResolveReference(ref), true
}
