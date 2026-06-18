package db

import (
	"context"
	"testing"
)

func TestSetExtractedHTMLBackfillsImage(t *testing.T) {
	d, _, id := seed(t)
	ctx := context.Background()
	if err := SetExtractedHTML(ctx, d, id, "<p>full text</p>", ptr("https://ex.com/lead.jpg")); err != nil {
		t.Fatal(err)
	}
	after, _ := GetArticle(ctx, d.R, id)
	if deref(after.ExtractedHTML) != "<p>full text</p>" {
		t.Errorf("extracted_html = %v", after.ExtractedHTML)
	}
	if deref(after.ImageURL) != "https://ex.com/lead.jpg" {
		t.Errorf("image not backfilled: %v", after.ImageURL)
	}
}

func TestSetExtractedHTMLKeepsExistingImage(t *testing.T) {
	d, _, id := seed(t)
	ctx := context.Background()
	if _, err := d.W.ExecContext(ctx, "UPDATE articles SET image_url = ?2 WHERE id = ?1", id, "https://ex.com/feed.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := SetExtractedHTML(ctx, d, id, "<p>full text</p>", ptr("https://ex.com/lead.jpg")); err != nil {
		t.Fatal(err)
	}
	after, _ := GetArticle(ctx, d.R, id)
	if deref(after.ImageURL) != "https://ex.com/feed.jpg" {
		t.Errorf("existing image overwritten: %v", after.ImageURL)
	}
}

func TestCardImageBackfillScan(t *testing.T) {
	d, _, id := seed(t)
	ctx := context.Background()
	if err := SetExtractedHTML(ctx, d, id,
		`<p>full text</p><img src="https://ex.com/from-extracted.jpg">`, nil); err != nil {
		t.Fatal(err)
	}
	updates, err := CardImageBackfillScan(ctx, d.R)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0].ID != id || updates[0].ImageURL != "https://ex.com/from-extracted.jpg" {
		t.Fatalf("backfill scan = %+v", updates)
	}
	// Applying the update sets image_url.
	if err := ApplyCardImages(ctx, d, updates); err != nil {
		t.Fatal(err)
	}
	after, _ := GetArticle(ctx, d.R, id)
	if deref(after.ImageURL) != "https://ex.com/from-extracted.jpg" {
		t.Errorf("apply did not set image: %v", after.ImageURL)
	}
}

func TestCardImageBackfillIncludesBlankImageURL(t *testing.T) {
	d, _, id := seed(t)
	ctx := context.Background()
	// A blank ('' not NULL) image_url must still be scanned.
	if _, err := d.W.ExecContext(ctx,
		`UPDATE articles SET image_url = '', content_html = '<p>x</p><img src="https://ex.com/blank.jpg">' WHERE id = ?1`,
		id); err != nil {
		t.Fatal(err)
	}
	updates, err := CardImageBackfillScan(ctx, d.R)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0].ImageURL != "https://ex.com/blank.jpg" {
		t.Fatalf("blank image_url not scanned: %+v", updates)
	}
}

func TestArticleTextPrefersExtracted(t *testing.T) {
	d, _, id := seed(t)
	ctx := context.Background()
	// With only the feed body, ArticleText returns it.
	_, text, err := ArticleText(ctx, d.R, id)
	if err != nil {
		t.Fatal(err)
	}
	if text != "body" {
		t.Errorf("text = %q, want feed body", text)
	}
	// After extraction, it returns the extracted plain text.
	if err := SetExtractedHTML(ctx, d, id, "<p>the full extracted article</p>", nil); err != nil {
		t.Fatal(err)
	}
	_, text, _ = ArticleText(ctx, d.R, id)
	if text != "the full extracted article" {
		t.Errorf("text = %q, want extracted", text)
	}
}

// ── list-preview translation cache (papr #92) ───────────────────────

func TestPreviewTranslationCacheKeyedByLangAndEngine(t *testing.T) {
	d, _, id := seed(t)
	ctx := context.Background()

	for _, c := range []struct{ lang, engine, title, snippet string }{
		{"zh", "google", "标题", "摘要"},
		{"ja", "google", "題名", "要約"},
		{"ja", "deepl", "見出し", "抜粋"},
	} {
		if err := SetPreviewTranslation(ctx, d.W, id, c.lang, c.engine, c.title, c.snippet); err != nil {
			t.Fatal(err)
		}
	}
	title, snippet, ok, err := GetPreviewTranslation(ctx, d.R, id, "ja", "google")
	if err != nil || !ok || title != "題名" || snippet != "要約" {
		t.Fatalf("ja/google = %q/%q ok=%v err=%v", title, snippet, ok, err)
	}
	if _, _, ok, _ := GetPreviewTranslation(ctx, d.R, id, "en", "google"); ok {
		t.Error("uncached lang must miss")
	}
	// Re-set updates in place (no duplicate-key error).
	if err := SetPreviewTranslation(ctx, d.W, id, "zh", "google", "新标题", "新摘要"); err != nil {
		t.Fatal(err)
	}
	title, _, _, _ = GetPreviewTranslation(ctx, d.R, id, "zh", "google")
	if title != "新标题" {
		t.Fatalf("upsert should update in place, got %q", title)
	}
}

func TestPreviewTranslationClearedWhenSourceChanges(t *testing.T) {
	d, _, id := seed(t)
	ctx := context.Background()
	if err := SetPreviewTranslation(ctx, d.W, id, "zh", "google", "标题", "摘要"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.W.ExecContext(ctx,
		"UPDATE articles SET title = 'New', body_text = 'New body' WHERE id = ?1", id); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := GetPreviewTranslation(ctx, d.R, id, "zh", "google"); ok {
		t.Error("source change must invalidate the cached preview (AU trigger)")
	}
}
