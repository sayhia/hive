package services

import (
	"strings"

	"hive/internal/apperr"
	"hive/internal/appstate"
	"hive/internal/db"
	"hive/internal/events"
	"hive/internal/extraction"
	"hive/internal/ingestion"
	"hive/internal/models"
	"hive/internal/sanitize"
)

// ArticleService exposes the article list / reader / read-state commands.
// Mirrors the article commands in commands.rs.
type ArticleService struct {
	app *appstate.State
}

// ListArticles returns the article rows for the current sidebar selection.
// search is null for no full-text filter.
func (s *ArticleService) ListArticles(query models.ArticleQuery, unreadOnly bool, search *string, oldestFirst bool, limit, offset int64) ([]models.ArticleSummary, error) {
	return db.ListArticles(bg(), s.app.DB.R, query, unreadOnly, search, oldestFirst, limit, offset)
}

// GetArticle returns the full article for the reading pane.
func (s *ArticleService) GetArticle(id int64) (*models.ArticleDetail, error) {
	return db.GetArticle(bg(), s.app.DB.R, id)
}

// Translations returns every cached (lang, engine) rendering of an article so
// the reader can show a previously translated copy instantly, without a new job.
func (s *ArticleService) Translations(id int64) ([]models.TranslationCache, error) {
	return db.GetTranslations(bg(), s.app.DB.R, id)
}

// MarkRead sets an article's read flag, queueing the change for sync when a
// server is linked.
func (s *ArticleService) MarkRead(id int64, read bool) error {
	ctx := bg()
	if err := db.SetRead(ctx, s.app.DB.W, id, read); err != nil {
		return err
	}
	enqueueIfConnected(ctx, s.app.DB.W, id, "read", read)
	// TODO(phase5): refresh the Dock badge and menu-bar tray here.
	return nil
}

// MarkStarred sets an article's starred flag, queueing the change for sync.
func (s *ArticleService) MarkStarred(id int64, starred bool) error {
	ctx := bg()
	if err := db.SetStarred(ctx, s.app.DB.W, id, starred); err != nil {
		return err
	}
	enqueueIfConnected(ctx, s.app.DB.W, id, "starred", starred)
	return nil
}

// MarkReadLater toggles an article's read-later flag.
func (s *ArticleService) MarkReadLater(id int64, value bool) error {
	return db.SetReadLater(bg(), s.app.DB.W, id, value)
}

// MarkAllRead marks every article in the current selection read and returns how
// many were flipped. Emits feeds-updated so the sidebar counts refresh.
func (s *ArticleService) MarkAllRead(query models.ArticleQuery) (int64, error) {
	ctx := bg()
	n, err := db.MarkAllRead(ctx, s.app.DB, query, db.IsFreshrssConnected(ctx, s.app.DB.R))
	if err != nil {
		return 0, err
	}
	events.Emit("feeds-updated", 0)
	// TODO(phase5): refresh the Dock badge and menu-bar tray here.
	return n, nil
}

// SmartCounts returns the unread / starred / read-later counts for the sidebar.
func (s *ArticleService) SmartCounts() (models.SmartCounts, error) {
	return db.SmartCounts(bg(), s.app.DB.R)
}

// ExtractFulltext fetches the article's source page and extracts its full text
// (Readability), storing the result so subsequent reads are instant/offline.
// Returns the extracted HTML.
func (s *ArticleService) ExtractFulltext(articleID int64) (string, error) {
	ctx := bg()
	detail, err := db.GetArticle(ctx, s.app.DB.R, articleID)
	if err != nil {
		return "", err
	}
	if detail.ExtractedHTML != nil && strings.TrimSpace(*detail.ExtractedHTML) != "" {
		return *detail.ExtractedHTML, nil
	}
	if detail.URL == nil || *detail.URL == "" {
		return "", apperr.Code("noArticleUrl")
	}
	bytes, ct, finalURL, err := ingestion.Get(ctx, s.app.HTTP(), *detail.URL)
	if err != nil {
		return "", err
	}
	// Decode in the page's declared charset so a non-UTF-8 page does not become
	// mojibake before Readability runs.
	html := ingestion.DecodeHTML(bytes, ct)
	content, leadImage, err := extraction.Extract(html, finalURL)
	if err != nil {
		return "", err
	}
	imageURL := sanitize.FirstImage(content)
	if imageURL == nil {
		imageURL = leadImage
	}
	if err := db.SetExtractedHTML(ctx, s.app.DB, articleID, content, imageURL); err != nil {
		return "", err
	}
	return content, nil
}
