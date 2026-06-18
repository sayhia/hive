// Type mirrors of the Rust domain model (see src-tauri/src/models.rs).

export type SourceType =
  | "rss"
  | "youtube"
  | "podcast"
  | "mastodon"
  | "bluesky"
  | "reddit"
  | "newsletter";

/** A configured email-newsletter source (mirrors commands::NewsletterSource). */
export interface NewsletterSource {
  feedId: number;
  title: string;
  host: string;
  port: number;
  username: string;
  folder: string;
}

/** Payload for add_newsletter_source (mirrors commands::NewsletterInput). */
export interface NewsletterInput {
  title: string | null;
  host: string;
  port: number;
  username: string;
  password: string;
  folder: string;
}

/** A feed-discovery result (mirrors discovery::DiscoveryResult). */
export interface DiscoveryResult {
  title: string;
  feedUrl: string;
  siteUrl: string | null;
  category: string | null;
  description: string | null;
  /** Primary language subtag ("zh", "en") of a directory entry; "" for scrapes. */
  lang: string;
  /** true → curated directory entry, false → live page scrape. */
  fromDirectory: boolean;
}

/** Build identity (mirrors services.AppInfo). */
export interface AppInfo {
  version: string;
  os: string;
  arch: string;
}

/** Update-check result (mirrors services.UpdateInfo). */
export interface UpdateInfo {
  current: string;
  latest: string;
  releaseUrl: string;
  hasUpdate: boolean;
}

/** Curated-directory status for settings (mirrors services.DirectoryStatus). */
export interface DirectoryStatus {
  count: number;
  /** The directory envelope's authored date; "" for legacy payloads. */
  updatedAt: string;
  /** Last remote check, RFC3339; "" when nothing was ever fetched. */
  checkedAt: string;
  /** True when a RefreshFeedDirectory call swapped in a new copy (304 = false). */
  changed: boolean;
}

/** One directory source for settings (mirrors services.DirectorySource). */
export interface DirectorySource {
  /** "" for the official built-in source. */
  url: string;
  official: boolean;
  /** The source's own entry count, before cross-source dedup. */
  count: number;
  updatedAt: string;
  checkedAt: string;
  /** false → no valid cached copy yet; absent from the merge. */
  ok: boolean;
}

/** A pre-subscribe peek at a feed (mirrors services.FeedPreview). */
export interface FeedPreview {
  title: string;
  feedUrl: string;
  siteUrl: string | null;
  description: string | null;
  items: { title: string; publishedAt: string | null }[] | null;
}

export interface Folder {
  id: number;
  name: string;
  position: number;
}

export interface Feed {
  id: number;
  feedUrl: string;
  siteUrl: string | null;
  title: string;
  description: string | null;
  faviconUrl: string | null;
  folderId: number | null;
  sourceType: SourceType;
  lastFetchedAt: string | null;
  fetchError: string | null;
  unreadCount: number;
  /** Per-feed refresh interval in minutes. `null` follows the global
   *  setting; the `525600` sentinel means "never". */
  refreshIntervalMin: number | null;
  /** Per-feed auto-translate override: `null` follows the global
   *  `translate_auto` setting, `0` forces off, `1` forces on. */
  autoTranslate: number | null;
  /** How this feed's articles open in the reader: "reader", "extracted" or
   *  "web". `null` follows the global default-open-mode preference. */
  openMode: string | null;
}

/** A translated list-row preview, from AIService.TranslateArticlePreview. */
export interface ArticlePreviewTranslation {
  articleId: number;
  title: string;
  snippet: string;
  lang: string;
  engine: string;
}

export interface Enclosure {
  url: string;
  mimeType: string | null;
  length: number | null;
}

export interface Tag {
  id: number;
  name: string;
  color: string;
  position: number;
  articleCount: number;
}

export type RuleField = "title" | "author" | "content" | "any";
export type RuleAction = "skip" | "read" | "star";

/** Dry-run result for a draft filter rule (see preview_rule command). */
export interface RulePreview {
  count: number;
  samples: string[];
}

