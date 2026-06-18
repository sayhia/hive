package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"hive/internal/apperr"
	"hive/internal/models"
)

// RefreshOffMinutes is the "never auto-refresh" sentinel (minutes ≈ one year).
// The Settings panel writes it as the global interval when auto-refresh is
// switched off, and a per-feed interval can carry it to opt one feed out.
const RefreshOffMinutes int64 = 525_600

// FeedToRefresh is a feed the scheduler should fetch: the stored revalidators
// (etag, last-modified) drive a conditional GET.
type FeedToRefresh struct {
	ID           int64
	FeedURL      string
	ETag         *string
	LastModified *string
}

// FindFeedByURL returns the id of a feed with the given feed_url, or nil.
func FindFeedByURL(ctx context.Context, q Querier, url string) (*int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, "SELECT id FROM feeds WHERE feed_url = ?1", url).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	return &id, nil
}

// InsertFeed inserts a new feed row and returns its id.
func InsertFeed(ctx context.Context, q Querier, feedURL string, siteURL *string, title string, description *string, sourceType models.SourceType, folderID *int64) (int64, error) {
	res, err := q.ExecContext(ctx,
		`INSERT INTO feeds(feed_url, site_url, title, description, source_type, folder_id)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
		feedURL, nz(siteURL), title, nz(description), sourceType, nz(folderID))
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	return res.LastInsertId()
}

// RefineFeedSourceType promotes a feed's source_type once its real kind is
// known from the parsed document — but only when it is still the generic 'rss'.
// The WHERE source_type = 'rss' guard makes this strictly a promotion.
func RefineFeedSourceType(ctx context.Context, q Querier, id int64, sourceType models.SourceType) error {
	if sourceType == models.SourceRSS {
		return nil
	}
	if _, err := q.ExecContext(ctx,
		"UPDATE feeds SET source_type = ?2 WHERE id = ?1 AND source_type = 'rss'",
		id, sourceType); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// ListFeeds returns every feed with a live unread count, ordered by title.
func ListFeeds(ctx context.Context, q Querier) ([]models.Feed, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT f.id, f.feed_url, f.site_url, f.title, f.description, f.favicon_url,
				f.folder_id, f.source_type, f.last_fetched_at, f.fetch_error,
				(SELECT COUNT(*) FROM articles a WHERE a.feed_id = f.id AND a.is_read = 0),
				f.refresh_interval_min, f.auto_translate, f.open_mode
		 FROM feeds f ORDER BY f.title COLLATE NOCASE`)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []models.Feed{}
	for rows.Next() {
		var (
			f                                          models.Feed
			siteURL, description, favicon, lastFetched sql.Null[string]
			fetchErr, openMode                         sql.Null[string]
			folderID, interval, autoTr                 sql.Null[int64]
		)
		if err := rows.Scan(&f.ID, &f.FeedURL, &siteURL, &f.Title, &description, &favicon,
			&folderID, &f.SourceType, &lastFetched, &fetchErr, &f.UnreadCount, &interval, &autoTr, &openMode); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		f.SiteURL = nullPtr(siteURL)
		f.Description = nullPtr(description)
		f.FaviconURL = nullPtr(favicon)
		f.FolderID = nullPtr(folderID)
		f.LastFetchedAt = nullPtr(lastFetched)
		f.FetchError = nullPtr(fetchErr)
		f.RefreshIntervalMin = nullPtr(interval)
		f.AutoTranslate = nullPtr(autoTr)
		f.OpenMode = nullPtr(openMode)
		out = append(out, f)
	}
	return out, rows.Err()
}

// scanFeedsToRefresh runs a feed-refresh query and collects FeedToRefresh rows.
func scanFeedsToRefresh(ctx context.Context, q Querier, sqlText string, args ...any) ([]FeedToRefresh, error) {
	rows, err := q.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []FeedToRefresh{}
	for rows.Next() {
		var (
			f                  FeedToRefresh
			etag, lastModified sql.Null[string]
		)
		if err := rows.Scan(&f.ID, &f.FeedURL, &etag, &lastModified); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		f.ETag = nullPtr(etag)
		f.LastModified = nullPtr(lastModified)
		out = append(out, f)
	}
	return out, rows.Err()
}

// FeedsToRefresh returns all feeds that need an HTTP fetch. Newsletter sources
// are excluded — they are polled over IMAP separately.
func FeedsToRefresh(ctx context.Context, q Querier) ([]FeedToRefresh, error) {
	return scanFeedsToRefresh(ctx, q,
		"SELECT id, feed_url, etag, last_modified FROM feeds WHERE source_type != 'newsletter'")
}

