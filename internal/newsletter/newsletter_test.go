package newsletter

import (
	"strings"
	"testing"
	"time"

	"hive/internal/models"
)

// derefOr returns the pointed-to string, or "" when p is nil.
func derefOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// PLAIN is a complete plain-text email.
const plain = "From: Jane Doe <jane@example.com>\r\n" +
	"Subject: Weekly digest\r\n" +
	"Date: Tue, 15 Nov 2022 10:30:00 +0000\r\n" +
	"Message-ID: <abc123@example.com>\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Hello there, this is the body of the newsletter.\r\n"

func TestParsesPlainTextEmail(t *testing.T) {
	p, ok := parseEmail([]byte(plain))
	if !ok {
		t.Fatal("should parse")
	}
	if p.article.Title != "Weekly digest" {
		t.Errorf("title = %q, want %q", p.article.Title, "Weekly digest")
	}
	if p.fromName != "Jane Doe" {
		t.Errorf("fromName = %q, want Jane Doe", p.fromName)
	}
	if p.fromAddr != "jane@example.com" {
		t.Errorf("fromAddr = %q, want jane@example.com", p.fromAddr)
	}
	if derefOr(p.article.Author) != "Jane Doe" {
		t.Errorf("author = %q, want Jane Doe", derefOr(p.article.Author))
	}
	if !strings.Contains(p.article.BodyText, "body of the newsletter") {
		t.Errorf("bodyText missing newsletter text: %q", p.article.BodyText)
	}
	if p.article.GUID != "abc123@example.com" {
		t.Errorf("guid = %q, want abc123@example.com", p.article.GUID)
	}
	if p.article.PublishedAt == nil {
		t.Error("publishedAt should be set")
	}
	// Plain text is wrapped in <pre>.
	if !strings.Contains(derefOr(p.article.ContentHTML), "<pre>") {
		t.Errorf("contentHTML missing <pre>: %q", derefOr(p.article.ContentHTML))
	}
}

func TestParsesMultipartHTMLEmail(t *testing.T) {
	raw := "From: News <news@site.com>\r\n" +
		"Subject: HTML newsletter\r\n" +
		"Date: Wed, 01 Jan 2025 12:00:00 +0000\r\n" +
		"Message-ID: <html1@site.com>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"BOUND\"\r\n" +
		"\r\n" +
		"--BOUND\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"plain fallback\r\n" +
		"--BOUND\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<html><body><h1>Big News</h1><p>An <b>HTML</b> body.</p><script>alert(1)</script></body></html>\r\n" +
		"--BOUND--\r\n"
	p, ok := parseEmail([]byte(raw))
	if !ok {
		t.Fatal("should parse")
	}
	if p.article.Title != "HTML newsletter" {
		t.Errorf("title = %q", p.article.Title)
	}
	html := derefOr(p.article.ContentHTML)
	// HTML part is preferred and sanitized (no <script>).
	if !strings.Contains(html, "Big News") {
		t.Errorf("html missing Big News: %q", html)
	}
	if strings.Contains(html, "<script>") {
		t.Errorf("html should not contain <script>: %q", html)
	}
	// body_text is the text rendering of the HTML part (not the plain fallback).
	if !strings.Contains(p.article.BodyText, "Big News") {
		t.Errorf("bodyText missing Big News: %q", p.article.BodyText)
	}
	if !strings.Contains(p.article.BodyText, "HTML") {
		t.Errorf("bodyText missing HTML body text: %q", p.article.BodyText)
	}
}

func TestFarFutureDateIsClampedToNow(t *testing.T) {
	// A Date: header decades in the future must not pin the message to the top
	// of the list forever — it is clamped down to ~now.
	raw := "From: a@b.com\r\n" +
		"Subject: From the future\r\n" +
		"Date: Mon, 01 Jan 2099 12:00:00 +0000\r\n" +
		"Message-ID: <future@b.com>\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"body\r\n"
	p, ok := parseEmail([]byte(raw))
	if !ok {
		t.Fatal("parse")
	}
	if p.article.PublishedAt == nil {
		t.Fatal("has a date")
	}
	parsed, err := time.Parse(time.RFC3339, *p.article.PublishedAt)
	if err != nil {
		t.Fatalf("rfc3339: %v", err)
	}
	// Clamped to roughly "now", well short of the year-2099 header.
	if !parsed.UTC().Before(time.Now().UTC().Add(48 * time.Hour)) {
		t.Errorf("future date not clamped: %v", parsed)
	}
}

