// Package newsletter implements email-newsletter ingestion (papr feature F5,
// Part B), ported from papr's src-tauri/src/ingestion/newsletter.rs.
//
// Competitors offer a hosted "dedicated address" for newsletters; that needs a
// mail server hive — being local-first — does not run. The local-first
// equivalent is to poll an IMAP mailbox the user already owns (a Gmail label, a
// Fastmail folder, …) and turn each message into an article.
//
// # Design
//
// A newsletter source is stored as a normal feed row (source_type =
// 'newsletter') so it shows up in the sidebar, carries articles, and
// participates in retention/search exactly like an RSS feed. Its IMAP
// connection details (models.NewsletterConfig) live in a dedicated table. The
// app-password is stored in plaintext, the same approach the FreshRSS sync path
// takes (the whole DB is local to the user's machine).
//
// # Testability
//
// The email → article conversion (parseEmail) is a pure function over raw
// RFC822 bytes, exhaustively unit-tested. The live IMAP connection
// (fetchRecent) is the only impure part and is isolated so the parsing layer
// can be tested without a network or a mail account.
package newsletter

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
	"github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"

	"hive/internal/apperr"
	"hive/internal/db"
	"hive/internal/models"
	"hive/internal/sanitize"
)

// defaultFetchLimit is how many of the most recent messages (newest tail) to
// pull from the mailbox per poll. Matches the bound the Rust caller passes to
// fetch_recent.
const defaultFetchLimit = 50

// Poll connects to cfg.Host:cfg.Port over implicit TLS, logs in, selects
// cfg.Folder, fetches the most recent messages and parses each into a
// db.NewArticle. The HTML body is preferred (sanitized into ContentHTML, with
// BodyText derived from it); a plain-text-only mail is wrapped in a <pre>
// block. The returned slice is newest-relevant first (the order the IMAP server
// returned the newest-tail range). The caller upserts each article.
func Poll(ctx context.Context, cfg models.NewsletterConfig) ([]db.NewArticle, error) {
	raws, err := fetchRecent(ctx, cfg, defaultFetchLimit)
	if err != nil {
		return nil, err
	}

	// Parse newest-first so the article list shows the most recent mail at the
	// top. The IMAP fetch returns the tail range in ascending sequence order,
	// so reverse it here.
	articles := make([]db.NewArticle, 0, len(raws))
	for i := len(raws) - 1; i >= 0; i-- {
		if a, ok := parseEmail(raws[i]); ok {
			articles = append(articles, a.article)
		}
	}
	return articles, nil
}

// parsedEmail is a converted email, ready to be persisted as an article.
type parsedEmail struct {
	// fromName is the From display name, falling back to the address — used as
	// author. Surfaced separately so tests (and potential callers) can group /
	// label by sender.
	fromName string
	// fromAddr is the bare From email address.
	fromAddr string
	// article is the body — sanitized HTML when the mail had an HTML part,
	// otherwise plain text wrapped as a <pre> block.
	article db.NewArticle
}

