package db

import (
	"context"
	"fmt"

	"hive/internal/apperr"
	"hive/internal/models"
)

// SmartCounts returns the (unread, starred, read-later) counts for the sidebar
// smart folders.
func SmartCounts(ctx context.Context, q Querier) (models.SmartCounts, error) {
	var c models.SmartCounts
	err := q.QueryRowContext(ctx,
		`SELECT
			(SELECT COUNT(*) FROM articles WHERE is_read = 0),
			(SELECT COUNT(*) FROM articles WHERE is_starred = 1),
			(SELECT COUNT(*) FROM articles WHERE read_later = 1)`).
		Scan(&c.Unread, &c.Starred, &c.ReadLater)
	if err != nil {
		return c, apperr.Wrap("db", err)
	}
	return c, nil
}

// Stats is (database bytes, article count, feed count) for the storage panel.
type Stats struct {
	DBBytes      int64
	ArticleCount int64
	FeedCount    int64
}

// StorageStats returns database size and content counts for the storage panel.
func StorageStats(ctx context.Context, q Querier) (Stats, error) {
	var s Stats
	var pageCount, pageSize int64
	if err := q.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err != nil {
		return s, apperr.Wrap("db", err)
	}
	if err := q.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return s, apperr.Wrap("db", err)
	}
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM articles").Scan(&s.ArticleCount); err != nil {
		return s, apperr.Wrap("db", err)
	}
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM feeds").Scan(&s.FeedCount); err != nil {
		return s, apperr.Wrap("db", err)
	}
	s.DBBytes = pageCount * pageSize
	return s, nil
}

// purgeWhere is the retention purge condition — shared verbatim by the
// tombstone INSERT and the DELETE in CleanupOldArticles so the two select
// exactly the same rows.
const purgeWhere = `is_starred = 0 AND read_later = 0 AND is_read = 1
	   AND NOT EXISTS (SELECT 1 FROM highlights WHERE article_id = articles.id)
	   AND datetime(COALESCE(published_at, fetched_at))
		   < datetime('now', ?1)`

// CleanupOldArticles deletes read articles older than days, keeping starred,
// read-later, and highlighted ones. Returns the number removed. Age is the
// effective date — COALESCE(published_at, fetched_at), wrapped in datetime() so
// the two stored timestamp formats compare correctly. A non-positive days is a
// no-op (it would otherwise purge everything or nothing).
//
// Every purged row is tombstoned first, in the same transaction, so a
// full-archive feed that still carries the item can't re-insert it as fresh
// unread on the next refresh. INSERT OR IGNORE because a guid purged once
// already has its tombstone.
func CleanupOldArticles(ctx context.Context, db *DB, days int64) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	modifier := fmt.Sprintf("-%d days", days)
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO article_tombstones(feed_id, guid)
		 SELECT feed_id, guid FROM articles WHERE `+purgeWhere, modifier); err != nil {
		return 0, apperr.Wrap("db", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM articles WHERE `+purgeWhere, modifier)
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, apperr.Wrap("db", err)
	}
	return removed, nil
}

// Vacuum reclaims free pages — must run outside any transaction.
func Vacuum(ctx context.Context, q Querier) error {
	if _, err := q.ExecContext(ctx, "VACUUM"); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// ClearAllData wipes all user content (feeds → articles cascade, folders).
// Settings are kept. Both deletes commit together.
func ClearAllData(ctx context.Context, db *DB) error {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap("db", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM feeds"); err != nil {
		return apperr.Wrap("db", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM folders"); err != nil {
		return apperr.Wrap("db", err)
	}
	if err := tx.Commit(); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// ResetSettings clears every stored setting.
func ResetSettings(ctx context.Context, q Querier) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM settings"); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// CountUnread returns the total number of unread articles.
func CountUnread(ctx context.Context, q Querier) (int64, error) {
	var n int64
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM articles WHERE is_read = 0").Scan(&n); err != nil {
		return 0, apperr.Wrap("db", err)
	}
	return n, nil
}

// CountFeedUnread returns the unread article count for a single feed.
func CountFeedUnread(ctx context.Context, q Querier, feedID int64) (int64, error) {
	var n int64
	if err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM articles WHERE feed_id = ?1 AND is_read = 0", feedID).Scan(&n); err != nil {
		return 0, apperr.Wrap("db", err)
	}
	return n, nil
}

// LatestFetch returns the timestamp of the most recent successful feed fetch.
func LatestFetch(ctx context.Context, q Querier) (*string, error) {
	var ts *string
	var raw any
	if err := q.QueryRowContext(ctx, "SELECT MAX(last_fetched_at) FROM feeds").Scan(&raw); err != nil {
		return nil, apperr.Wrap("db", err)
	}
	switch v := raw.(type) {
	case string:
		ts = &v
	case []byte:
		s := string(v)
		ts = &s
	}
	return ts, nil
}
