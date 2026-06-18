package db

import (
	"context"

	"hive/internal/apperr"
	"hive/internal/models"
)

// NewsletterSourceRow is one configured email-newsletter source: the backing
// feed plus its IMAP connection details (without the password). Mirrors the
// commands::NewsletterSource payload.
type NewsletterSourceRow struct {
	FeedID   int64
	Title    string
	Host     string
	Port     uint16
	Username string
	Folder   string
}

// InsertNewsletterSource inserts a newsletter source: a feeds row (source_type =
// 'newsletter') plus its IMAP credentials in newsletter_sources. Both land in
// one transaction so a failure cannot leave a feed with no credentials.
func InsertNewsletterSource(ctx context.Context, db *DB, feedURL, title string, cfg models.NewsletterConfig) (int64, error) {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		"INSERT INTO feeds(feed_url, title, source_type) VALUES (?1, ?2, 'newsletter')",
		feedURL, title)
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	feedID, err := res.LastInsertId()
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO newsletter_sources(feed_id, host, port, username, password, folder)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
		feedID, cfg.Host, cfg.Port, cfg.Username, cfg.Password, cfg.Folder); err != nil {
		return 0, apperr.Wrap("db", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, apperr.Wrap("db", err)
	}
	return feedID, nil
}

// ListNewsletterSources returns every configured newsletter source (without the
// password) for the UI list.
func ListNewsletterSources(ctx context.Context, q Querier) ([]NewsletterSourceRow, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT n.feed_id, f.title, n.host, n.port, n.username, n.folder
		 FROM newsletter_sources n JOIN feeds f ON f.id = n.feed_id
		 ORDER BY f.title COLLATE NOCASE`)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []NewsletterSourceRow{}
	for rows.Next() {
		var r NewsletterSourceRow
		if err := rows.Scan(&r.FeedID, &r.Title, &r.Host, &r.Port, &r.Username, &r.Folder); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// NewsletterToPoll pairs a feed id with its IMAP config.
type NewsletterToPoll struct {
	FeedID int64
	Config models.NewsletterConfig
}

// scanNewsletterPolls runs a poll-list query and collects NewsletterToPoll rows.
func scanNewsletterPolls(ctx context.Context, q Querier, sqlText string, args ...any) ([]NewsletterToPoll, error) {
	rows, err := q.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []NewsletterToPoll{}
	for rows.Next() {
		var n NewsletterToPoll
		if err := rows.Scan(&n.FeedID, &n.Config.Host, &n.Config.Port,
			&n.Config.Username, &n.Config.Password, &n.Config.Folder); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NewsletterSourcesToPoll returns every newsletter source's IMAP config — the
// work list the refresh scheduler polls each cycle.
func NewsletterSourcesToPoll(ctx context.Context, q Querier) ([]NewsletterToPoll, error) {
	return scanNewsletterPolls(ctx, q,
		"SELECT feed_id, host, port, username, password, folder FROM newsletter_sources")
}

// NewsletterSourcesForFeed returns the IMAP config for one newsletter source —
// the per-feed manual refresh. Empty when the id is not a newsletter source.
func NewsletterSourcesForFeed(ctx context.Context, q Querier, feedID int64) ([]NewsletterToPoll, error) {
	return scanNewsletterPolls(ctx, q,
		"SELECT feed_id, host, port, username, password, folder FROM newsletter_sources WHERE feed_id = ?1",
		feedID)
}

// NewsletterSourcesInFolder returns the IMAP configs for the newsletter sources
// in a folder — the per-folder manual refresh.
func NewsletterSourcesInFolder(ctx context.Context, q Querier, folderID int64) ([]NewsletterToPoll, error) {
	return scanNewsletterPolls(ctx, q,
		`SELECT s.feed_id, s.host, s.port, s.username, s.password, s.folder
		 FROM newsletter_sources s JOIN feeds f ON f.id = s.feed_id WHERE f.folder_id = ?1`,
		folderID)
}

// NewsletterSourcesDueToPoll returns newsletter sources that are due to be
// polled, applying the same per-feed/global interval logic as
// FeedsDueForRefresh.
func NewsletterSourcesDueToPoll(ctx context.Context, q Querier, globalMin int64) ([]NewsletterToPoll, error) {
	return scanNewsletterPolls(ctx, q,
		`SELECT s.feed_id, s.host, s.port, s.username, s.password, s.folder
		 FROM newsletter_sources s JOIN feeds f ON f.id = s.feed_id
		 WHERE COALESCE(f.refresh_interval_min, ?1) < ?2
		   AND ( f.last_fetched_at IS NULL
				 OR (julianday('now') - julianday(f.last_fetched_at)) * 1440.0
					>= COALESCE(f.refresh_interval_min, ?1) )`,
		globalMin, RefreshOffMinutes)
}

// DeleteNewsletterSource removes a newsletter source. Deleting the feeds row
// cascades to both newsletter_sources and the source's articles.
func DeleteNewsletterSource(ctx context.Context, q Querier, feedID int64) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM feeds WHERE id = ?1", feedID); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}
