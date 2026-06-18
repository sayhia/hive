package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"

	"hive/internal/apperr"
	"hive/internal/models"
)

// NewArticle is a parsed article ready for insertion.
type NewArticle struct {
	GUID string
	// SyntheticGUID marks a guid the parser composed from title + publish
	// date because the feed provided none. Upsert then also treats a
	// same-feed URL match as the same article, so rows keyed off the URL
	// before the composite key existed don't re-ingest as new.
	SyntheticGUID bool
	URL           *string
	Title         string
	Author        *string
	Summary       *string
	ContentHTML   *string
	BodyText      string
	ImageURL      *string
	PublishedAt   *string
	Enclosures    []models.Enclosure
}

// ruleMatches reports whether rule (scoped to feedID) matches the incoming
// article a. The query is a comma-separated keyword list; any substring hit
// fires it. The "any" field checks each field independently (mirroring
// preview_rule's per-column LIKE) so a keyword cannot straddle a field boundary.
func ruleMatches(rule models.Rule, feedID int64, a *NewArticle) bool {
	if rule.FeedID != nil && *rule.FeedID != feedID {
		return false
	}
	author := deref(a.Author)
	var fields []string
	switch rule.Field {
	case "author":
		fields = []string{strings.ToLower(author)}
	case "content":
		fields = []string{strings.ToLower(a.BodyText)}
	case "any":
		fields = []string{
			strings.ToLower(a.Title),
			strings.ToLower(author),
			strings.ToLower(a.BodyText),
		}
	default:
		fields = []string{strings.ToLower(a.Title)}
	}
	for _, term := range strings.Split(rule.Query, ",") {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" {
			continue
		}
		for _, h := range fields {
			if strings.Contains(h, term) {
				return true
			}
		}
	}
	return false
}

