package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"hive/internal/apperr"
	"hive/internal/models"
)

const highlightCols = "id, article_id, quote, prefix, suffix, text_offset, color, note, created_at"

// NewHighlight is the input for creating a highlight — everything in Highlight
// except the database-assigned id and created_at.
type NewHighlight struct {
	ArticleID  int64
	Quote      string
	Prefix     string
	Suffix     string
	TextOffset int64
	Color      string
	Note       string
}

func scanHighlight(s interface {
	Scan(dest ...any) error
}) (models.Highlight, error) {
	var h models.Highlight
	err := s.Scan(&h.ID, &h.ArticleID, &h.Quote, &h.Prefix, &h.Suffix,
		&h.TextOffset, &h.Color, &h.Note, &h.CreatedAt)
	return h, err
}

// InsertHighlight inserts a highlight and returns its new id.
func InsertHighlight(ctx context.Context, q Querier, h NewHighlight) (int64, error) {
	res, err := q.ExecContext(ctx,
		`INSERT INTO highlights(article_id, quote, prefix, suffix, text_offset, color, note)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)`,
		h.ArticleID, h.Quote, h.Prefix, h.Suffix, h.TextOffset, h.Color, h.Note)
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	return res.LastInsertId()
}

// ListHighlights returns all highlights for one article, oldest first.
func ListHighlights(ctx context.Context, q Querier, articleID int64) ([]models.Highlight, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT "+highlightCols+" FROM highlights WHERE article_id = ?1 ORDER BY text_offset, id",
		articleID)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []models.Highlight{}
	for rows.Next() {
		h, err := scanHighlight(rows)
		if err != nil {
			return nil, apperr.Wrap("db", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// highlightWithCtxCols is the JOIN projection used by the global highlights
// browser and the Markdown exporter. The h.* columns mirror highlightCols.
const highlightWithCtxCols = "h.id, h.article_id, h.quote, h.prefix, h.suffix, " +
	"h.text_offset, h.color, h.note, h.created_at, " +
	"a.url, a.title, f.title, f.favicon_url"

func scanHighlightWithContext(s interface {
	Scan(dest ...any) error
}) (models.HighlightWithContext, error) {
	var (
		hc                  models.HighlightWithContext
		articleURL, favicon sql.Null[string]
	)
	err := s.Scan(
		&hc.ID, &hc.ArticleID, &hc.Quote, &hc.Prefix, &hc.Suffix,
		&hc.TextOffset, &hc.Color, &hc.Note, &hc.CreatedAt,
		&articleURL, &hc.ArticleTitle, &hc.FeedTitle, &favicon,
	)
	hc.ArticleURL = nullPtr(articleURL)
	hc.FaviconURL = nullPtr(favicon)
	return hc, err
}

// ListAllHighlights returns every highlight enriched with the article and feed
// it belongs to, newest first. Used by the global Highlights browser.
func ListAllHighlights(ctx context.Context, q Querier) ([]models.HighlightWithContext, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT "+highlightWithCtxCols+
			" FROM highlights h"+
			" JOIN articles a ON a.id = h.article_id"+
			" JOIN feeds    f ON f.id = a.feed_id"+
			" ORDER BY h.created_at DESC, h.id DESC")
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []models.HighlightWithContext{}
	for rows.Next() {
		hc, err := scanHighlightWithContext(rows)
		if err != nil {
			return nil, apperr.Wrap("db", err)
		}
		out = append(out, hc)
	}
	return out, rows.Err()
}

// escapeLikeForHighlights escapes the three SQL LIKE wildcards (% _ \) so user
// input matches literally. The query is paired with `LIKE ?1 ESCAPE '\\'`.
func escapeLikeForHighlights(q string) string {
	r := make([]byte, 0, len(q)+4)
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c == '\\' || c == '%' || c == '_' {
			r = append(r, '\\')
		}
		r = append(r, c)
	}
	return string(r)
}

// SearchAllHighlights returns every highlight whose quote, note, or owning
// article title matches the query (case-insensitive substring). An empty query
// returns the full list, identical to ListAllHighlights.
func SearchAllHighlights(ctx context.Context, q Querier, query string) ([]models.HighlightWithContext, error) {
	if query == "" {
		return ListAllHighlights(ctx, q)
	}
	pat := "%" + escapeLikeForHighlights(query) + "%"
	rows, err := q.QueryContext(ctx,
		"SELECT "+highlightWithCtxCols+
			" FROM highlights h"+
			" JOIN articles a ON a.id = h.article_id"+
			" JOIN feeds    f ON f.id = a.feed_id"+
			" WHERE h.quote LIKE ?1 ESCAPE '\\'"+
			" OR h.note  LIKE ?1 ESCAPE '\\'"+
			" OR a.title LIKE ?1 ESCAPE '\\'"+
			" ORDER BY h.created_at DESC, h.id DESC", pat)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []models.HighlightWithContext{}
	for rows.Next() {
		hc, err := scanHighlightWithContext(rows)
		if err != nil {
			return nil, apperr.Wrap("db", err)
		}
		out = append(out, hc)
	}
	return out, rows.Err()
}

// SearchAllHighlightsInRange returns every highlight created in
// [fromISO, toISO) — inclusive of from, exclusive of to — newest first.
// Used by the Digest tab's time-range chips and by ExportRangeMarkdown.
func SearchAllHighlightsInRange(ctx context.Context, q Querier, fromISO, toISO string) ([]models.HighlightWithContext, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT "+highlightWithCtxCols+
			" FROM highlights h"+
			" JOIN articles a ON a.id = h.article_id"+
			" JOIN feeds    f ON f.id = a.feed_id"+
			" WHERE h.created_at >= ?1 AND h.created_at < ?2"+
			" ORDER BY h.created_at DESC, h.id DESC",
		fromISO, toISO)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []models.HighlightWithContext{}
	for rows.Next() {
		hc, err := scanHighlightWithContext(rows)
		if err != nil {
			return nil, apperr.Wrap("db", err)
		}
		out = append(out, hc)
	}
	return out, rows.Err()
}

