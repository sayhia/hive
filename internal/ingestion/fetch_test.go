package ingestion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hive/internal/apperr"
)

func TestCharsetFromContentType(t *testing.T) {
	cases := []struct{ in, want string }{
		{"text/html; charset=UTF-8", "utf-8"},
		{`text/html;charset="Shift_JIS"`, "shift_jis"},
		{"text/html ; CHARSET = gbk ", "gbk"},
		{"text/html", ""},
		{"text/html; charset=", ""},
	}
	for _, c := range cases {
		if got := charsetFromContentType(c.in); got != c.want {
			t.Errorf("charsetFromContentType(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCharsetFromHTML(t *testing.T) {
	if got := charsetFromHTML([]byte(`<html><head><meta charset="euc-kr">`)); got != "euc-kr" {
		t.Errorf("meta charset = %q, want euc-kr", got)
	}
	if got := charsetFromHTML([]byte(`<meta http-equiv="Content-Type" content="text/html; charset=Shift_JIS">`)); got != "shift_jis" {
		t.Errorf("http-equiv charset = %q, want shift_jis", got)
	}
	if got := charsetFromHTML([]byte(`<html><head><title>x</title>`)); got != "" {
		t.Errorf("no charset expected, got %q", got)
	}
	// A CSS @charset in an inline <style> must not be picked over the real meta.
	html := []byte(`<head><style>@charset "UTF-8";</style><meta charset="Shift_JIS"></head>`)
	if got := charsetFromHTML(html); got != "shift_jis" {
		t.Errorf("CSS @charset derailed sniff = %q, want shift_jis", got)
	}
	// The literal word in an early <title> must not be mistaken for a decl.
	html = []byte(`<head><title>How to set the charset</title><meta charset="euc-kr"></head>`)
	if got := charsetFromHTML(html); got != "euc-kr" {
		t.Errorf("title-charset derailed sniff = %q, want euc-kr", got)
	}
	if got := charsetFromHTML([]byte(`<head><title>charset basics</title></head>`)); got != "" {
		t.Errorf("charset only outside meta should yield empty, got %q", got)
	}
}

func TestDecodeHTML(t *testing.T) {
	// ISO-8859-1 byte 0xE9 is 'é'.
	if got := DecodeHTML([]byte("caf\xe9"), strptr("text/html; charset=iso-8859-1")); got != "café" {
		t.Errorf("iso-8859-1 decode = %q, want café", got)
	}
	// No header charset — the <meta> declaration is used. windows-1252 0x80 = €.
	html := append([]byte("<meta charset=windows-1252><body>"), 0x80)
	if got := DecodeHTML(html, nil); !contains(got, "€") {
		t.Errorf("windows-1252 meta decode missing €: %q", got)
	}
	// Valid UTF-8 round-trips untouched.
	if got := DecodeHTML([]byte("héllo"), strptr("text/html")); got != "héllo" {
		t.Errorf("utf-8 passthrough = %q", got)
	}
	// A bogus charset label must not panic — it falls back to UTF-8.
	if got := DecodeHTML([]byte("plain"), strptr("text/html; charset=not-a-charset")); got != "plain" {
		t.Errorf("unknown charset fallback = %q, want plain", got)
	}
}

// testClient builds an HTTP client that goes through the same uaTransport
// production uses, so UA / Accept-Language assertions exercise real code.
func testClient() *http.Client {
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &uaTransport{base: http.DefaultTransport},
	}
}

func TestUserAgentLooksLikeABrowser(t *testing.T) {
	// Many sites 403 anything that names itself a bot. A regression here would
	// silently break full-text extraction against Cloudflare-fronted hosts.
	if !strings.HasPrefix(UserAgent, "Mozilla/5.0") || !strings.Contains(UserAgent, "Chrome/") {
		t.Errorf("UserAgent should look like a real browser, got %q", UserAgent)
	}
}

func TestUaTransportSetsHeadersWhenMissing(t *testing.T) {
	var seenUA, seenLang string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenUA = r.Header.Get("User-Agent")
		seenLang = r.Header.Get("Accept-Language")
	}))
	defer srv.Close()
	if _, _, _, err := Get(context.Background(), testClient(), srv.URL); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if seenUA != UserAgent {
		t.Errorf("UA = %q, want %q", seenUA, UserAgent)
	}
	if seenLang != AcceptLanguage {
		t.Errorf("Accept-Language = %q, want %q", seenLang, AcceptLanguage)
	}
}

func TestClassifyHTTPErrorBuckets(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		header http.Header
		want   string
	}{
		{"plain 403", 403, "go away", nil, "networkForbidden"},
		{"401", 401, "", nil, "networkForbidden"},
		{"404", 404, "", nil, "networkNotFound"},
		{"410", 410, "", nil, "networkNotFound"},
		{"429", 429, "", nil, "networkRateLimit"},
		{"500", 500, "", nil, "networkServerError"},
		{"503", 503, "", nil, "networkServerError"},
		{"other 4xx", 418, "", nil, "network"},
		{
			"cloudflare via header",
			403,
			"go away",
			http.Header{"Cf-Mitigated": []string{"challenge"}},
			"networkCloudflare",
		},
		{
			"cloudflare via Just a moment HTML",
			403,
			`<!DOCTYPE html><html><head><title>Just a moment...</title></head><body>cf-chl-</body></html>`,
			http.Header{"Server": []string{"cloudflare"}, "Content-Type": []string{"text/html"}},
			"networkCloudflare",
		},
		{
			// Cloudflare-fronted CDN serving a binary file that 403s isn't a
			// challenge page. Don't false-positive on those.
			"cloudflare 403 on binary content",
			403,
			"\x00\x01garbage",
			http.Header{"Server": []string{"cloudflare"}, "Content-Type": []string{"application/octet-stream"}},
			"networkForbidden",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, vs := range c.header {
					for _, v := range vs {
						w.Header().Add(k, v)
					}
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			_, _, _, err := Get(context.Background(), testClient(), srv.URL)
			if err == nil {
				t.Fatalf("expected error for status %d", c.status)
			}
			ae, ok := err.(*apperr.AppError)
			if !ok {
				t.Fatalf("error is not *AppError: %T %v", err, err)
			}
			if ae.Code != c.want {
				t.Errorf("code = %q, want %q (detail=%q)", ae.Code, c.want, ae.Detail)
			}
			if !strings.HasPrefix(ae.Detail, "HTTP ") {
				t.Errorf("detail %q should start with 'HTTP '", ae.Detail)
			}
		})
	}
}

func strptr(s string) *string { return &s }
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
