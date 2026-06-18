// Package models holds the domain types shared between the database layer,
// ingestion, services, and the frontend. All structs serialize to camelCase so
// the Vue side stays idiomatic and matches the original src/types.ts contract.
//
// Ported from papr's src-tauri/src/models.rs. Nullable fields use pointer types
// so a nil value marshals to JSON `null` (matching the `T | null` frontend
// types) rather than being omitted.
package models

// SourceType is the kind of source a feed represents. Drives differentiated
// rendering in the UI.
type SourceType = string

const (
	SourceRSS        SourceType = "rss"
	SourceYoutube    SourceType = "youtube"
	SourcePodcast    SourceType = "podcast"
	SourceMastodon   SourceType = "mastodon"
	SourceBluesky    SourceType = "bluesky"
	SourceReddit     SourceType = "reddit"
	SourceNewsletter SourceType = "newsletter"
)

// Folder groups feeds in the sidebar.
type Folder struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Position int64  `json:"position"`
}

// Feed is a subscription. unreadCount is computed at query time.
type Feed struct {
	ID            int64   `json:"id"`
	FeedURL       string  `json:"feedUrl"`
	SiteURL       *string `json:"siteUrl"`
	Title         string  `json:"title"`
	Description   *string `json:"description"`
	FaviconURL    *string `json:"faviconUrl"`
	FolderID      *int64  `json:"folderId"`
	SourceType    string  `json:"sourceType"`
	LastFetchedAt *string `json:"lastFetchedAt"`
	FetchError    *string `json:"fetchError"`
	UnreadCount   int64   `json:"unreadCount"`
	// RefreshIntervalMin is the per-feed refresh interval in minutes. nil
	// follows the global refresh_interval_min setting; the 525600 sentinel
	// means "never".
	RefreshIntervalMin *int64 `json:"refreshIntervalMin"`
	// AutoTranslate is the per-feed auto-translate override: nil follows the
	// global translate_auto setting, 0 forces off, 1 forces on.
	AutoTranslate *int64 `json:"autoTranslate"`
	// OpenMode is how articles from this feed open in the reader:
	// "reader" (RSS content), "extracted" (auto-run full-text extraction) or
	// "web" (the original page in the native page view). nil follows the
	// global default-open-mode preference.
	OpenMode *string `json:"openMode"`
}

// ArticlePreviewTranslation is a translated list-row preview (title +
// snippet), returned by AIService.TranslateArticlePreview and cached in
// article_preview_translations.
type ArticlePreviewTranslation struct {
	ArticleID int64  `json:"articleId"`
	Title     string `json:"title"`
	Snippet   string `json:"snippet"`
	Lang      string `json:"lang"`
	Engine    string `json:"engine"`
}

// Enclosure is a media attachment on an article (podcast audio, video, etc.).
type Enclosure struct {
	URL      string  `json:"url"`
	MimeType *string `json:"mimeType"`
	Length   *int64  `json:"length"`
}

// Tag is a user-defined label that can be attached to any number of articles.
type Tag struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Color is a palette key (resolved to a colour by the frontend).
	Color    string `json:"color"`
	Position int64  `json:"position"`
	// ArticleCount is how many articles currently carry this tag.
	ArticleCount int64 `json:"articleCount"`
}

// Rule is a keyword filter applied to incoming articles at ingestion time.
type Rule struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// FeedID nil applies the rule to every feed; otherwise scoped to one feed.
	FeedID *int64 `json:"feedId"`
	// Field is which text to match: title | author | content | any.
	Field string `json:"field"`
	// Query is comma-separated keywords; the rule fires if any one is a substring.
	Query string `json:"query"`
	// Action is what to do on a match: skip | read | star.
	Action   string `json:"action"`
	Position int64  `json:"position"`
}

// ArticleSummary is a row in the article list pane. Keeps the payload small
// (no full HTML body).
type ArticleSummary struct {
	ID          int64   `json:"id"`
	FeedID      int64   `json:"feedId"`
	FeedTitle   string  `json:"feedTitle"`
	SourceType  string  `json:"sourceType"`
	Title       string  `json:"title"`
	Author      *string `json:"author"`
	Snippet     *string `json:"snippet"`
	ImageURL    *string `json:"imageUrl"`
	URL         *string `json:"url"`
	PublishedAt *string `json:"publishedAt"`
	IsRead      bool    `json:"isRead"`
	IsStarred   bool    `json:"isStarred"`
	ReadLater   bool    `json:"readLater"`
}

