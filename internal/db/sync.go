package db

import (
	"context"
	"database/sql"
	"errors"

	"hive/internal/apperr"
)

// ArticleIDByURL returns the local article id for a given source URL — used to
// reconcile remote state. nil when no article matches.
func ArticleIDByURL(ctx context.Context, q Querier, url string) (*int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, "SELECT id FROM articles WHERE url = ?1 LIMIT 1", url).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	return &id, nil
}

// SetRemoteID records the sync server's item id for a local article.
func SetRemoteID(ctx context.Context, q Querier, articleID int64, remoteID string) error {
	if _, err := q.ExecContext(ctx,
		"UPDATE articles SET remote_id = ?2 WHERE id = ?1", articleID, remoteID); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// SetSyncState applies remote read/starred state to a local article.
func SetSyncState(ctx context.Context, q Querier, articleID int64, read, starred bool) error {
	if _, err := q.ExecContext(ctx,
		"UPDATE articles SET is_read = ?2, is_starred = ?3 WHERE id = ?1",
		articleID, read, starred); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// FeedIDsByURLs resolves a set of feed URLs to the ids of the local feeds that
// carry them, skipping any URL the local DB doesn't track. Used to scope a sync
// reconciliation to the feeds the server actually knows about.
func FeedIDsByURLs(ctx context.Context, q Querier, urls map[string]struct{}) ([]int64, error) {
	ids := make([]int64, 0, len(urls))
	for u := range urls {
		id, err := FindFeedByURL(ctx, q, u)
		if err != nil {
			return nil, err
		}
		if id != nil {
			ids = append(ids, *id)
		}
	}
	return ids, nil
}

// ReconcileSyncState reconciles local read/starred state against the server's
// authoritative unread + starred URL sets, for every article under one of
// feedIDs. An article is marked read unless its URL is in unreadURLs, and
// starred iff its URL is in starredURLs — so the server's read state wins even
// for the long tail of items a pull of only recent items never enumerates.
//
// Articles carrying an un-pushed local edit (in sync_queue) are skipped, so a
// pull never clobbers a change still waiting to be sent. Only rows whose state
// actually changes are written. Returns the number of articles changed.
func ReconcileSyncState(ctx context.Context, db *DB, feedIDs []int64, unreadURLs, starredURLs map[string]struct{}) (int, error) {
	pendingIDs, err := PendingSyncArticleIDs(ctx, db.R)
	if err != nil {
		return 0, err
	}
	pending := make(map[int64]struct{}, len(pendingIDs))
	for _, id := range pendingIDs {
		pending[id] = struct{}{}
	}

	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	defer tx.Rollback()

	type change struct {
		id            int64
		read, starred bool
	}
	var changed []change
	for _, fid := range feedIDs {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, url, is_read, is_starred FROM articles
			 WHERE feed_id = ?1 AND url IS NOT NULL`, fid)
		if err != nil {
			return 0, apperr.Wrap("db", err)
		}
		for rows.Next() {
			var (
				id                  int64
				url                 string
				curRead, curStarred bool
			)
			if err := rows.Scan(&id, &url, &curRead, &curStarred); err != nil {
				rows.Close()
				return 0, apperr.Wrap("db", err)
			}
			if _, isPending := pending[id]; isPending {
				continue
			}
			_, unread := unreadURLs[url]
			_, starred := starredURLs[url]
			read := !unread
			if read != curRead || starred != curStarred {
				changed = append(changed, change{id: id, read: read, starred: starred})
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return 0, apperr.Wrap("db", err)
		}
	}
	for _, c := range changed {
		if _, err := tx.ExecContext(ctx,
			"UPDATE articles SET is_read = ?2, is_starred = ?3 WHERE id = ?1",
			c.id, c.read, c.starred); err != nil {
			return 0, apperr.Wrap("db", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, apperr.Wrap("db", err)
	}
	return len(changed), nil
}

// EnqueueSync queues a local read/starred change to push on the next sync.
func EnqueueSync(ctx context.Context, q Querier, articleID int64, field string, value bool) error {
	if _, err := q.ExecContext(ctx,
		`INSERT INTO sync_queue(article_id, field, value) VALUES (?1, ?2, ?3)
		 ON CONFLICT(article_id, field) DO UPDATE SET value = excluded.value`,
		articleID, field, value); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// PendingSyncArticleIDs returns article ids that still carry un-pushed local
// changes — their state must not be overwritten by a pull until sent.
func PendingSyncArticleIDs(ctx context.Context, q Querier) ([]int64, error) {
	rows, err := q.QueryContext(ctx, "SELECT DISTINCT article_id FROM sync_queue")
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SyncEntry is one pushable change drained from the sync queue.
type SyncEntry struct {
	ArticleID int64
	RemoteID  string
	Field     string
	Value     bool
}

// TakeSyncQueue drains pushable queue entries. Only rows whose article already
// has a remote id are returned and removed; the rest wait for a pull to assign
// one. The caller MUST re-queue any entry whose push fails (see RequeueSync).
func TakeSyncQueue(ctx context.Context, db *DB) ([]SyncEntry, error) {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`SELECT q.article_id, a.remote_id, q.field, q.value
		 FROM sync_queue q JOIN articles a ON a.id = q.article_id
		 WHERE a.remote_id IS NOT NULL`)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	out := []SyncEntry{}
	for rows.Next() {
		var (
			e   SyncEntry
			val int64
		)
		if err := rows.Scan(&e.ArticleID, &e.RemoteID, &e.Field, &val); err != nil {
			rows.Close()
			return nil, apperr.Wrap("db", err)
		}
		e.Value = val != 0
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap("db", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sync_queue WHERE article_id IN
			(SELECT id FROM articles WHERE remote_id IS NOT NULL)`); err != nil {
		return nil, apperr.Wrap("db", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, apperr.Wrap("db", err)
	}
	return out, nil
}

// RequeueSync re-inserts a queue entry whose push failed. Unlike EnqueueSync it
// does not clobber a newer edit the user made on the same article during sync.
func RequeueSync(ctx context.Context, q Querier, articleID int64, field string, value bool) error {
	if _, err := q.ExecContext(ctx,
		`INSERT INTO sync_queue(article_id, field, value) VALUES (?1, ?2, ?3)
		 ON CONFLICT(article_id, field) DO NOTHING`,
		articleID, field, value); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}