// FeedsToRefreshForFeed returns the single non-newsletter feed by id — the
// per-feed manual refresh. Empty if the id is unknown or names a newsletter
// source (polled over IMAP, not fetched here).
func FeedsToRefreshForFeed(ctx context.Context, q Querier, feedID int64) ([]FeedToRefresh, error) {
	return scanFeedsToRefresh(ctx, q,
		"SELECT id, feed_url, etag, last_modified FROM feeds WHERE source_type != 'newsletter' AND id = ?1",
		feedID)
}

// FeedsToRefreshInFolder returns the non-newsletter feeds in a folder — the
// per-folder manual refresh.
func FeedsToRefreshInFolder(ctx context.Context, q Querier, folderID int64) ([]FeedToRefresh, error) {
	return scanFeedsToRefresh(ctx, q,
		"SELECT id, feed_url, etag, last_modified FROM feeds WHERE source_type != 'newsletter' AND folder_id = ?1",
		folderID)
}

// FeedsDueForRefresh returns non-newsletter feeds whose effective interval (the
// per-feed refresh_interval_min, or globalMin when unset) has elapsed since
// last_fetched_at. Feeds whose effective interval is the "off" sentinel are
// excluded. A never-fetched feed is always due.
func FeedsDueForRefresh(ctx context.Context, q Querier, globalMin int64) ([]FeedToRefresh, error) {
	return scanFeedsToRefresh(ctx, q,
		`SELECT id, feed_url, etag, last_modified FROM feeds
		 WHERE source_type != 'newsletter'
		   AND COALESCE(refresh_interval_min, ?1) < ?2
		   AND ( last_fetched_at IS NULL
				 OR (julianday('now') - julianday(last_fetched_at)) * 1440.0
					>= COALESCE(refresh_interval_min, ?1) )`,
		globalMin, RefreshOffMinutes)
}