// parseEmail converts one raw RFC822 message into an article.
//
// Pure and network-free. Mapping:
//   - Subject           → title
//   - From display name → author (address used when there is no name)
//   - HTML body (or text) → sanitized content
//   - Date              → PublishedAt (RFC 3339); nil when absent
//   - Message-ID        → GUID (falls back to a content hash)
//
// Returns ok == false only when the bytes do not parse as an email at all.
// go-message is lenient: arbitrary bytes parse as a body-only email, so in
// practice this almost always succeeds (mirroring mail-parser's leniency).
func parseEmail(raw []byte) (parsedEmail, bool) {
	// go-message returns an IsUnknownCharset error alongside a usable Entity for
	// exotic charsets. Unlike Rust's mail-parser it is NOT lenient about a
	// malformed header: arbitrary bytes yield a nil entity and a hard error. To
	// preserve mail-parser's "any bytes parse as a body-only email" contract —
	// so a poll never silently drops a malformed message — fall back to treating
	// the raw bytes as a degenerate plain-text body.
	entity, err := message.Read(bytes.NewReader(raw))
	if entity == nil || (err != nil && !message.IsUnknownCharset(err)) {
		return degenerateEmail(raw), true
	}

	header := gomail.Header{Header: entity.Header}

	// ── Subject. Header.Subject() decodes RFC 2047 encoded-words. ──
	subject, _ := header.Subject()
	subject = strings.TrimSpace(subject)
	title := subject
	if title == "" {
		title = "(no subject)"
	}

	// ── Sender: prefer the display name, fall back to the address. ──
	fromName, fromAddr := "Unknown sender", ""
	if addrs, _ := header.AddressList("From"); len(addrs) > 0 {
		a := addrs[0]
		fromAddr = strings.TrimSpace(a.Address)
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = fromAddr
		}
		fromName = name
	}

	// ── Body: a genuine HTML part wins; otherwise wrap the plain-text part.
	// go-message synthesizes nothing — we walk the parts and keep the first
	// real text/html part and the first text/plain part. ──
	htmlBody, textBody, enclosures := walkParts(entity)

	var contentHTML *string
	var bodyText string
	switch {
	case htmlBody != "":
		sanitized := sanitize.Sanitize(htmlBody, "")
		contentHTML = &sanitized
		bodyText = sanitize.HTMLToText(htmlBody)
	case textBody != "":
		// Plain-text newsletter: preserve its line breaks by wrapping the
		// (HTML-escaped) text in a <pre> block.
		text := strings.TrimSpace(textBody)
		bodyText = text
		wrapped := "<pre>" + sanitize.EscapeHTML(text) + "</pre>"
		contentHTML = &wrapped
	default:
		// no usable body part
	}

	// ── Date → RFC 3339. Header.Date() already decodes RFC 2822 dates. A
	// future-dated Date: header (clock-wrong sender, spam) would otherwise pin
	// the message to the top of the newest-first list forever, so clamp it the
	// same way the RSS path does in clampPublishDate. ──
	var publishedAt *string
	if d, derr := header.Date(); derr == nil && !d.IsZero() {
		s := clampPublishDate(d.UTC()).Format(time.RFC3339)
		publishedAt = &s
	}

	// ── GUID: Message-ID is globally unique; hash the body if missing. ──
	guid := ""
	if mid, _ := header.MessageID(); mid != "" {
		guid = mid
	} else {
		guid = fmt.Sprintf("papr-newsletter-%d", stableHash(raw))
	}

	// ── Summary: the first 280 chars of the rendered body text. ──
	var summary *string
	if bodyText != "" {
		s := truncateRunes(bodyText, 280)
		summary = &s
	}

	author := fromName
	article := db.NewArticle{
		GUID:        guid,
		URL:         nil,
		Title:       title,
		Author:      &author,
		Summary:     summary,
		ContentHTML: contentHTML,
		BodyText:    bodyText,
		ImageURL:    nil,
		PublishedAt: publishedAt,
		Enclosures:  enclosures,
	}

	return parsedEmail{fromName: fromName, fromAddr: fromAddr, article: article}, true
}

// degenerateEmail builds a body-only article from bytes go-message refused to
// parse as a structured message, mirroring mail-parser's leniency (any input
// produces *some* article). The raw bytes become the plain-text body; all
// header-derived fields take their usual "missing" fallbacks and the GUID is
// synthesized from the bytes so re-polling the same junk still dedups.
func degenerateEmail(raw []byte) parsedEmail {
	text := strings.TrimSpace(string(raw))
	var contentHTML *string
	var summary *string
	if text != "" {
		wrapped := "<pre>" + sanitize.EscapeHTML(text) + "</pre>"
		contentHTML = &wrapped
		s := truncateRunes(text, 280)
		summary = &s
	}
	author := "Unknown sender"
	article := db.NewArticle{
		GUID:        fmt.Sprintf("papr-newsletter-%d", stableHash(raw)),
		Title:       "(no subject)",
		Author:      &author,
		Summary:     summary,
		ContentHTML: contentHTML,
		BodyText:    text,
	}
	return parsedEmail{fromName: "Unknown sender", article: article}
}

// walkParts traverses the message body, returning the first text/html part, the
// first text/plain part, and the attachments as enclosures. Both bodies are
// returned so the caller can apply HTML-over-text precedence; only the chosen
// one is rendered. Mirrors mail-parser's body_html(0)/body_text(0) "first part
// of that type" semantics plus its attachments() iterator.
func walkParts(entity *message.Entity) (htmlBody, textBody string, enclosures []models.Enclosure) {
	mr := gomail.NewReader(entity)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !message.IsUnknownCharset(err) {
			break
		}
		if part == nil {
			break
		}

		switch h := part.Header.(type) {
		case *gomail.InlineHeader:
			contentType, _, _ := h.ContentType()
			body, _ := io.ReadAll(part.Body)
			switch {
			case strings.EqualFold(contentType, "text/html") && htmlBody == "":
				htmlBody = string(body)
			case strings.EqualFold(contentType, "text/plain") && textBody == "":
				textBody = string(body)
			}
		case *gomail.AttachmentHeader:
			// Emails embed attachment bytes inline; we only surface a
			// descriptive pseudo-URL (there is nothing to link to).
			name, _ := h.Filename()
			if name == "" {
				// Skip nameless attachments — mail-parser's attachment_name()
				// returns None for these and they are filtered out.
				io.Copy(io.Discard, part.Body)
				continue
			}
			body, _ := io.ReadAll(part.Body)
			length := int64(len(body))
			var mime *string
			if ct, _, cterr := h.ContentType(); cterr == nil && ct != "" {
				m := ct
				mime = &m
			}
			enclosures = append(enclosures, models.Enclosure{
				URL:      "cid:" + name,
				MimeType: mime,
				Length:   &length,
			})
		default:
			// Drain unknown part bodies so NextPart advances cleanly.
			io.Copy(io.Discard, part.Body)
		}
	}
	return htmlBody, textBody, enclosures
}