func TestMissingDateYieldsNilPublishedAt(t *testing.T) {
	raw := "From: x@y.com\r\n" +
		"Subject: No date here\r\n" +
		"Message-ID: <nodate@y.com>\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"body\r\n"
	p, ok := parseEmail([]byte(raw))
	if !ok {
		t.Fatal("should parse")
	}
	if p.article.PublishedAt != nil {
		t.Errorf("publishedAt = %v, want nil", *p.article.PublishedAt)
	}
	if p.article.Title != "No date here" {
		t.Errorf("title = %q", p.article.Title)
	}
}

func TestMissingMessageIDSynthesizesStableGUID(t *testing.T) {
	raw := "From: a@b.com\r\n" +
		"Subject: No id\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"some content\r\n"
	p1, ok1 := parseEmail([]byte(raw))
	p2, ok2 := parseEmail([]byte(raw))
	if !ok1 || !ok2 {
		t.Fatal("parse")
	}
	if !strings.HasPrefix(p1.article.GUID, "papr-newsletter-") {
		t.Errorf("guid = %q, want papr-newsletter- prefix", p1.article.GUID)
	}
	// Deterministic: the same bytes always hash to the same guid.
	if p1.article.GUID != p2.article.GUID {
		t.Errorf("guids differ: %q vs %q", p1.article.GUID, p2.article.GUID)
	}
}

func TestSenderWithoutDisplayNameUsesAddress(t *testing.T) {
	raw := "From: bare@example.org\r\n" +
		"Subject: Bare sender\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"hi\r\n"
	p, ok := parseEmail([]byte(raw))
	if !ok {
		t.Fatal("parse")
	}
	if p.fromName != "bare@example.org" {
		t.Errorf("fromName = %q, want bare@example.org", p.fromName)
	}
	if p.fromAddr != "bare@example.org" {
		t.Errorf("fromAddr = %q, want bare@example.org", p.fromAddr)
	}
}

func TestMissingSubjectFallsBack(t *testing.T) {
	raw := "From: a@b.com\r\n" +
		"Message-ID: <s@b.com>\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"body\r\n"
	p, ok := parseEmail([]byte(raw))
	if !ok {
		t.Fatal("parse")
	}
	if p.article.Title != "(no subject)" {
		t.Errorf("title = %q, want (no subject)", p.article.Title)
	}
}

func TestEncodedWordSubjectIsDecoded(t *testing.T) {
	// RFC 2047 encoded-word: "=?utf-8?B?...?=" → "Héllo"
	raw := "From: a@b.com\r\n" +
		"Subject: =?utf-8?B?SMOpbGxv?=\r\n" +
		"Message-ID: <enc@b.com>\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"body\r\n"
	p, ok := parseEmail([]byte(raw))
	if !ok {
		t.Fatal("parse")
	}
	if p.article.Title != "Héllo" {
		t.Errorf("title = %q, want Héllo", p.article.Title)
	}
}

func TestQuotedPrintableBodyIsDecoded(t *testing.T) {
	raw := "From: a@b.com\r\n" +
		"Subject: QP body\r\n" +
		"Message-ID: <qp@b.com>\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"Caf=C3=A9 time=21\r\n"
	p, ok := parseEmail([]byte(raw))
	if !ok {
		t.Fatal("parse")
	}
	if !strings.Contains(p.article.BodyText, "Café time!") {
		t.Errorf("body was: %q", p.article.BodyText)
	}
}

func TestHTMLSpecialCharsEscapedInPlainWrap(t *testing.T) {
	raw := "From: a@b.com\r\n" +
		"Subject: Angle brackets\r\n" +
		"Message-ID: <ab@b.com>\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"use <div> & such\r\n"
	p, ok := parseEmail([]byte(raw))
	if !ok {
		t.Fatal("parse")
	}
	html := derefOr(p.article.ContentHTML)
	if !strings.Contains(html, "&lt;div&gt;") {
		t.Errorf("html missing escaped div: %q", html)
	}
	if !strings.Contains(html, "&amp;") {
		t.Errorf("html missing escaped amp: %q", html)
	}
}