export interface Rule {
  id: number;
  name: string;
  enabled: boolean;
  feedId: number | null;
  field: RuleField;
  query: string;
  action: RuleAction;
  position: number;
}

export interface ArticleSummary {
  id: number;
  feedId: number;
  feedTitle: string;
  sourceType: SourceType;
  title: string;
  author: string | null;
  snippet: string | null;
  imageUrl: string | null;
  url: string | null;
  publishedAt: string | null;
  isRead: boolean;
  isStarred: boolean;
  readLater: boolean;
}

export interface ArticleDetail {
  id: number;
  feedId: number;
  feedTitle: string;
  sourceType: SourceType;
  title: string;
  author: string | null;
  url: string | null;
  contentHtml: string | null;
  extractedHtml: string | null;
  imageUrl: string | null;
  publishedAt: string | null;
  isRead: boolean;
  isStarred: boolean;
  readLater: boolean;
  aiSummary: string | null;
  /** The owning feed's auto-translate override: null follows the global
   *  setting, 0 forces off, 1 forces on. */
  feedAutoTranslate: number | null;
  /** The owning feed's open-mode override; `null` follows the global default. */
  feedOpenMode: string | null;
  enclosures: Enclosure[];
  tags: Tag[];
}

/** One cached (lang, engine) rendering of an article (mirrors
 *  models::TranslationCache): the translated title + body HTML. */
export interface TranslationCache {
  lang: string;
  engine: string;
  title: string | null;
  html: string;
}

export interface SmartCounts {
  unread: number;
  starred: number;
  readLater: number;
}

/** A user highlight / annotation (mirrors models::Highlight). */
export interface Highlight {
  id: number;
  articleId: number;
  quote: string;
  prefix: string;
  suffix: string;
  textOffset: number;
  color: string;
  note: string;
  createdAt: string;
}

/** A highlight enriched with its owning article + feed metadata (mirrors
 *  models::HighlightWithContext); used by the global highlights browser and
 *  the Markdown export. Per-article reader code keeps using `Highlight`. */
export interface HighlightWithContext extends Highlight {
  articleUrl: string | null;
  articleTitle: string;
  feedTitle: string;
  faviconUrl: string | null;
}

// Mirrors the adjacently-tagged Rust `ArticleQuery` enum.
export type ArticleQuery =
  | { kind: "all" }
  | { kind: "unread" }
  | { kind: "starred" }
  | { kind: "readLater" }
  | { kind: "feed"; value: number }
  | { kind: "folder"; value: number }
  | { kind: "tag"; value: number };

export type AiEvent =
  | { type: "delta"; data: string }
  | { type: "done" }
  | { type: "error"; data: string }
  // Emitted once, before the answer, by the RAG Q&A path: `data` is a JSON
  // array of AiSource (the retrieved articles) the UI shows as citations.
  | { type: "sources"; data: string };

/** One retrieved article the Q&A answer drew on, surfaced as a clickable
 *  citation. Parsed from a "sources" AiEvent's JSON `data`. */
export interface AiSource {
  id: number;
  title: string;
  feedTitle: string;
}

/** One turn of the Ask conversation sent back as history so a follow-up keeps
 *  its context. Matches the generated bindings' ai.Message shape. */
export interface AiChatTurn {
  role: "user" | "assistant";
  content: string;
}

/** Progress for a font download (events on "font:<streamID>"). `total` is -1
 *  when the server doesn't send a Content-Length; `done` marks the final event. */
export interface FontProgress {
  id: string;
  received: number;
  total: number;
  done: boolean;
  error: string;
}

/** Batch-level translation progress (mirrors commands::TranslateEvent). The
 *  start/done events also carry the translated title (empty when unavailable). */
export type TranslateEvent =
  | { type: "start"; data: { total: number; title?: string } }
  | { type: "batch"; data: { html: string; done: number } }
  | { type: "done"; data: { html: string; title?: string } };

export type RefreshProgress =
  | { event: "started"; data: { total: number } }
  | {
      event: "feedDone";
      data: { feedId: number; newArticles: number; error: string | null };
    }
  | { event: "finished"; data: { newArticles: number } };