// clampPublishDate caps a publish date at now + 24h: a far-future date (a
// clock-wrong sender, spam) is pulled down to "now" so it cannot pin the
// message to the top of the newest-first list forever. Mirrors papr's
// ingestion::parse::clamp_publish_date.
func clampPublishDate(date time.Time) time.Time {
	now := time.Now().UTC()
	cutoff := now.Add(24 * time.Hour)
	if date.After(cutoff) {
		return now
	}
	return date
}

// stableHash is a deterministic 64-bit hash, used to synthesize a guid for an
// email that carries no Message-ID header (so re-polling the same mail still
// dedups). The Rust side uses std's DefaultHasher; any stable 64-bit hash over
// the same bytes serves the same purpose — here FNV-1a.
func stableHash(b []byte) uint64 {
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64()
}

// truncateRunes returns the first n runes of s (so multi-byte characters are
// never split), matching Rust's `chars().take(n)`.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// synthFeedURL builds the synthetic feed URL for a newsletter source. It is not
// fetched over HTTP — it only gives the feeds.feed_url UNIQUE constraint a
// value so the same mailbox cannot be added twice. Mirrors synthetic_feed_url.
func synthFeedURL(cfg models.NewsletterConfig) string {
	return fmt.Sprintf("imap://%s@%s:%d/%s", cfg.Username, cfg.Host, cfg.Port, cfg.Folder)
}

// ─────────────────────────── live IMAP ───────────────────────────

// fetchRecent connects to the IMAP server over implicit TLS and returns the raw
// RFC822 bytes of the most recent limit messages in cfg.Folder.
//
// This is the only impure function in this package. The go-imap client is
// blocking; ctx is honoured for the dial deadline and checked after each step
// so a cancelled poll returns promptly. Parsing the returned bytes is delegated
// to the pure parseEmail.
func fetchRecent(ctx context.Context, cfg models.NewsletterConfig, limit int) ([][]byte, error) {
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))

	// Implicit-TLS IMAP (port 993). DialTLS wraps the TLS handshake; ServerName
	// must be set for certificate verification against the host.
	dialer := &net.Dialer{}
	if deadline, ok := ctx.Deadline(); ok {
		dialer.Deadline = deadline
	}
	tlsConn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: cfg.Host})
	if err != nil {
		return nil, apperr.Codef("newsletterConnectFailed", "IMAP connect failed: %v", err)
	}
	client, err := imapclient.New(tlsConn)
	if err != nil {
		tlsConn.Close()
		return nil, apperr.Codef("newsletterConnectFailed", "IMAP connect failed: %v", err)
	}
	// Bound every IMAP command: go-imap's Timeout defaults to 0 (wait forever)
	// and the dial deadline is cleared once connected, so without this a server
	// that stalls mid-command would hang the poll indefinitely (and leak the
	// Fetch goroutine below).
	if deadline, ok := ctx.Deadline(); ok {
		client.Timeout = time.Until(deadline)
	} else {
		client.Timeout = 60 * time.Second
	}
	// Logout best-effort closes the connection (and the underlying conn).
	defer func() { _ = client.Logout() }()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := client.Login(cfg.Username, cfg.Password); err != nil {
		return nil, apperr.Codef("newsletterLoginFailed", "IMAP login failed: %v", err)
	}

	// Select read-only: polling must never set the \Seen flag on the user's
	// mail or otherwise mutate the mailbox.
	mbox, err := client.Select(cfg.Folder, true)
	if err != nil {
		return nil, apperr.Codef("newsletterSelectFailed", "IMAP select '%s' failed: %v", cfg.Folder, err)
	}

	total := mbox.Messages
	if total == 0 {
		return nil, nil
	}

	// Fetch the last `limit` messages by sequence number (newest tail).
	start := uint32(1)
	if total > uint32(limit) {
		start = total - uint32(limit) + 1
	}
	seqset := new(imap.SeqSet)
	seqset.AddRange(start, total)

	section, err := imap.ParseBodySectionName(imap.FetchRFC822)
	if err != nil {
		return nil, apperr.Wrap("newsletterFetchFailed", err)
	}

	messages := make(chan *imap.Message, 16)
	fetchErr := make(chan error, 1)
	go func() {
		fetchErr <- client.Fetch(seqset, []imap.FetchItem{imap.FetchRFC822}, messages)
	}()

	var bodies [][]byte
	for msg := range messages {
		if msg == nil {
			continue
		}
		body := msg.GetBody(section)
		if body == nil {
			continue
		}
		raw, rerr := io.ReadAll(body)
		if rerr != nil {
			continue
		}
		bodies = append(bodies, raw)
	}
	if err := <-fetchErr; err != nil {
		return nil, apperr.Codef("newsletterFetchFailed", "IMAP fetch failed: %v", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return bodies, nil
}
