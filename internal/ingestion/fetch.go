// Package ingestion is hive's feed-fetching and parsing pipeline: conditional
// HTTP GET with charset detection (fetch.go), RSS/Atom/JSON parsing and source
// classification (parse.go), multi-source URL normalization (sources.go), feed
// discovery (discovery.go), the background refresh scheduler (scheduler.go) and
// IMAP newsletter polling (newsletter.go). Ported from papr's
// src-tauri/src/ingestion/*.
package ingestion

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"

	"hive/internal/apperr"
)

// UserAgent is sent with every outbound request. Many sites (and CDNs like
// Cloudflare) outright reject requests that look like a bot — a polite
// "Hive/0.1" identifier got 403'd against any Cloudflare-fronted blog or
// Medium-style host. Pretending to be a current Chrome on macOS yields the
// same content a browser would, at the cost of looking less honest. The
// trade-off is deliberate: full-text extraction is the user actively asking
// "give me this article", not an unattended crawl.
const UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// AcceptLanguage matches what a real browser sends. Some sites 403 a request
// with no Accept-Language at all, so a reasonable default goes through the
// shared transport.
const AcceptLanguage = "en-US,en;q=0.9"

// maxBodyBytes caps a fetched body. Feeds and article pages are text — a few
// hundred KB at most — so 16 MiB is generous while stopping a hostile or
// misconfigured server from exhausting memory.
const maxBodyBytes = 16 * 1024 * 1024

// errorBodyPeekBytes bounds how much of an error response body we sniff to
// classify the failure (Cloudflare challenge page, plain 403, etc.). Just the
// first chunk is enough to spot the giveaway markers.
const errorBodyPeekBytes = 16 * 1024

// uaTransport adds the hive User-Agent + a default Accept-Language to every
// request that lacks one. Accept-Encoding is intentionally untouched so
// net/http handles gzip transparently.
type uaTransport struct{ base http.RoundTripper }

func (t *uaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", UserAgent)
	}
	if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", AcceptLanguage)
	}
	return t.base.RoundTrip(req)
}

// BuildClient builds an HTTP client (connection pooling, gzip, redirects).
// timeoutSecs bounds the whole request (clamped to [5,300]). proxy is "system"
// (honour HTTP(S)_PROXY env), "none" (bypass), or an explicit proxy URL.
func BuildClient(timeoutSecs int64, proxy string) *http.Client {
	if timeoutSecs < 5 {
		timeoutSecs = 5
	} else if timeoutSecs > 300 {
		timeoutSecs = 300
	}
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          50,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	switch proxy {
	case "system", "":
		tr.Proxy = http.ProxyFromEnvironment
	case "none":
		tr.Proxy = nil
	default:
		if u, err := url.Parse(proxy); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{
		Timeout:   time.Duration(timeoutSecs) * time.Second,
		Transport: &uaTransport{base: tr},
	}
}

// Fetched is the result of a conditional GET against a feed URL.
type Fetched struct {
	// NotModified is true when the server returned 304 — the stored copy is
	// still current and Bytes is empty.
	NotModified  bool
	Bytes        []byte
	ETag         *string
	LastModified *string
}

// classifyHTTPError builds a coded error for a non-2xx HTTP response. The
// status code alone isn't enough: a 403 from a normal server and a 403 from
// Cloudflare's "Just a moment…" challenge are very different problems with
// different remediations, so we peek at the body and headers to classify.
//
// Codes (each is a flat i18n key under `error.*`, matching existing style
// like `noAiKey` — vue-i18n treats `.` as a nested-key separator, so dots
// can't appear in the code):
//   - networkCloudflare — Cloudflare bot challenge page
//   - networkForbidden  — 401 / 403
//   - networkNotFound   — 404 / 410
//   - networkRateLimit  — 429
//   - networkServerError — 5xx
//   - network            — anything else (catch-all)
//
// Detail is "HTTP <code> at <host>" so the frontend's errorText() can pull
// {status} and {host} out for the i18n template.
func classifyHTTPError(resp *http.Response, requestURL string) *apperr.AppError {
	host := ""
	if u, err := url.Parse(requestURL); err == nil {
		host = u.Host
	}
	if host == "" && resp.Request != nil && resp.Request.URL != nil {
		host = resp.Request.URL.Host
	}
	if host == "" {
		host = requestURL
	}

	code := resp.StatusCode
	bucket := "network"
	switch {
	case code == 401 || code == 403:
		bucket = "networkForbidden"
	case code == 404 || code == 410:
		bucket = "networkNotFound"
	case code == 429:
		bucket = "networkRateLimit"
	case code >= 500:
		bucket = "networkServerError"
	}

	// Cloudflare classification overrides 403/503 — the underlying status code
	// is misleading on a challenge page, since the *server* is fine and the
	// *content* is reachable from a real browser.
	if isCloudflareChallenge(resp) {
		bucket = "networkCloudflare"
	}

	return apperr.Codef(bucket, "HTTP %d at %s", code, host)
}

// isCloudflareChallenge looks for the giveaway signals that the response is
// Cloudflare's bot-detection / managed-challenge page rather than the real
// origin. The header check is authoritative; the body sniff catches older
// challenges that didn't set cf-mitigated.
func isCloudflareChallenge(resp *http.Response) bool {
	if resp.Header.Get("CF-Mitigated") != "" {
		return true
	}
	server := strings.ToLower(resp.Header.Get("Server"))
	if !strings.Contains(server, "cloudflare") {
		return false
	}
	// Only sniff text-ish bodies. A binary file from a Cloudflare-fronted CDN
	// that happens to 403 isn't a challenge page.
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if ct != "" && !strings.Contains(ct, "text/html") && !strings.Contains(ct, "text/plain") {
		return false
	}
	peek, err := io.ReadAll(io.LimitReader(resp.Body, errorBodyPeekBytes))
	if err != nil {
		return false
	}
	// Replace the body so any later code (we don't read it today, but a future
	// caller might) sees the bytes we consumed. Cheap and harmless.
	resp.Body = io.NopCloser(strings.NewReader(string(peek)))
	low := strings.ToLower(string(peek))
	return strings.Contains(low, "just a moment") ||
		strings.Contains(low, "cf-chl-") ||
		strings.Contains(low, "challenge-platform") ||
		strings.Contains(low, "cf-browser-verification")
}

// readCapped reads a response body, aborting if it exceeds maxBodyBytes.
func readCapped(resp *http.Response) ([]byte, error) {
	if resp.ContentLength > maxBodyBytes {
		return nil, apperr.Code("responseTooLarge")
	}
	buf, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, apperr.Wrap("network", err)
	}
	if len(buf) > maxBodyBytes {
		return nil, apperr.Code("responseTooLarge")
	}
	return buf, nil
}