func TestMultipartAttachmentBecomesEnclosure(t *testing.T) {
	// A mixed message with a text body and a named attachment: the attachment
	// surfaces as a cid: pseudo-URL enclosure (mirroring attachments() →
	// Enclosure{ url: "cid:<name>", ... }).
	raw := "From: pod@cast.com\r\n" +
		"Subject: With attachment\r\n" +
		"Message-ID: <att@cast.com>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"MIX\"\r\n" +
		"\r\n" +
		"--MIX\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"see attached\r\n" +
		"--MIX\r\n" +
		"Content-Type: audio/mpeg; name=\"ep1.mp3\"\r\n" +
		"Content-Disposition: attachment; filename=\"ep1.mp3\"\r\n" +
		"\r\n" +
		"FAKEAUDIOBYTES\r\n" +
		"--MIX--\r\n"
	p, ok := parseEmail([]byte(raw))
	if !ok {
		t.Fatal("parse")
	}
	if len(p.article.Enclosures) != 1 {
		t.Fatalf("want 1 enclosure, got %d", len(p.article.Enclosures))
	}
	enc := p.article.Enclosures[0]
	if enc.URL != "cid:ep1.mp3" {
		t.Errorf("enclosure url = %q, want cid:ep1.mp3", enc.URL)
	}
	if enc.MimeType == nil || *enc.MimeType != "audio/mpeg" {
		t.Errorf("enclosure mime = %v, want audio/mpeg", enc.MimeType)
	}
	if enc.Length == nil || *enc.Length == 0 {
		t.Errorf("enclosure length = %v, want non-zero", enc.Length)
	}
}

func TestGarbageBytesStillParse(t *testing.T) {
	// go-message is lenient: arbitrary bytes parse as a body-only email. We
	// only require it not to panic and to produce some article.
	_, ok := parseEmail([]byte("not really an email"))
	if !ok {
		t.Error("garbage should still parse as a degenerate email")
	}
}

func TestSyntheticURLIsUniquePerMailbox(t *testing.T) {
	cfg := models.NewsletterConfig{
		Host:     "imap.gmail.com",
		Port:     993,
		Username: "me@gmail.com",
		Password: "secret",
		Folder:   "Newsletters",
	}
	if got := synthFeedURL(cfg); got != "imap://me@gmail.com@imap.gmail.com:993/Newsletters" {
		t.Errorf("synthFeedURL = %q", got)
	}
}

func TestStableHashDeterministic(t *testing.T) {
	a := stableHash([]byte("hello world"))
	b := stableHash([]byte("hello world"))
	c := stableHash([]byte("hello worle"))
	if a != b {
		t.Errorf("stableHash not deterministic: %d vs %d", a, b)
	}
	if a == c {
		t.Errorf("stableHash collided on distinct input: %d", a)
	}
}

func TestClampPublishDate(t *testing.T) {
	now := time.Now().UTC()
	// A date well in the past is returned unchanged.
	past := now.Add(-72 * time.Hour)
	if got := clampPublishDate(past); !got.Equal(past) {
		t.Errorf("past date altered: got %v want %v", got, past)
	}
	// A date within the 24h grace window is kept.
	soon := now.Add(12 * time.Hour)
	if got := clampPublishDate(soon); !got.Equal(soon) {
		t.Errorf("near-future date altered: got %v want %v", got, soon)
	}
	// A far-future date is pulled down to ~now (<= cutoff).
	far := now.Add(365 * 24 * time.Hour)
	got := clampPublishDate(far)
	if !got.Before(now.Add(25 * time.Hour)) {
		t.Errorf("far-future date not clamped: %v", got)
	}
}

func TestTruncateRunes(t *testing.T) {
	// Multi-byte runes must not be split mid-character.
	s := strings.Repeat("é", 300)
	got := truncateRunes(s, 280)
	if n := len([]rune(got)); n != 280 {
		t.Errorf("rune count = %d, want 280", n)
	}
	if !strings.HasPrefix(s, got) {
		t.Error("truncation is not a prefix of the original")
	}
	// Shorter-than-limit input is returned whole.
	if got := truncateRunes("abc", 280); got != "abc" {
		t.Errorf("short input altered: %q", got)
	}
}