// GetHighlight fetches one highlight by id, if it exists.
func GetHighlight(ctx context.Context, q Querier, id int64) (*models.Highlight, error) {
	h, err := scanHighlight(q.QueryRowContext(ctx,
		"SELECT "+highlightCols+" FROM highlights WHERE id = ?1", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	return &h, nil
}

// UpdateHighlightNote replaces a highlight's note text (an empty string clears it).
func UpdateHighlightNote(ctx context.Context, q Querier, id int64, note string) error {
	if _, err := q.ExecContext(ctx, "UPDATE highlights SET note = ?2 WHERE id = ?1", id, note); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// SetHighlightColor changes a highlight's colour (a palette key).
func SetHighlightColor(ctx context.Context, q Querier, id int64, color string) error {
	if _, err := q.ExecContext(ctx, "UPDATE highlights SET color = ?2 WHERE id = ?1", id, color); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// DeleteHighlight removes a highlight.
func DeleteHighlight(ctx context.Context, q Querier, id int64) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM highlights WHERE id = ?1", id); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// DeleteHighlights removes a batch of highlights in chunked IN-clause deletes,
// all inside one transaction: a mid-batch failure deletes nothing rather than
// an unknown prefix (the frontend has already cleared its selection by commit
// time, so a partial delete would be unretryable and silently divergent).
// Chunking keeps the bind-variable count well under SQLite's limit.
func DeleteHighlights(ctx context.Context, d *DB, ids []int64) error {
	tx, err := d.W.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap("db", err)
	}
	defer tx.Rollback()

	const chunk = 500
	for start := 0; start < len(ids); start += chunk {
		end := min(start+chunk, len(ids))
		part := ids[start:end]
		ph := make([]string, len(part))
		args := make([]any, len(part))
		for i, id := range part {
			ph[i] = "?"
			args[i] = id
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM highlights WHERE id IN ("+strings.Join(ph, ",")+")", args...); err != nil {
			return apperr.Wrap("db", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}
