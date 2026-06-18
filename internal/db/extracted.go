package db

import (
	"context"
	"database/sql"
	"strings"

	"hive/internal/apperr"
	"hive/internal/sanitize"
)

// ArticleText returns (title, plain_text) for building an AI prompt. It prefers
// the extracted full text when the user has run extraction, so a summary /
// answer covers the whole article rather than the (often truncated) feed body.
func ArticleText(ctx context.Context, q Querier, id int64) (string, string, error) {
	var (
		title, body string
		extracted   sql.Null[string]
	)
	if err := q.QueryRowContext(ctx,
		"SELECT title, body_text, extracted_html FROM articles WHERE id = ?1", id).
		Scan(&title, &body, &extracted); err != nil {
		return "", "", apperr.Wrap("db", err)
	}
	text := body
	if extracted.Valid && strings.TrimSpace(extracted.V) != "" {
		text = sanitize.HTMLToText(extracted.V)
	}
	return title, text, nil
}

// SetExtractedHTML stores the extracted full-text HTML and re-indexes the
// article's FTS body with it, so search covers the whole article. A lead image
// is adopted only when the article has none. Both writes commit together.
func SetExtractedHTML(ctx context.Context, db *DB, id int64, html string, imageURL *string) error {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap("db", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE articles
			SET extracted_html = ?2,
				image_url = CASE
					WHEN ?3 IS NOT NULL AND (image_url IS NULL OR trim(image_url) = '')
					THEN ?3
					ELSE image_url
				END
		 WHERE id = ?1`,
		id, html, nz(imageURL)); err != nil {
		return apperr.Wrap("db", err)
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE articles_fts SET body = ?2 WHERE rowid = ?1",
		id, sanitize.HTMLToText(html)); err != nil {
		return apperr.Wrap("db", err)
	}
	// The full text supersedes the feed's summary, so any cached translation —
	// produced from that summary — is now stale. Drop them so the next translate
	// runs against the full text.
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM article_translations WHERE article_id = ?1", id); err != nil {
		return apperr.Wrap("db", err)
	}
	if err := tx.Commit(); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// CardImage is an (article id, image URL) pair to adopt as a card thumbnail.
type CardImage struct {
	ID       int64
	ImageURL string
}

// CardImageBackfillScan scans stored articles that have no thumbnail but whose
// feed or extracted body HTML embeds an image, returning the pairs to adopt.
// Reads only — pair with ApplyCardImages so the heavy HTML parse runs on a
// reader connection and only the quick writes take the writer lock.
func CardImageBackfillScan(ctx context.Context, q Querier) ([]CardImage, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, content_html, extracted_html FROM articles
		 WHERE (image_url IS NULL OR trim(image_url) = '')
		   AND (
				(content_html IS NOT NULL AND content_html <> '')
				OR (extracted_html IS NOT NULL AND extracted_html <> '')
		   )`)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []CardImage{}
	for rows.Next() {
		var (
			id                     int64
			contentHTML, extracted sql.Null[string]
		)
		if err := rows.Scan(&id, &contentHTML, &extracted); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		var img *string
		if contentHTML.Valid {
			img = sanitize.FirstImage(contentHTML.V)
		}
		if img == nil && extracted.Valid {
			img = sanitize.FirstImage(extracted.V)
		}
		if img != nil {
			out = append(out, CardImage{ID: id, ImageURL: *img})
		}
	}
	return out, rows.Err()
}

// ApplyCardImages persists the pairs found by CardImageBackfillScan, in a
// single transaction.
func ApplyCardImages(ctx context.Context, db *DB, updates []CardImage) error {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap("db", err)
	}
	defer tx.Rollback()
	for _, u := range updates {
		if _, err := tx.ExecContext(ctx,
			"UPDATE articles SET image_url = ?2 WHERE id = ?1", u.ID, u.ImageURL); err != nil {
			return apperr.Wrap("db", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}
