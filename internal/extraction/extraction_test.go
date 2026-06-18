package extraction

import (
	"errors"
	"strings"
	"testing"

	"hive/internal/apperr"
)

// articleHTML is a simple, content-rich page: an og:image plus enough article
// body for Readability to identify and extract the main content.
const articleHTML = `<!DOCTYPE html>
<html>
<head>
<title>A Simple Article</title>
<meta property="og:image" content="https://ex.com/a.jpg">
</head>
<body>
<header><nav>Home About Contact</nav></header>
<article>
<h1>A Simple Article</h1>
<p>This is the first paragraph of a reasonably long article. It contains
enough text that the Readability algorithm will recognise it as the main
content of the page rather than navigation or boilerplate chrome.</p>
<p>Here is a second substantial paragraph. Readability scores nodes by the
amount of text and the density of links, so a few full sentences of prose
like this one help it lock on to the real article body and discard the rest
of the surrounding markup that makes up a typical web page layout.</p>
<p>A third paragraph adds further weight to the candidate so the extractor is
confident about what to keep. Lorem ipsum dolor sit amet, consectetur
adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore.</p>
</article>
<footer>Copyright 2026</footer>
</body>
</html>`

func TestExtractReturnsContentAndLeadImage(t *testing.T) {
	content, leadImage, err := Extract(articleHTML, "https://site.test/post")
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if strings.TrimSpace(content) == "" {
		t.Fatal("Extract returned empty content")
	}
	if !strings.Contains(content, "first paragraph") {
		t.Errorf("extracted content missing expected article text; got:\n%s", content)
	}
	if leadImage == nil {
		t.Fatal("Extract returned nil lead image, want og:image")
	}
	if *leadImage != "https://ex.com/a.jpg" {
		t.Errorf("lead image = %q, want %q", *leadImage, "https://ex.com/a.jpg")
	}
}

func TestExtractEmptyContent(t *testing.T) {
	// A page with no extractable article body should yield the coded error.
	_, _, err := Extract("<html><head></head><body></body></html>", "https://site.test/")
	if err == nil {
		t.Fatal("expected error for non-extractable content, got nil")
	}
	var ae *apperr.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("error is not *apperr.AppError: %v", err)
	}
	if ae.Code != "noExtractableContent" {
		t.Errorf("error code = %q, want %q", ae.Code, "noExtractableContent")
	}
}

func TestLeadImageReadsOGImage(t *testing.T) {
	html := `<meta property="og:image" content="https://ex.com/a.jpg">`
	got := leadImageFrom(html, "https://site.test/post")
	if got == nil || *got != "https://ex.com/a.jpg" {
		t.Errorf("leadImageFrom = %v, want https://ex.com/a.jpg", deref(got))
	}
}

func TestLeadImageResolvesRelativeURLs(t *testing.T) {
	html := `<meta name="twitter:image" content="/img/a.jpg">`
	got := leadImageFrom(html, "https://site.test/post/1")
	if got == nil || *got != "https://site.test/img/a.jpg" {
		t.Errorf("leadImageFrom = %v, want https://site.test/img/a.jpg", deref(got))
	}
}

func TestLeadImageSkipsDataURI(t *testing.T) {
	html := `<meta property="og:image" content="data:image/png;base64,iVBORw0KGgo=">`
	if got := leadImageFrom(html, "https://site.test/post"); got != nil {
		t.Errorf("leadImageFrom = %v, want nil for data: URI", *got)
	}
}

func TestLeadImageSkipsUnresolvableRelative(t *testing.T) {
	// Relative URL with no usable base cannot be resolved to http(s).
	html := `<meta property="og:image" content="/img/a.jpg">`
	if got := leadImageFrom(html, ""); got != nil {
		t.Errorf("leadImageFrom = %v, want nil for unresolvable relative URL", *got)
	}
}

func TestLeadImageNoneReturnsNil(t *testing.T) {
	html := `<meta name="description" content="no image here">`
	if got := leadImageFrom(html, "https://site.test/post"); got != nil {
		t.Errorf("leadImageFrom = %v, want nil when no image meta present", *got)
	}
}

func TestLeadImagePrefersHrefForLinkRel(t *testing.T) {
	html := `<link rel="image_src" href="https://ex.com/hero.png">`
	got := leadImageFrom(html, "https://site.test/post")
	if got == nil || *got != "https://ex.com/hero.png" {
		t.Errorf("leadImageFrom = %v, want https://ex.com/hero.png", deref(got))
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
