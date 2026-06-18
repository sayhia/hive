package services

import (
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"hive/internal/apperr"
	"hive/internal/appstate"
	"hive/internal/db"
	"hive/internal/models"
)

// HighlightService exposes the highlight / annotation layer (feature F7).
type HighlightService struct {
	app *appstate.State
}

// CreateHighlight inserts a highlight and returns its new id. The anchor fields
// (prefix / suffix / textOffset) let the frontend re-locate the span even after
// the rendered text shifts.
func (s *HighlightService) CreateHighlight(articleID int64, quote, prefix, suffix string, textOffset int64, color, note string) (int64, error) {
	if strings.TrimSpace(quote) == "" {
		return 0, apperr.Code("emptyHighlight")
	}
	return db.InsertHighlight(bg(), s.app.DB.W, db.NewHighlight{
		ArticleID:  articleID,
		Quote:      quote,
		Prefix:     prefix,
		Suffix:     suffix,
		TextOffset: textOffset,
		Color:      color,
		Note:       note,
	})
}

// ListHighlights returns all highlights for one article, oldest first.
func (s *HighlightService) ListHighlights(articleID int64) ([]models.Highlight, error) {
	return db.ListHighlights(bg(), s.app.DB.R, articleID)
}

// ListAllHighlights returns every highlight enriched with its article and feed,
// newest first — used by the Highlights browser panel.
func (s *HighlightService) ListAllHighlights() ([]models.HighlightWithContext, error) {
	return db.ListAllHighlights(bg(), s.app.DB.R)
}

// UpdateHighlightNote replaces a highlight's note (empty string clears it).
func (s *HighlightService) UpdateHighlightNote(id int64, note string) error {
	return db.UpdateHighlightNote(bg(), s.app.DB.W, id, note)
}

// SetHighlightColor changes a highlight's colour (a palette key).
func (s *HighlightService) SetHighlightColor(id int64, color string) error {
	return db.SetHighlightColor(bg(), s.app.DB.W, id, color)
}

// DeleteHighlight removes a highlight.
func (s *HighlightService) DeleteHighlight(id int64) error {
	return db.DeleteHighlight(bg(), s.app.DB.W, id)
}

// DeleteHighlights removes a batch of highlights (the browser's multi-select
// delete) in one transactional call rather than one IPC round-trip per row.
func (s *HighlightService) DeleteHighlights(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return db.DeleteHighlights(bg(), s.app.DB, ids)
}

// SearchHighlights filters the global highlight list by quote, note, or
// owning-article title. An empty query is equivalent to ListAllHighlights.
func (s *HighlightService) SearchHighlights(query string) ([]models.HighlightWithContext, error) {
	return db.SearchAllHighlights(bg(), s.app.DB.R, strings.TrimSpace(query))
}

// SearchInRange returns every highlight created in [fromISO, toISO) — used
// by the Digest tab's time-range chips.
func (s *HighlightService) SearchInRange(fromISO, toISO string) ([]models.HighlightWithContext, error) {
	return db.SearchAllHighlightsInRange(bg(), s.app.DB.R, fromISO, toISO)
}

// ExportAllMarkdown renders every highlight as one Markdown document,
// grouped by article and dated today. The Wails WKWebView has no download
// handler, so a webview `<a download>` saves nothing; the export is driven
// by a native save panel and the file is written here in Go (mirrors
// OpmlService.ExportOpml). Returns the saved path, or an empty string when
// the user cancels (not an error).
func (s *HighlightService) ExportAllMarkdown() (string, error) {
	rows, err := db.ListAllHighlights(bg(), s.app.DB.R)
	if err != nil {
		return "", apperr.Wrap("highlight", err)
	}
	content := db.BuildHighlightsMarkdown(rows, time.Now().Format("2006-01-02"))
	defaultName := "hive-highlights-" + time.Now().Format("2006-01-02") + ".md"
	path, err := application.Get().Dialog.SaveFile().
		SetFilename(defaultName).
		AddFilter("Markdown", "*.md").
		CanCreateDirectories(true).
		PromptForSingleSelection()
	if err != nil {
		return "", apperr.Wrap("highlightExport", err)
	}
	if path == "" {
		return "", nil // user cancelled — not an error
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", apperr.Wrap("highlightExport", err)
	}
	return path, nil
}

// ExportRangeMarkdown renders every highlight created in [fromISO, toISO) as
// one Markdown document, grouped by article. Mirrors ExportAllMarkdown's
// native-save-dialog pattern (the Wails WKWebView can't drive a download via
// a synthetic <a>). fromISO and toISO are expected as "YYYY-MM-DD..." — only
// their first 10 characters are used for the default filename.
func (s *HighlightService) ExportRangeMarkdown(fromISO, toISO string) (string, error) {
	rows, err := db.SearchAllHighlightsInRange(bg(), s.app.DB.R, fromISO, toISO)
	if err != nil {
		return "", apperr.Wrap("highlight", err)
	}
	content := db.BuildHighlightsMarkdown(rows, time.Now().Format("2006-01-02"))
	fromDate, toDate := fromISO, toISO
	if len(fromDate) >= 10 {
		fromDate = fromDate[:10]
	}
	if len(toDate) >= 10 {
		toDate = toDate[:10]
	}
	defaultName := "hive-highlights-" + fromDate + "-to-" + toDate + ".md"
	path, err := application.Get().Dialog.SaveFile().
		SetFilename(defaultName).
		AddFilter("Markdown", "*.md").
		CanCreateDirectories(true).
		PromptForSingleSelection()
	if err != nil {
		return "", apperr.Wrap("highlightExport", err)
	}
	if path == "" {
		return "", nil // user cancelled — not an error
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", apperr.Wrap("highlightExport", err)
	}
	return path, nil
}
