package db

// migrations is the append-only list of schema migrations, ported verbatim from
// papr's src-tauri/src/db.rs MIGRATIONS. The slice index + 1 is the schema
// version. Never edit a shipped migration — add a new one at the end.
var migrations = []string{
	// v1 — initial schema: folders, feeds, articles, enclosures, the FTS5
	// index and settings.
	`
	CREATE TABLE folders (
		id        INTEGER PRIMARY KEY,
		name      TEXT NOT NULL,
		position  INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE feeds (
		id              INTEGER PRIMARY KEY,
		feed_url        TEXT NOT NULL UNIQUE,
		site_url        TEXT,
		title           TEXT NOT NULL,
		description     TEXT,
		favicon_url     TEXT,
		folder_id       INTEGER REFERENCES folders(id) ON DELETE SET NULL,
		source_type     TEXT NOT NULL DEFAULT 'rss',
		etag            TEXT,
		last_modified   TEXT,
		last_fetched_at TEXT,
		fetch_error     TEXT,
		created_at      TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE articles (
		id            INTEGER PRIMARY KEY,
		feed_id       INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
		guid          TEXT NOT NULL,
		url           TEXT,
		title         TEXT NOT NULL,
		author        TEXT,
		summary       TEXT,
		content_html  TEXT,
		extracted_html TEXT,
		body_text     TEXT NOT NULL DEFAULT '',
		image_url     TEXT,
		ai_summary    TEXT,
		published_at  TEXT,
		fetched_at    TEXT NOT NULL DEFAULT (datetime('now')),
		is_read       INTEGER NOT NULL DEFAULT 0,
		is_starred    INTEGER NOT NULL DEFAULT 0,
		read_later    INTEGER NOT NULL DEFAULT 0,
		UNIQUE(feed_id, guid)
	);

	CREATE INDEX idx_articles_feed      ON articles(feed_id);
	CREATE INDEX idx_articles_published ON articles(published_at DESC);
	CREATE INDEX idx_articles_unread    ON articles(is_read) WHERE is_read = 0;

	CREATE TABLE enclosures (
		id         INTEGER PRIMARY KEY,
		article_id INTEGER NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
		url        TEXT NOT NULL,
		mime_type  TEXT,
		length     INTEGER
	);
	CREATE INDEX idx_enclosures_article ON enclosures(article_id);

	CREATE VIRTUAL TABLE articles_fts USING fts5(
		title, body, tokenize = 'porter unicode61'
	);

	CREATE TRIGGER articles_fts_ad AFTER DELETE ON articles BEGIN
		DELETE FROM articles_fts WHERE rowid = old.id;
	END;

	CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	`,

	// v2 — placeholder. An earlier sqlite-vec semantic-search schema was
	// removed; this keeps the version count aligned. Search is keyword-only
	// (FTS5). A no-op statement preserves the version slot.
	`SELECT 1;`,

	// v3 — sync support: a remote item id per article plus a small queue of
	// local read/starred changes still to push to the sync server.
	`
	ALTER TABLE articles ADD COLUMN remote_id TEXT;
	CREATE TABLE sync_queue (
		article_id INTEGER NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
		field      TEXT NOT NULL,
		value      INTEGER NOT NULL,
		PRIMARY KEY (article_id, field)
	);
	`,

	// v4 — article tags: a flat label set plus an article↔tag join table.
	`
	CREATE TABLE tags (
		id        INTEGER PRIMARY KEY,
		name      TEXT NOT NULL UNIQUE,
		color     TEXT NOT NULL DEFAULT 'clay',
		position  INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE article_tags (
		article_id INTEGER NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
		tag_id     INTEGER NOT NULL REFERENCES tags(id)     ON DELETE CASCADE,
		PRIMARY KEY (article_id, tag_id)
	);
	CREATE INDEX idx_article_tags_tag ON article_tags(tag_id);
	`,

	// v5 — filter rules: keyword matches applied to incoming articles to
	// auto-skip noise, or auto mark-read / star them, at ingestion time.
	`
	CREATE TABLE rules (
		id         INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		enabled    INTEGER NOT NULL DEFAULT 1,
		feed_id    INTEGER REFERENCES feeds(id) ON DELETE CASCADE,
		field      TEXT NOT NULL DEFAULT 'title',
		query      TEXT NOT NULL,
		action     TEXT NOT NULL DEFAULT 'skip',
		position   INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	);
	`,

	// v6 — index over the effective article date the list sorts by,
	// COALESCE(published_at, fetched_at). (Superseded by v12.)
	`CREATE INDEX idx_articles_sort
		ON articles(COALESCE(published_at, fetched_at) DESC, id DESC);`,

	// v7 — every date ordering now sorts on the effective date and uses
	// idx_articles_sort, so the original published_at-only index is dead
	// weight on each insert. Drop it.
	`DROP INDEX idx_articles_published;`,

	// v8 — partial indexes mirroring idx_articles_unread for the other two
	// smart-view flags, so the Starred / Read-later sidebar counts and list
	// queries use a tiny index instead of a full table scan.
	`CREATE INDEX idx_articles_starred
		 ON articles(is_starred) WHERE is_starred = 1;
	 CREATE INDEX idx_articles_readlater
		 ON articles(read_later) WHERE read_later = 1;`,

	// v9 — index the article URL. FreshRSS reconciliation matches remote items
	// to local articles by URL and the dedup check tests URL existence per
	// inserted article; both full-scanned the table without this.
	`CREATE INDEX idx_articles_url ON articles(url);`,

	// v10 — email-newsletter sources (feature F5). A newsletter is a normal
	// feeds row (source_type = 'newsletter'); this side-table holds the IMAP
	// connection details, keyed 1:1 by feed_id and cascade-deleted with the
	// feed.
	`
	CREATE TABLE newsletter_sources (
		feed_id   INTEGER PRIMARY KEY REFERENCES feeds(id) ON DELETE CASCADE,
		host      TEXT NOT NULL,
		port      INTEGER NOT NULL DEFAULT 993,
		username  TEXT NOT NULL,
		password  TEXT NOT NULL,
		folder    TEXT NOT NULL DEFAULT 'INBOX'
	);
	`,

	// v11 — highlights / annotations layer (feature F7). Each highlight pins a
	// span of an article's rendered plain text. text_offset is the character
	// offset of the quote; prefix / suffix carry a short context window for
	// robust re-anchoring when the rendered text shifts.
	`
	CREATE TABLE highlights (
		id          INTEGER PRIMARY KEY,
		article_id  INTEGER NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
		quote       TEXT NOT NULL,
		prefix      TEXT NOT NULL DEFAULT '',
		suffix      TEXT NOT NULL DEFAULT '',
		text_offset INTEGER NOT NULL DEFAULT 0,
		color       TEXT NOT NULL DEFAULT 'yellow',
		note        TEXT NOT NULL DEFAULT '',
		created_at  TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE INDEX idx_highlights_article ON highlights(article_id);
	`,

	// v12 — rebuild the article-sort index over the *normalised* effective
	// date. published_at is RFC 3339 (T-separated) while fetched_at uses
	// SQLite's space-separated form; the old raw COALESCE index sorted the two
	// formats byte-for-byte and put dated rows up to a day ahead of same-
	// instant dateless ones. Wrapping in datetime() parses both to one
	// canonical form; the ORDER BY clauses are wrapped to match.
	`DROP INDEX idx_articles_sort;
	 CREATE INDEX idx_articles_sort
		 ON articles(datetime(COALESCE(published_at, fetched_at)) DESC,
					 id DESC);`,

	// v13 — mark feeds whose title the user has set by hand, so a refresh's
	// update_feed_meta leaves a manual rename alone while still refreshing
	// every other piece of feed metadata.
	`ALTER TABLE feeds ADD COLUMN custom_title INTEGER NOT NULL DEFAULT 0;`,

	// v14 — cache a translated copy of the article body. translated_lang
	// records the target language the cache was produced for, so a later change
	// to the translation-target setting is detected as a cache miss.
	`ALTER TABLE articles ADD COLUMN translated_html TEXT;
	 ALTER TABLE articles ADD COLUMN translated_lang TEXT;`,

	// v15 — per-feed refresh interval (minutes). NULL follows the global
	// refresh_interval_min setting; the 525_600 sentinel means "never".
	`ALTER TABLE feeds ADD COLUMN refresh_interval_min INTEGER;`,

	// v16 — richer translation cache + per-feed auto-translate.
	//   article_translations keeps several (lang, engine) renderings per article
	//   (each with the translated title), replacing the single body slot in
	//   articles.translated_html/_lang (left in place as dead columns). Pruned to
	//   the few most-recent per article by the service layer.
	//   feeds.auto_translate: NULL follows the global translate_auto setting,
	//   0 forces off, 1 forces on.
	`
	CREATE TABLE article_translations (
		article_id INTEGER NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
		lang       TEXT NOT NULL,
		engine     TEXT NOT NULL,
		title      TEXT,
		html       TEXT NOT NULL,
		updated_at TEXT NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY (article_id, lang, engine)
	);
	ALTER TABLE feeds ADD COLUMN auto_translate INTEGER;
	`,

	// v17 — one-time clear of the translation cache. Early auto-translate could
	// run against a feed's summary before full-text extraction finished and cache
	// that summary translation; those stale entries are wiped so they re-translate
	// against the full text. The cache is derived data, so dropping it is safe.
	`DELETE FROM article_translations;`,

	// v18 — downloaded open-source fonts (the font-switching feature). Tracks
	// files fetched into <dataDir>/fonts and served at /userfonts/<file>. The
	// chosen ui/reader font references these by id; that choice lives in the
	// frontend prefs (localStorage) alongside theme/accent, not here.
	`
	CREATE TABLE installed_fonts (
		id           TEXT PRIMARY KEY,
		family       TEXT NOT NULL,
		label        TEXT NOT NULL,
		category     TEXT NOT NULL DEFAULT 'sans',
		cjk          INTEGER NOT NULL DEFAULT 0,
		axis         TEXT NOT NULL DEFAULT 'both',
		license      TEXT NOT NULL DEFAULT '',
		source       TEXT NOT NULL DEFAULT '',
		file         TEXT NOT NULL,
		ext          TEXT NOT NULL DEFAULT 'woff2',
		bytes        INTEGER NOT NULL DEFAULT 0,
		installed_at TEXT NOT NULL DEFAULT (datetime('now'))
	);
	`,

	// v19 — partial composite index for the per-feed unread count subquery
	// in ListFeeds (internal/db/feeds.go). idx_articles_unread covers only
	// is_read alone, so the correlated COUNT(*) per feed had to filter by
	// feed_id row-by-row, scaling first-screen load with article volume.
	// This index lets each subquery run as an index-only scan over just the
	// unread rows for that feed.
	`CREATE INDEX idx_articles_feed_unread
		ON articles(feed_id) WHERE is_read = 0;`,

	// v20 — retention tombstones. A full-archive feed (e.g. Hugo's index.xml)
	// ships its entire history in the feed document forever, so a read article
	// that retention purges is re-fetched on the very next refresh and
	// re-inserted as brand-new *unread* — resurfacing the whole archive the
	// user just cleared, every day the daily cleanup runs. CleanupOldArticles
	// records each purged article's (feed_id, guid) here and UpsertArticle
	// drops a matching re-fetch, so a retention-purged read article stays
	// gone. Cascade-deletes with the feed.
	`
	CREATE TABLE article_tombstones (
		feed_id INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
		guid    TEXT NOT NULL,
		PRIMARY KEY (feed_id, guid)
	);
	`,

	// v21 — per-feed open mode: 'reader', 'extracted' or 'web'. NULL (the
	// default) follows the global default-open-mode preference (a frontend
	// pref), which itself defaults to today's behaviour: reader view.
	`ALTER TABLE feeds ADD COLUMN open_mode TEXT;`,

	// v22 — independent list-pane preview-translation cache. Preview
	// translations are keyed by article, target language and engine so
	// switching language or engine never overwrites another cached preview.
	// A source title/body update invalidates the derived preview text.
	`
	CREATE TABLE article_preview_translations (
		article_id INTEGER NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
		lang       TEXT NOT NULL,
		engine     TEXT NOT NULL,
		title      TEXT NOT NULL,
		snippet    TEXT NOT NULL,
		updated_at TEXT NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY(article_id, lang, engine)
	);
	CREATE TRIGGER article_preview_translations_au
	AFTER UPDATE OF title, body_text ON articles BEGIN
		DELETE FROM article_preview_translations WHERE article_id = new.id;
	END;
	`,
}