// SetFeedRefreshInterval sets (or clears) a feed's per-feed refresh interval.
// nil reverts the feed to the global interval; RefreshOffMinutes opts it out.
func SetFeedRefreshInterval(ctx context.Context, q Querier, id int64, minutes *int64) error {
	if _, err := q.ExecContext(ctx,
		"UPDATE feeds SET refresh_interval_min = ?2 WHERE id = ?1", id, nz(minutes)); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// SetFeedAutoTranslate sets (or clears) a feed's auto-translate override. nil
// reverts the feed to the global translate_auto setting; 0 forces off, 1 on.
func SetFeedAutoTranslate(ctx context.Context, q Querier, id int64, mode *int64) error {
	if _, err := q.ExecContext(ctx,
		"UPDATE feeds SET auto_translate = ?2 WHERE id = ?1", id, nz(mode)); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// SetFeedOpenMode sets (or clears) a feed's per-feed open mode: "reader",
// "extracted" or "web". nil reverts the feed to the global default.
func SetFeedOpenMode(ctx context.Context, q Querier, id int64, mode *string) error {
	if _, err := q.ExecContext(ctx,
		"UPDATE feeds SET open_mode = ?2 WHERE id = ?1", id, nz(mode)); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// UpdateFeedMeta refreshes a feed's metadata from its parsed document. A nil or
// empty field leaves the stored value untouched (NULLIF(?, ”)). The feed title
// is applied only when the user has not renamed the feed by hand (custom_title
// = 0); otherwise this would revert a manual rename on the next poll.
func UpdateFeedMeta(ctx context.Context, q Querier, id int64, title, siteURL, description, faviconURL *string) error {
	if _, err := q.ExecContext(ctx,
		`UPDATE feeds SET
			title       = CASE WHEN custom_title = 1 THEN title
							   ELSE COALESCE(NULLIF(?2, ''), title) END,
			site_url    = COALESCE(NULLIF(?3, ''), site_url),
			description = COALESCE(NULLIF(?4, ''), description),
			favicon_url = COALESCE(NULLIF(?5, ''), favicon_url)
		 WHERE id = ?1`,
		id, nz(title), nz(siteURL), nz(description), nz(faviconURL)); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// SetFeedFetchState records the result of a fetch: new revalidators, an
// optional error, and a fresh last_fetched_at timestamp.
func SetFeedFetchState(ctx context.Context, q Querier, id int64, etag, lastModified, fetchErr *string) error {
	if _, err := q.ExecContext(ctx,
		`UPDATE feeds SET etag = ?2, last_modified = ?3, fetch_error = ?4,
						  last_fetched_at = datetime('now')
		 WHERE id = ?1`,
		id, nz(etag), nz(lastModified), nz(fetchErr)); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// TouchFeed records a successful fetch that produced no changes (304).
func TouchFeed(ctx context.Context, q Querier, id int64) error {
	if _, err := q.ExecContext(ctx,
		"UPDATE feeds SET last_fetched_at = datetime('now'), fetch_error = NULL WHERE id = ?1",
		id); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// FeedLastFetched returns a feed's last_fetched_at timestamp, if ever fetched.
func FeedLastFetched(ctx context.Context, q Querier, id int64) (*string, error) {
	var ts sql.Null[string]
	err := q.QueryRowContext(ctx, "SELECT last_fetched_at FROM feeds WHERE id = ?1", id).Scan(&ts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	return nullPtr(ts), nil
}

// SetFeedError records a failed fetch, keeping the previous content untouched.
func SetFeedError(ctx context.Context, q Querier, id int64, fetchErr string) error {
	if _, err := q.ExecContext(ctx,
		"UPDATE feeds SET last_fetched_at = datetime('now'), fetch_error = ?2 WHERE id = ?1",
		id, fetchErr); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// DeleteFeed removes a feed; its articles and enclosures cascade.
func DeleteFeed(ctx context.Context, q Querier, id int64) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM feeds WHERE id = ?1", id); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// DeleteFeeds removes many feeds in one transaction — each feed's articles and
// enclosures cascade, like DeleteFeed. Chunked to stay under SQLite's
// bound-parameter limit. Mirrors DeleteHighlights; for the settings batch
// unsubscribe.
func DeleteFeeds(ctx context.Context, d *DB, ids []int64) error {
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
			"DELETE FROM feeds WHERE id IN ("+strings.Join(ph, ",")+")", args...); err != nil {
			return apperr.Wrap("db", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// ExportFeed is one row of OPML export: title, feed URL and folder name (if any).
type ExportFeed struct {
	Title   string
	FeedURL string
	Folder  *string
}

// FeedsForExport returns feeds for OPML export. Newsletter sources are excluded:
// their synthetic imap:// feed_url is not an RSS subscription.
func FeedsForExport(ctx context.Context, q Querier) ([]ExportFeed, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT f.title, f.feed_url, fo.name
		 FROM feeds f LEFT JOIN folders fo ON fo.id = f.folder_id
		 WHERE f.source_type != 'newsletter'
		 ORDER BY fo.name, f.title`)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []ExportFeed{}
	for rows.Next() {
		var (
			e      ExportFeed
			folder sql.Null[string]
		)
		if err := rows.Scan(&e.Title, &e.FeedURL, &folder); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		e.Folder = nullPtr(folder)
		out = append(out, e)
	}
	return out, rows.Err()
}

// FeedURLsForSync returns feed URLs the sync layer should mirror onto the
// server. Excludes newsletter sources.
func FeedURLsForSync(ctx context.Context, q Querier) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT feed_url FROM feeds WHERE source_type != 'newsletter' AND feed_url <> ''")
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// MoveFeed reassigns a feed to a folder (nil = no folder).
func MoveFeed(ctx context.Context, q Querier, id int64, folderID *int64) error {
	if _, err := q.ExecContext(ctx,
		"UPDATE feeds SET folder_id = ?2 WHERE id = ?1", id, nz(folderID)); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// FeedFolderID returns the folder a feed currently sits in, if any.
func FeedFolderID(ctx context.Context, q Querier, id int64) (*int64, error) {
	var folderID sql.Null[int64]
	err := q.QueryRowContext(ctx, "SELECT folder_id FROM feeds WHERE id = ?1", id).Scan(&folderID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	return nullPtr(folderID), nil
}

// RenameFeed sets a feed's display title to a user-chosen value and raises
// custom_title so a later refresh's UpdateFeedMeta does not revert the rename.
func RenameFeed(ctx context.Context, q Querier, id int64, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return apperr.Code("emptyFeedTitle")
	}
	if _, err := q.ExecContext(ctx,
		"UPDATE feeds SET title = ?2, custom_title = 1 WHERE id = ?1", id, title); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// SetFeedURL repoints a feed at a new URL (a site moved its feed) while keeping
// its articles, folder and per-feed settings. The conditional-GET validators
// belong to the old URL, so etag / last_modified are cleared along with any
// stale fetch error, and last_fetched_at is reset so the scheduler refetches it
// promptly. feed_url is UNIQUE — repointing onto a URL another feed already has
// returns "alreadySubscribed" rather than surfacing a raw constraint failure.
func SetFeedURL(ctx context.Context, q Querier, id int64, url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return apperr.Code("emptyFeedUrl")
	}
	var n int64
	if err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM feeds WHERE feed_url = ?1 AND id <> ?2", url, id).Scan(&n); err != nil {
		return apperr.Wrap("db", err)
	}
	if n > 0 {
		return apperr.Code("alreadySubscribed")
	}
	if _, err := q.ExecContext(ctx,
		`UPDATE feeds SET feed_url = ?2, etag = NULL, last_modified = NULL,
		                  fetch_error = NULL, last_fetched_at = NULL
		 WHERE id = ?1`, id, url); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}