// ConditionalGet performs a conditional GET, sending If-None-Match /
// If-Modified-Since when revalidators are available.
func ConditionalGet(ctx context.Context, client *http.Client, rawURL string, etag, lastModified *string) (Fetched, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Fetched{}, apperr.Wrap("network", err)
	}
	if etag != nil {
		req.Header.Set("If-None-Match", *etag)
	}
	if lastModified != nil {
		req.Header.Set("If-Modified-Since", *lastModified)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Fetched{}, apperr.Wrap("network", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return Fetched{NotModified: true}, nil
	}
	if resp.StatusCode >= 400 {
		return Fetched{}, classifyHTTPError(resp, rawURL)
	}
	bytes, err := readCapped(resp)
	if err != nil {
		return Fetched{}, err
	}
	return Fetched{
		Bytes:        bytes,
		ETag:         headerPtr(resp, "ETag"),
		LastModified: headerPtr(resp, "Last-Modified"),
	}, nil
}

// headerPtr returns a pointer to a response header value, or nil if absent.
func headerPtr(resp *http.Response, name string) *string {
	if v := resp.Header.Get(name); v != "" {
		return &v
	}
	return nil
}

// Get is a plain GET returning (body, content-type, final URL) — used for feed
// auto-discovery and full-text article extraction.
func Get(ctx context.Context, client *http.Client, rawURL string) ([]byte, *string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, "", apperr.Wrap("network", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, "", apperr.Wrap("network", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, nil, "", classifyHTTPError(resp, rawURL)
	}
	finalURL := resp.Request.URL.String()
	ct := headerPtr(resp, "Content-Type")
	bytes, err := readCapped(resp)
	if err != nil {
		return nil, nil, "", err
	}
	return bytes, ct, finalURL, nil
}

// charsetFromContentType pulls the charset parameter out of a Content-Type
// header value, e.g. "text/html; charset=Shift_JIS" → "shift_jis".
func charsetFromContentType(contentType string) string {
	for _, part := range strings.Split(contentType, ";") {
		k, v, ok := strings.Cut(part, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "charset") {
			v = strings.TrimSpace(v)
			v = strings.Trim(v, `"`)
			v = strings.TrimSpace(v)
			if v != "" {
				return strings.ToLower(v)
			}
		}
	}
	return ""
}

// parseCharsetValue reads a charset label beginning at tail — the text right
// after a "charset" keyword — skipping an optional '=' and surrounding quotes.
func parseCharsetValue(tail string) string {
	tail = strings.TrimSpace(tail)
	tail = strings.TrimPrefix(tail, "=")
	tail = strings.TrimSpace(tail)
	tail = strings.TrimLeft(tail, `"'`)
	var b strings.Builder
	for _, c := range tail {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteRune(c)
		} else {
			break
		}
	}
	return b.String()
}

// charsetFromHTML sniffs a charset from an HTML document's own <meta>
// declaration. Only the first 2 KiB are scanned, and the keyword is honoured
// only when it sits inside a <meta …> tag (so a CSS @charset rule or an early
// <title> can't derail it).
func charsetFromHTML(bytes []byte) string {
	head := bytes
	if len(head) > 2048 {
		head = head[:2048]
	}
	text := strings.ToLower(string(head))
	search := 0
	for {
		rel := strings.Index(text[search:], "<meta")
		if rel < 0 {
			return ""
		}
		tagStart := search + rel
		tagEnd := len(text)
		if i := strings.Index(text[tagStart:], ">"); i >= 0 {
			tagEnd = tagStart + i
		}
		tag := text[tagStart:tagEnd]
		if i := strings.Index(tag, "charset"); i >= 0 {
			if v := parseCharsetValue(tag[i+len("charset"):]); v != "" {
				return v
			}
		}
		search = tagEnd
	}
}

// DecodeHTML decodes fetched HTML/text bytes into a string using the page's
// declared encoding rather than assuming UTF-8. Resolution order: the HTTP
// Content-Type charset, then the document's <meta charset>, then UTF-8.
func DecodeHTML(bytes []byte, contentType *string) string {
	label := ""
	if contentType != nil {
		label = charsetFromContentType(*contentType)
	}
	if label == "" {
		label = charsetFromHTML(bytes)
	}
	if label == "" || strings.EqualFold(label, "utf-8") {
		return string(bytes)
	}
	enc, err := htmlindex.Get(label)
	if err != nil || enc == nil {
		return string(bytes)
	}
	out, _, err := transform.Bytes(enc.NewDecoder(), bytes)
	if err != nil {
		return string(bytes)
	}
	return string(out)
}