// UpsertArticle inserts an article if it is new (by feed_id + guid). It returns
// true only when a genuinely new and unread article was inserted — callers
// tally this as the count of fresh articles surfaced to the user.
//
// When dedup is on, an article whose URL already exists (in any feed) is
// skipped. Enabled rules are evaluated first: a skip match drops the article
// entirely, while read / star matches pre-set the article's state on insert. An
// article inserted but pre-marked read by a read rule returns false.
func UpsertArticle(ctx context.Context, db *DB, feedID int64, a *NewArticle, dedup bool, rules []models.Rule) (bool, error) {
	if dedup && a.URL != nil && *a.URL != "" {
		var exists bool
		if err := db.W.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM articles WHERE url = ?1)", *a.URL).Scan(&exists); err != nil {
			return false, apperr.Wrap("db", err)
		}
		if exists {
			return false, nil
		}
	}

	// A synthetic (title+date) guid can't match rows stored under the old
	// URL-fallback key, and a guid-less feed may also legitimately re-serve an
	// item whose title was edited. In both cases the same feed_id + URL still
	// identifies the article — treat it as already present rather than new.
	if a.SyntheticGUID && a.URL != nil && *a.URL != "" {
		var exists bool
		if err := db.W.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM articles WHERE feed_id = ?1 AND url = ?2)",
			feedID, *a.URL).Scan(&exists); err != nil {
			return false, apperr.Wrap("db", err)
		}
		if exists {
			return false, nil
		}
	}

	// A previously retention-purged article (same feed + guid) must not be
	// re-ingested as fresh unread. A full-archive feed keeps every past item
	// in its document, so without this the daily cleanup and the next refresh
	// would ping-pong the whole read history back as new, every day. Checked
	// regardless of the dedup flag — the tombstone, not URL dedup, is what
	// closes the loop.
	var tombstoned bool
	if err := db.W.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM article_tombstones WHERE feed_id = ?1 AND guid = ?2)",
		feedID, a.GUID).Scan(&tombstoned); err != nil {
		return false, apperr.Wrap("db", err)
	}
	if tombstoned {
		return false, nil
	}

	// Apply filter rules: skip wins outright; read / star tint the new row.
	startRead, startStarred := false, false
	for _, rule := range rules {
		if !ruleMatches(rule, feedID, a) {
			continue
		}
		switch rule.Action {
		case "skip":
			return false, nil
		case "read":
			startRead = true
		case "star":
			startStarred = true
		}
	}

	// The article row, its FTS index entry, and its enclosures must land
	// together — wrap them in a transaction so a mid-loop failure rolls back.
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return false, apperr.Wrap("db", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO articles
			(feed_id, guid, url, title, author, summary, content_html, body_text,
			 image_url, published_at, is_read, is_starred)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12)
		 ON CONFLICT(feed_id, guid) DO NOTHING`,
		feedID, a.GUID, nz(a.URL), a.Title, nz(a.Author), nz(a.Summary),
		nz(a.ContentHTML), a.BodyText, nz(a.ImageURL), nz(a.PublishedAt),
		startRead, startStarred)
	if err != nil {
		return false, apperr.Wrap("db", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, apperr.Wrap("db", err)
	}
	if n == 0 {
		return false, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return false, apperr.Wrap("db", err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO articles_fts(rowid, title, body) VALUES (?1, ?2, ?3)",
		id, a.Title, a.BodyText); err != nil {
		return false, apperr.Wrap("db", err)
	}
	for _, e := range a.Enclosures {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO enclosures(article_id, url, mime_type, length) VALUES (?1,?2,?3,?4)",
			id, e.URL, nz(e.MimeType), nz(e.Length)); err != nil {
			return false, apperr.Wrap("db", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, apperr.Wrap("db", err)
	}
	// A row inserted but pre-marked read by a read rule is not "new" from the
	// user's point of view — report it as not-inserted.
	return !startRead, nil
}

// ftsQuery turns raw user text into a safe FTS5 MATCH expression (each term
// prefix-matched). orJoin selects how multiple terms combine: false joins with
// an implicit AND (explicit search — adding words narrows results); true joins
// with OR (recall-oriented retrieval, e.g. RAG over a question).
//
// Punctuation splits a word into separate terms rather than being deleted
// inside it, matching how the unicode61 tokenizer indexes rust-lang / node.js.
func ftsQuery(input string, orJoin bool) string {
	var terms []string
	for _, t := range strings.FieldsFunc(input, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		terms = append(terms, `"`+t+`"*`)
	}
	if len(terms) == 0 {
		return `""`
	}
	if orJoin {
		return strings.Join(terms, " OR ")
	}
	return strings.Join(terms, " ")
}

// ListArticles builds and runs the article-list query for the given sidebar
// selection. It uses plain positional placeholders because the WHERE clause and
// the optional FTS join are assembled dynamically.
func ListArticles(ctx context.Context, q Querier, query models.ArticleQuery, unreadOnly bool, search *string, oldestFirst bool, limit, offset int64) ([]models.ArticleSummary, error) {
	where := []string{"1=1"}
	var binds []any

	switch query.Kind {
	case models.QueryAll:
	case models.QueryUnread:
		where = append(where, "a.is_read = 0")
	case models.QueryStarred:
		where = append(where, "a.is_starred = 1")
	case models.QueryReadLater:
		where = append(where, "a.read_later = 1")
	case models.QueryFeed:
		where = append(where, "a.feed_id = ?")
		binds = append(binds, deref(query.Value))
	case models.QueryFolder:
		where = append(where, "f.folder_id = ?")
		binds = append(binds, deref(query.Value))
	case models.QueryTag:
		where = append(where, "a.id IN (SELECT article_id FROM article_tags WHERE tag_id = ?)")
		binds = append(binds, deref(query.Value))
	}
	if unreadOnly && query.Kind != models.QueryUnread {
		where = append(where, "a.is_read = 0")
	}

	searching := search != nil && strings.TrimSpace(*search) != ""
	var sb strings.Builder
	sb.WriteString(
		`SELECT a.id, a.feed_id, f.title, f.source_type, a.title, a.author,
				substr(a.body_text,1,280), a.image_url, a.url, a.published_at,
				a.is_read, a.is_starred, a.read_later
		 FROM articles a JOIN feeds f ON f.id = a.feed_id `)
	if searching {
		sb.WriteString("JOIN articles_fts fts ON fts.rowid = a.id ")
		where = append(where, "articles_fts MATCH ?")
		// Explicit search: every typed word must match (AND).
		binds = append(binds, ftsQuery(*search, false))
	}
	sb.WriteString("WHERE ")
	sb.WriteString(strings.Join(where, " AND "))
	switch {
	case searching:
		sb.WriteString(" ORDER BY fts.rank ")
	case oldestFirst:
		sb.WriteString(" ORDER BY datetime(COALESCE(a.published_at, a.fetched_at)) ASC, a.id ASC ")
	default:
		sb.WriteString(" ORDER BY datetime(COALESCE(a.published_at, a.fetched_at)) DESC, a.id DESC ")
	}
	sb.WriteString("LIMIT ? OFFSET ?")
	binds = append(binds, limit, offset)

	rows, err := q.QueryContext(ctx, sb.String(), binds...)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []models.ArticleSummary{}
	for rows.Next() {
		var (
			s                                   models.ArticleSummary
			author, snippet, imageURL, url, pub sql.Null[string]
		)
		if err := rows.Scan(&s.ID, &s.FeedID, &s.FeedTitle, &s.SourceType, &s.Title,
			&author, &snippet, &imageURL, &url, &pub,
			&s.IsRead, &s.IsStarred, &s.ReadLater); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		s.Author = nullPtr(author)
		s.Snippet = nullPtr(snippet)
		s.ImageURL = nullPtr(imageURL)
		s.URL = nullPtr(url)
		s.PublishedAt = nullPtr(pub)
		out = append(out, s)
	}
	return out, rows.Err()
}

// RagResult is one (id, title, feedTitle) match for AI retrieval context.
type RagResult struct {
	ID        int64
	Title     string
	FeedTitle string
	// Summary is the compact per-article context handed to the model: the cached
	// AI summary when the article has one, otherwise a body excerpt, capped to
	// the caller's summaryChars. Empty only for a title-only article (skipped).
	Summary string
}

// SearchArticlesForRAG retrieves up to limit articles relevant to a
// natural-language question, for use as RAG context. Uses OR-joined FTS terms
// so a multi-word question still matches articles containing some of its
// keywords. Ordered by FTS relevance.
func SearchArticlesForRAG(ctx context.Context, q Querier, question string, limit, summaryChars int64) ([]RagResult, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT a.id, a.title, f.title,
		        substr(COALESCE(NULLIF(TRIM(a.ai_summary), ''), a.body_text), 1, ?2)
		 FROM articles a
		 JOIN feeds f ON f.id = a.feed_id
		 JOIN articles_fts fts ON fts.rowid = a.id
		 WHERE articles_fts MATCH ?1
		 ORDER BY fts.rank
		 LIMIT ?3`,
		ftsQuery(question, true), summaryChars, limit)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []RagResult{}
	for rows.Next() {
		var r RagResult
		if err := rows.Scan(&r.ID, &r.Title, &r.FeedTitle, &r.Summary); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DigestItem is one recent article as (title, feedTitle, text) for an AI digest.
type DigestItem struct {
	Title     string
	FeedTitle string
	Text      string
}

// DigestSource returns articles for building an AI digest, unread first.
//
// A digest is a "catch me up" briefing, so it leads with what the user hasn't
// read (is_read ASC puts unread before read), most-recent within each group.
// Ordering rather than filtering on is_read means a caught-up user (no unread)
// still gets a briefing of their most recent articles instead of an empty one —
// the fallback is built into the sort.
func DigestSource(ctx context.Context, q Querier, limit int64) ([]DigestItem, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT a.title, f.title, substr(a.body_text, 1, 600)
		 FROM articles a JOIN feeds f ON f.id = a.feed_id
		 ORDER BY a.is_read ASC, datetime(COALESCE(a.published_at, a.fetched_at)) DESC, a.id DESC
		 LIMIT ?1`, limit)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []DigestItem{}
	for rows.Next() {
		var d DigestItem
		if err := rows.Scan(&d.Title, &d.FeedTitle, &d.Text); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RecentArticlesForRAG returns the most recent articles (unread first) as RAG
// context. It is the zero-hit fallback for Ask: a question with no content
// keywords — "what are today's headlines?", "what's new in my feeds?" — matches
// nothing in the FTS index, so SearchArticlesForRAG comes back empty. Falling
// back to recent articles lets those overview/recency questions still be
// answered. Same shape (RagResult) as SearchArticlesForRAG so the caller's
// context-building and source-emission paths are identical. Ordering mirrors
// DigestSource.
func RecentArticlesForRAG(ctx context.Context, q Querier, limit, summaryChars int64) ([]RagResult, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT a.id, a.title, f.title,
		        substr(COALESCE(NULLIF(TRIM(a.ai_summary), ''), a.body_text), 1, ?1)
		 FROM articles a JOIN feeds f ON f.id = a.feed_id
		 ORDER BY a.is_read ASC, datetime(COALESCE(a.published_at, a.fetched_at)) DESC, a.id DESC
		 LIMIT ?2`, summaryChars, limit)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []RagResult{}
	for rows.Next() {
		var r RagResult
		if err := rows.Scan(&r.ID, &r.Title, &r.FeedTitle, &r.Summary); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetArticle returns the full article shown in the reading pane, including
// enclosures and tags.
func GetArticle(ctx context.Context, q Querier, id int64) (*models.ArticleDetail, error) {
	var (
		d                                       models.ArticleDetail
		author, url, contentHTML, extractedHTML sql.Null[string]
		imageURL, pub, aiSummary, feedOpenMode  sql.Null[string]
		feedAutoTranslate                       sql.Null[int64]
	)
	err := q.QueryRowContext(ctx,
		`SELECT a.id, a.feed_id, f.title, f.source_type, a.title, a.author, a.url,
				a.content_html, a.extracted_html, a.image_url, a.published_at,
				a.is_read, a.is_starred, a.read_later, a.ai_summary,
				f.auto_translate, f.open_mode
		 FROM articles a JOIN feeds f ON f.id = a.feed_id WHERE a.id = ?1`, id).
		Scan(&d.ID, &d.FeedID, &d.FeedTitle, &d.SourceType, &d.Title, &author, &url,
			&contentHTML, &extractedHTML, &imageURL, &pub,
			&d.IsRead, &d.IsStarred, &d.ReadLater, &aiSummary,
			&feedAutoTranslate, &feedOpenMode)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	d.Author = nullPtr(author)
	d.URL = nullPtr(url)
	d.ContentHTML = nullPtr(contentHTML)
	d.ExtractedHTML = nullPtr(extractedHTML)
	d.ImageURL = nullPtr(imageURL)
	d.PublishedAt = nullPtr(pub)
	d.AiSummary = nullPtr(aiSummary)
	d.FeedAutoTranslate = nullPtr(feedAutoTranslate)
	d.FeedOpenMode = nullPtr(feedOpenMode)

	encRows, err := q.QueryContext(ctx,
		"SELECT url, mime_type, length FROM enclosures WHERE article_id = ?1", id)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer encRows.Close()
	d.Enclosures = []models.Enclosure{}
	for encRows.Next() {
		var (
			e        models.Enclosure
			mimeType sql.Null[string]
			length   sql.Null[int64]
		)
		if err := encRows.Scan(&e.URL, &mimeType, &length); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		e.MimeType = nullPtr(mimeType)
		e.Length = nullPtr(length)
		d.Enclosures = append(d.Enclosures, e)
	}
	if err := encRows.Err(); err != nil {
		return nil, apperr.Wrap("db", err)
	}

	tags, err := TagsForArticle(ctx, q, id)
	if err != nil {
		return nil, err
	}
	d.Tags = tags
	return &d, nil
}

// SetRead sets an article's read flag.
func SetRead(ctx context.Context, q Querier, id int64, read bool) error {
	if _, err := q.ExecContext(ctx, "UPDATE articles SET is_read = ?2 WHERE id = ?1", id, read); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// SetStarred sets an article's starred flag.
func SetStarred(ctx context.Context, q Querier, id int64, starred bool) error {
	if _, err := q.ExecContext(ctx, "UPDATE articles SET is_starred = ?2 WHERE id = ?1", id, starred); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// SetReadLater sets an article's read-later flag.
func SetReadLater(ctx context.Context, q Querier, id int64, v bool) error {
	if _, err := q.ExecContext(ctx, "UPDATE articles SET read_later = ?2 WHERE id = ?1", id, v); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// SetAiSummary stores a generated AI summary for an article.
func SetAiSummary(ctx context.Context, q Querier, id int64, summary string) error {
	if _, err := q.ExecContext(ctx, "UPDATE articles SET ai_summary = ?2 WHERE id = ?1", id, summary); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// translationCacheKeep is how many (lang, engine) renderings are retained per
// article. A reader rarely needs more than a couple at once; the oldest are
// pruned so the cache can't grow without bound on a heavily re-translated piece.
const translationCacheKeep = 4

// ArticlePreviewText returns the list-preview source text for an article: its
// title and the same body_text excerpt the list rows show.
func ArticlePreviewText(ctx context.Context, q Querier, id int64) (title, snippet string, err error) {
	err = q.QueryRowContext(ctx,
		"SELECT title, substr(body_text, 1, 280) FROM articles WHERE id = ?1", id).
		Scan(&title, &snippet)
	if err != nil {
		return "", "", apperr.Wrap("db", err)
	}
	return title, snippet, nil
}

// GetPreviewTranslation returns the cached list-preview translation for this
// exact target language and engine, if one exists.
func GetPreviewTranslation(ctx context.Context, q Querier, id int64, lang, engine string) (title, snippet string, ok bool, err error) {
	err = q.QueryRowContext(ctx,
		`SELECT title, snippet FROM article_preview_translations
		  WHERE article_id = ?1 AND lang = ?2 AND engine = ?3`, id, lang, engine).
		Scan(&title, &snippet)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, apperr.Wrap("db", err)
	}
	return title, snippet, true, nil
}

// SetPreviewTranslation caches a list-preview translation independently from
// full-body translations.
func SetPreviewTranslation(ctx context.Context, q Querier, id int64, lang, engine, title, snippet string) error {
	if _, err := q.ExecContext(ctx,
		`INSERT INTO article_preview_translations(article_id, lang, engine, title, snippet, updated_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, datetime('now'))
		 ON CONFLICT(article_id, lang, engine) DO UPDATE SET
			 title = excluded.title,
			 snippet = excluded.snippet,
			 updated_at = excluded.updated_at`,
		id, lang, engine, strings.TrimSpace(title), strings.TrimSpace(snippet)); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// UpsertTranslation caches a completed translation keyed by (article, lang,
// engine), storing the translated title and body, then prunes the article's
// cache to the translationCacheKeep most-recently-updated renderings.
func UpsertTranslation(ctx context.Context, q Querier, id int64, lang, engine string, title *string, html string) error {
	if _, err := q.ExecContext(ctx,
		`INSERT INTO article_translations (article_id, lang, engine, title, html, updated_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, datetime('now'))
		 ON CONFLICT(article_id, lang, engine)
		 DO UPDATE SET title = excluded.title, html = excluded.html, updated_at = excluded.updated_at`,
		id, lang, engine, title, html); err != nil {
		return apperr.Wrap("db", err)
	}
	// Prune all but the newest translationCacheKeep for this article.
	if _, err := q.ExecContext(ctx,
		`DELETE FROM article_translations
		 WHERE article_id = ?1 AND rowid NOT IN (
			SELECT rowid FROM article_translations WHERE article_id = ?1
			ORDER BY updated_at DESC, rowid DESC LIMIT ?2)`,
		id, translationCacheKeep); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// GetTranslations returns every cached rendering for an article (newest first),
// so the reader can pick the one matching the current target language + engine.
func GetTranslations(ctx context.Context, q Querier, id int64) ([]models.TranslationCache, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT lang, engine, title, html FROM article_translations
		 WHERE article_id = ?1 ORDER BY updated_at DESC, rowid DESC`, id)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []models.TranslationCache{}
	for rows.Next() {
		var (
			t     models.TranslationCache
			title sql.Null[string]
		)
		if err := rows.Scan(&t.Lang, &t.Engine, &title, &t.HTML); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		t.Title = nullPtr(title)
		out = append(out, t)
	}
	return out, rows.Err()
}

// MarkAllRead marks every article matching the current sidebar selection as
// read. When enqueueSync is set, the read change is also queued for the sync
// server. Returns the number of rows flipped.
func MarkAllRead(ctx context.Context, db *DB, query models.ArticleQuery, enqueueSync bool) (int64, error) {
	// WHERE fragment selecting the articles in the current view, plus an
	// optional bound id (feed / folder / tag).
	var pred string
	var id *int64
	switch query.Kind {
	case models.QueryAll, models.QueryUnread:
		pred = "1"
	case models.QueryStarred:
		pred = "is_starred = 1"
	case models.QueryReadLater:
		pred = "read_later = 1"
	case models.QueryFeed:
		pred, id = "feed_id = ?", query.Value
	case models.QueryFolder:
		pred, id = "feed_id IN (SELECT id FROM feeds WHERE folder_id = ?)", query.Value
	case models.QueryTag:
		pred, id = "id IN (SELECT article_id FROM article_tags WHERE tag_id = ?)", query.Value
	default:
		pred = "1"
	}
	var bind []any
	if id != nil {
		bind = append(bind, *id)
	}

	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	defer tx.Rollback()

	// Queue before flipping so the is_read = 0 filter still matches.
	if enqueueSync {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO sync_queue(article_id, field, value)"+
				" SELECT id, 'read', 1 FROM articles"+
				" WHERE "+pred+" AND is_read = 0"+
				" ON CONFLICT(article_id, field) DO UPDATE SET value = 1",
			bind...); err != nil {
			return 0, apperr.Wrap("db", err)
		}
	}
	res, err := tx.ExecContext(ctx,
		"UPDATE articles SET is_read = 1 WHERE "+pred+" AND is_read = 0", bind...)
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, apperr.Wrap("db", err)
	}
	return n, nil
}

// IsFreshrssConnected reports whether a FreshRSS server is currently linked (a
// non-empty URL is stored).
func IsFreshrssConnected(ctx context.Context, q Querier) bool {
	v, err := GetSetting(ctx, q, "freshrss_url")
	if err != nil || v == nil {
		return false
	}
	return strings.TrimSpace(*v) != ""
}