// ArticleDetail is the full article shown in the reading pane.
type ArticleDetail struct {
	ID         int64   `json:"id"`
	FeedID     int64   `json:"feedId"`
	FeedTitle  string  `json:"feedTitle"`
	SourceType string  `json:"sourceType"`
	Title      string  `json:"title"`
	Author     *string `json:"author"`
	URL        *string `json:"url"`
	// ContentHTML is sanitized HTML from the feed itself.
	ContentHTML *string `json:"contentHtml"`
	// ExtractedHTML is sanitized HTML from full-text extraction, if performed.
	ExtractedHTML *string `json:"extractedHtml"`
	ImageURL      *string `json:"imageUrl"`
	PublishedAt   *string `json:"publishedAt"`
	IsRead        bool    `json:"isRead"`
	IsStarred     bool    `json:"isStarred"`
	ReadLater     bool    `json:"readLater"`
	AiSummary     *string `json:"aiSummary"`
	// FeedAutoTranslate is the owning feed's auto-translate override: nil follows
	// the global translate_auto setting, 0 forces off, 1 forces on. Surfaced here
	// so the reader can resolve the effective setting without a second query.
	FeedAutoTranslate *int64 `json:"feedAutoTranslate"`
	// FeedOpenMode is the owning feed's open-mode override ("reader" /
	// "extracted" / "web"): nil follows the global default-open-mode
	// preference. Surfaced here for the same one-query reason.
	FeedOpenMode *string     `json:"feedOpenMode"`
	Enclosures   []Enclosure `json:"enclosures"`
	// Tags currently attached to this article.
	Tags []Tag `json:"tags"`
}

// TranslationCache is one cached rendering of an article, keyed by target
// language + engine: the translated body HTML and (optionally) the translated
// title. Several may exist per article; the reader picks the one matching the
// current target language + engine.
type TranslationCache struct {
	Lang   string  `json:"lang"`
	Engine string  `json:"engine"`
	Title  *string `json:"title"`
	HTML   string  `json:"html"`
}

// Highlight is a user highlight / annotation pinned to a span of an article's
// rendered plain text (feature F7). TextOffset plus Prefix / Suffix form a
// resilient anchor: the offset is tried first, the context window second.
type Highlight struct {
	ID        int64 `json:"id"`
	ArticleID int64 `json:"articleId"`
	// Quote is the highlighted text itself.
	Quote string `json:"quote"`
	// Prefix is a short window of text immediately before the quote.
	Prefix string `json:"prefix"`
	// Suffix is a short window of text immediately after the quote.
	Suffix string `json:"suffix"`
	// TextOffset is the character offset of the quote within the article's
	// plain-text render.
	TextOffset int64 `json:"textOffset"`
	// Color is a palette key resolved to a colour by the frontend.
	Color string `json:"color"`
	// Note is an optional user note; an empty string means no note.
	Note      string `json:"note"`
	CreatedAt string `json:"createdAt"`
}

// HighlightWithContext is Highlight enriched with the article and feed it
// belongs to, used by the global Highlights browser panel and the Markdown
// export. Per-article reader code keeps using the lean Highlight type.
type HighlightWithContext struct {
	Highlight
	ArticleURL   *string `json:"articleUrl"`
	ArticleTitle string  `json:"articleTitle"`
	FeedTitle    string  `json:"feedTitle"`
	FaviconURL   *string `json:"faviconUrl"`
}

// NewsletterConfig holds the IMAP connection details for an email-newsletter
// source. Lives here (a leaf package) so both the db layer and the ingestion
// layer can share it without an import cycle.
type NewsletterConfig struct {
	Host     string
	Port     uint16
	Username string
	Password string
	Folder   string
}

// NewsletterSource is a configured email-newsletter source for the UI list
// (mirrors types.ts NewsletterSource — no password).
type NewsletterSource struct {
	FeedID   int64  `json:"feedId"`
	Title    string `json:"title"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Folder   string `json:"folder"`
}

// NewsletterInput is the payload for adding a newsletter source (mirrors
// types.ts NewsletterInput).
type NewsletterInput struct {
	Title    *string `json:"title"`
	Host     string  `json:"host"`
	Port     int     `json:"port"`
	Username string  `json:"username"`
	Password string  `json:"password"`
	Folder   string  `json:"folder"`
}

// SmartCounts holds the live counts shown next to the smart views in the
// sidebar (mirrors commands::SmartCounts).
type SmartCounts struct {
	Unread    int64 `json:"unread"`
	Starred   int64 `json:"starred"`
	ReadLater int64 `json:"readLater"`
}

// RulePreview is the dry-run result for a draft filter rule (mirrors
// commands::RulePreview / the preview_rule command).
type RulePreview struct {
	Count   int64    `json:"count"`
	Samples []string `json:"samples"`
}

// StorageStats summarizes on-disk usage for the storage panel (mirrors the
// StorageStats interface in src/api.ts).
type StorageStats struct {
	DBBytes      int64 `json:"dbBytes"`
	ArticleCount int64 `json:"articleCount"`
	FeedCount    int64 `json:"feedCount"`
}

// ArticleQuery filters the article list, mirroring the sidebar selection. It is
// the Go form of the adjacently-tagged Rust `ArticleQuery` enum: Kind is one of
// all | unread | starred | readLater | feed | folder | tag, and Value carries
// the feed/folder/tag id for the latter three.
type ArticleQuery struct {
	Kind  string `json:"kind"`
	Value *int64 `json:"value"`
}

// Query kinds for ArticleQuery.Kind.
const (
	QueryAll       = "all"
	QueryUnread    = "unread"
	QueryStarred   = "starred"
	QueryReadLater = "readLater"
	QueryFeed      = "feed"
	QueryFolder    = "folder"
	QueryTag       = "tag"
)
