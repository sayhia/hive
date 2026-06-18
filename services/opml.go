package services

import (
	"context"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"

	"hive/internal/apperr"
	"hive/internal/appstate"
	"hive/internal/db"
	"hive/internal/events"
	"hive/internal/ingestion"
	"hive/internal/opml"
)

// OpmlService exposes OPML import / export.
type OpmlService struct {
	app *appstate.State
}

// ImportOpml imports subscriptions from OPML content, creating folders as
// needed, and returns the number of feeds added. The whole import runs in one
// transaction so a mid-list failure rolls back rather than leaving feeds
// half-imported. Newly imported feeds have no articles until the next refresh.
func (s *OpmlService) ImportOpml(content string) (int64, error) {
	ctx := bg()
	entries, err := opml.Parse(content)
	if err != nil {
		return 0, err
	}
	tx, err := s.app.DB.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	defer tx.Rollback()

	var added int64
	for _, e := range entries {
		if existing, err := db.FindFeedByURL(ctx, tx, e.XMLURL); err != nil {
			return 0, err
		} else if existing != nil {
			continue
		}
		var folderID *int64
		if e.Folder != nil {
			id, err := db.FolderIDByName(ctx, tx, *e.Folder)
			if err != nil {
				return 0, err
			}
			folderID = &id
		}
		sourceType := ingestion.DetectSourceType(e.XMLURL)
		if _, err := db.InsertFeed(ctx, tx, e.XMLURL, nil, e.Title, nil, sourceType, folderID); err != nil {
			return 0, err
		}
		added++
	}
	if err := tx.Commit(); err != nil {
		return 0, apperr.Wrap("db", err)
	}
	events.Emit("feeds-updated", 0)
	// Newly imported feeds have no articles yet — kick off a refresh in the
	// background. waitIfBusy queues it behind any in-flight refresh so the
	// imported feeds still get fetched rather than being skipped.
	go func() {
		_, _ = ingestion.RefreshAll(context.Background(), s.app, nil, true, ingestion.ScopeAll, 0)
	}()
	return added, nil
}

// ExportOpml exports all (non-newsletter) subscriptions as OPML. The Wails
// WKWebView has no download handler, so a webview `<a download>` saves nothing;
// the export is driven by a native save panel and the file is written here in
// Go. Returns the saved path, or an empty string when the user cancels (not an
// error). Mark-all and other callers ignore the path; the UI just confirms.
func (s *OpmlService) ExportOpml() (string, error) {
	feeds, err := db.FeedsForExport(bg(), s.app.DB.R)
	if err != nil {
		return "", err
	}
	content, err := opml.Build(feeds)
	if err != nil {
		return "", err
	}
	path, err := application.Get().Dialog.SaveFile().
		SetFilename("subscriptions.opml").
		AddFilter("OPML", "*.opml").
		CanCreateDirectories(true).
		PromptForSingleSelection()
	if err != nil {
		return "", apperr.Wrap("opmlExport", err)
	}
	if path == "" {
		return "", nil // user cancelled — not an error
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", apperr.Wrap("opmlExport", err)
	}
	return path, nil
}
