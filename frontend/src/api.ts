// Typed wrappers over the Wails service bindings (frontend/bindings/hive/services).
// This is the Vue port of papr's src/api.ts: it keeps the same function names and
// shapes the components expect, but calls Wails-generated service methods instead
// of Tauri `invoke`, and replaces Tauri `Channel<T>` streaming with Wails events
// (a uniquely-named event per request, subscribed via the runtime Events API).

import { Events } from "@wailsio/runtime";
import {
  AIService,
  ArticleService,
  FeedService,
  FolderService,
  FontService,
  HighlightService,
  NewsletterService,
  OpmlService,
  PageViewService,
  RuleService,
  SettingService,
  StorageService,
  SyncService,
  SystemService,
  TagService,
} from "../bindings/hive/services";
import type {
  AiChatTurn,
  AiEvent,
  AppInfo,
  ArticleDetail,
  ArticlePreviewTranslation,
  ArticleQuery,
  ArticleSummary,
  DirectorySource,
  DirectoryStatus,
  DiscoveryResult,
  Feed,
  FeedPreview,
  Folder,
  Highlight,
  HighlightWithContext,
  NewsletterInput,
  NewsletterSource,
  RefreshProgress,
  Rule,
  RuleAction,
  RuleField,
  SmartCounts,
  Tag,
  TranslateEvent,
  TranslationCache,
  UpdateInfo,
} from "./types";
import { createReorderBuffer } from "./lib/streamOrder";
import {
  pendingFeedDeletes,
  pendingFolderDeletes,
  pendingTagDeletes,
} from "./lib/pendingDeletes";
import type { FontProgress } from "./types";
import type { InstalledFont } from "../bindings/hive/internal/db/models";

// ── streaming helper ──
/** Subscribe to a per-request event, run the bound streaming call, and tear the
 *  subscription down when it settles. Wails has no per-call Channel, so the
 *  backend emits on `"<base>:<id>"` and we listen for the lifetime of the call.
 *
 *  Wails v3 (alpha) dispatches every backend Emit on its own goroutine, so
 *  consecutive emits race and can arrive out of order — for a token stream that
 *  scrambles the text. The backend stamps each event with a monotonic sequence
 *  number (events.Sequencer) wrapped as `{ seq, data }`; here we buffer by that
 *  number and release only a contiguous prefix, so `onEvent` always sees the
 *  original emit order. */
/** A running stream: awaitable like a Promise, and stoppable via `cancel()`.
 *  `cancel` aborts the backend call — Wails cancels the Go context, so a
 *  streamed generation actually stops instead of running to completion (or the
 *  300s timeout) in the background, burning API quota, after the UI moved on. */
export interface StreamHandle extends Promise<void> {
  cancel(): void;
}

function stream<T>(
  base: string,
  onEvent: (e: T) => void,
  call: (id: string) => Promise<unknown>,
): StreamHandle {
  const id = crypto.randomUUID();
  const buf = createReorderBuffer<T>(onEvent);
  const off = Events.On(`${base}:${id}`, (ev: { data: unknown }) => {
    // Wails delivers the payload as `ev.data`; the backend wraps it as
    // `{ seq, data }` (events.Sequencer) so we can restore order.
    const env: any = (ev && (ev as any).data) ?? ev;
    if (typeof env?.seq === "number") buf.push(env.seq, env.data as T);
    // A payload with no seq can only come from a non-sequenced emit on this
    // name; deliver it as-is rather than drop it.
    else if (env != null) onEvent(env as T);
  });
  // Keep the raw call result: a Wails-generated streaming method returns a
  // CancellablePromise whose `.cancel()` cancels the backend context.
  const pending = call(id) as Promise<unknown> & { cancel?: () => void };
  const done = Promise.resolve(pending)
    .then(() => void 0)
    .finally(() => {
      // The call has settled, so every event has been emitted. Flush any
      // out-of-order tail still buffered, then unsubscribe.
      buf.flush();
      off();
    }) as StreamHandle;
  done.cancel = () => {
    try {
      pending.cancel?.();
    } catch {
      /* not cancellable or already settled — nothing to stop */
    }
  };
  return done;
}

// ── folders ──
// NOTE: Go returns a nil slice for an empty result, which crosses the Wails
// bridge as `null`. Every list endpoint coerces null → [] here so no consumer
// can crash on `.map`/`.flat`/`.length` (see the listArticles note below).
// listFolders / listFeeds / listTags additionally drop rows whose delete sits
// inside its 6s undo window (lib/pendingDeletes.ts): the backend still has
// those rows until the deferred delete commits, so an unfiltered mid-window
// refetch would resurrect them in the sidebar ("vanish → reappear → vanish").
export const listFolders = () =>
  (FolderService.ListFolders() as Promise<Folder[] | null>).then((r) =>
    (r ?? []).filter((f) => !pendingFolderDeletes.has(f.id)),
  );
export const createFolder = (name: string) => FolderService.CreateFolder(name) as Promise<number>;
export const renameFolder = (id: number, name: string) => FolderService.RenameFolder(id, name) as Promise<void>;
export const deleteFolder = (id: number) => FolderService.DeleteFolder(id) as Promise<void>;

// ── images ──
/** Fetch an image's raw bytes via the backend (which walks Referer fallbacks).
 *  The Go side returns the bytes base64-encoded; decode to a Uint8Array. */
export async function fetchImage(url: string, pageUrl?: string | null): Promise<Uint8Array> {
  const b64 = (await SystemService.FetchImage(url, pageUrl ?? null)) as unknown as string;
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return bytes;
}

/** "Save image" via a native save dialog, entirely in Go (fetch → dialog →
 *  write). Returns whether a file was actually written — false when the user
 *  cancels. Kept in Go so a large image never round-trips the JS bridge as a
 *  base64 blob. */
export const saveImage = (url: string, pageUrl?: string | null) =>
  SystemService.SaveImage(url, pageUrl ?? null) as Promise<boolean>;

// ── feeds ──
export const listFeeds = () =>
  (FeedService.ListFeeds() as Promise<Feed[] | null>).then((r) =>
    (r ?? [])
      .filter((f) => !pendingFeedDeletes.has(f.id))
      // A folder mid-deletion re-parents its feeds to "uncategorized" in the
      // UI (the DB FK is ON DELETE SET NULL) — mirror that on refetches too.
      .map((f) =>
        f.folderId != null && pendingFolderDeletes.has(f.folderId)
          ? { ...f, folderId: null }
          : f,
      ),
  );
export const addFeed = (url: string, folderId: number | null) =>
  FeedService.AddFeed(url, folderId) as Promise<Feed>;
/** Fetch-free subscribe from directory metadata — bulk adds; articles arrive
 *  via the background refresh that follows. */
export const quickSubscribe = (r: DiscoveryResult, folderId: number | null) =>
  FeedService.QuickSubscribe(r.feedUrl, r.title, r.siteUrl, r.description, folderId) as Promise<Feed>;
export const searchFeedDirectory = (query: string, lang: string) =>
  (FeedService.SearchFeedDirectory(query, lang) as Promise<DiscoveryResult[] | null>).then((r) => r ?? []);
export const previewFeed = (url: string) => FeedService.PreviewFeed(url) as Promise<FeedPreview>;
export const directoryStatus = () => FeedService.DirectoryStatus() as Promise<DirectoryStatus>;
export const refreshFeedDirectory = () =>
  FeedService.RefreshFeedDirectory() as Promise<DirectoryStatus>;
export const listDirectorySources = () =>
  (FeedService.ListDirectorySources() as Promise<DirectorySource[] | null>).then((r) => r ?? []);
export const addDirectorySource = (url: string) =>
  (FeedService.AddDirectorySource(url) as Promise<DirectorySource[] | null>).then((r) => r ?? []);
export const removeDirectorySource = (url: string) =>
  (FeedService.RemoveDirectorySource(url) as Promise<DirectorySource[] | null>).then((r) => r ?? []);
export const deleteFeed = (id: number) => FeedService.DeleteFeed(id) as Promise<void>;
/** Batch unsubscribe (settings multi-select) — one IPC call, chunked SQL. */
export const deleteFeeds = (ids: number[]) => FeedService.DeleteFeeds(ids) as Promise<void>;
export const moveFeed = (id: number, folderId: number | null) =>
  FeedService.MoveFeed(id, folderId) as Promise<void>;
export const renameFeed = (id: number, title: string) => FeedService.RenameFeed(id, title) as Promise<void>;
/** Repoint a feed at a new URL (the site moved its feed), keeping its articles,
 *  folder and per-feed settings. Rejects an empty or already-subscribed URL. */
export const setFeedUrl = (id: number, url: string) => FeedService.SetFeedURL(id, url) as Promise<void>;
export const setFeedRefreshInterval = (id: number, minutes: number | null) =>
  FeedService.SetFeedRefreshInterval(id, minutes) as Promise<void>;
/** Per-feed auto-translate override: null follows global, 0 off, 1 on. */
export const setFeedAutoTranslate = (id: number, mode: number | null) =>
  FeedService.SetFeedAutoTranslate(id, mode) as Promise<void>;
/** Per-feed open mode ("reader" / "extracted" / "web"). null reverts the feed
 *  to the global default-open-mode preference. */
export const setFeedOpenMode = (id: number, mode: "reader" | "extracted" | "web" | null) =>
  FeedService.SetFeedOpenMode(id, mode) as Promise<void>;

/** Refresh feeds, reporting progress through the supplied callback. With no
 *  `scope` this refreshes every feed; pass `{ feedId }` for a single feed or
 *  `{ folderId }` for every feed in one folder. */
export function refreshFeeds(
  onProgress?: (p: RefreshProgress) => void,
  scope?: { feedId?: number; folderId?: number },
): Promise<number> {
  let count = 0;
  return stream<RefreshProgress>(
    "refresh",
    (p) => {
      if (p.event === "finished") count = p.data.newArticles;
      onProgress?.(p);
    },
    (id) =>
      FeedService.RefreshFeeds(scope?.feedId ?? null, scope?.folderId ?? null, id).then(
        (n) => (count = n as number),
      ),
  ).then(() => count);
}

// ── articles ──
/** The generated binding's ArticleQuery requires `value` (number | null), but
 *  our discriminated union omits it for the smart views ({ kind: "all" } etc.).
 *  Normalize at the boundary so a smart-view query crosses with value = null. */
function wireQuery(q: ArticleQuery): { kind: string; value: number | null } {
  return { kind: q.kind, value: "value" in q && q.value != null ? q.value : null };
}

export const listArticles = (
  query: ArticleQuery,
  unreadOnly: boolean,
  search: string | null,
  oldestFirst: boolean,
  limit: number,
  offset: number,
) =>
  // The Go backend returns a nil slice for an empty result, which crosses the
  // Wails bridge as `null`. Coerce to [] so an empty page never lands in the
  // infinite query's `pages` array as null — otherwise consumers that map/flat
  // over pages (articleActions.patch, getNextPageParam, currentList) crash with
  // "null is not an object (evaluating 'page.map')".
  (
    ArticleService.ListArticles(
      wireQuery(query),
      unreadOnly,
      search,
      oldestFirst,
      limit,
      offset,
    ) as Promise<ArticleSummary[] | null>
  ).then((rows) => rows ?? []);
export const getArticle = (id: number) => ArticleService.GetArticle(id) as Promise<ArticleDetail>;
/** Every cached (lang, engine) rendering of an article, for instant re-display. */
export const getTranslations = (id: number) =>
  (ArticleService.Translations(id) as Promise<TranslationCache[] | null>).then((r) => r ?? []);
export const markRead = (id: number, read: boolean) => ArticleService.MarkRead(id, read) as Promise<void>;
export const markStarred = (id: number, starred: boolean) =>
  ArticleService.MarkStarred(id, starred) as Promise<void>;
export const markReadLater = (id: number, value: boolean) =>
  ArticleService.MarkReadLater(id, value) as Promise<void>;
export const markAllRead = (query: ArticleQuery) => ArticleService.MarkAllRead(wireQuery(query)) as Promise<number>;
export const smartCounts = () => ArticleService.SmartCounts() as Promise<SmartCounts>;

// ── full-text extraction ──
export const extractFulltext = (articleId: number) =>
  ArticleService.ExtractFulltext(articleId) as Promise<string>;

// ── OPML ──
export const importOpml = (content: string) => OpmlService.ImportOpml(content) as Promise<number>;
export const exportOpml = () => OpmlService.ExportOpml() as Promise<string>;

// ── AI ──
/** What a successful `testAi` round trip saw (mirrors ai.TestOutcome). */
export interface AiTestOutcome {
  model: string;
  baseUrl: string;
  latencyMs: number;
  reply: string;
}
/** Send one minimal request to the given provider and report what came back.
 *  Takes the values currently in the settings form so the test works before
 *  saving; empty model/URL fields use provider defaults. */
export const testAi = (args: { provider: string; apiKey: string; model: string; baseUrl: string }) =>
  AIService.TestConnection(args.provider, args.apiKey, args.model, args.baseUrl) as Promise<AiTestOutcome>;

// ── AI (streaming over events) ──
export function aiSummarize(articleId: number, onToken: (e: AiEvent) => void): Promise<void> {
  return stream<AiEvent>("ai", onToken, (id) => AIService.Summarize(articleId, id));
}
/** Ask a RAG question over the user's subscriptions, streaming the answer.
 *  `history` is the prior conversation (empty for a first question) so a
 *  follow-up keeps its context. Returns a StreamHandle: await it for
 *  completion, or call `.cancel()` to stop generation (e.g. a Stop button). */
export function aiAsk(
  question: string,
  history: AiChatTurn[],
  onToken: (e: AiEvent) => void,
): StreamHandle {
  return stream<AiEvent>("ai", onToken, (id) => AIService.Ask(question, history, id));
}
/** Plain assistant chat — no RAG / article context. Same streaming + cancel
 *  shape as aiAsk; `history` keeps the conversation for follow-ups. */
export function aiChat(
  question: string,
  history: AiChatTurn[],
  onToken: (e: AiEvent) => void,
): StreamHandle {
  return stream<AiEvent>("ai", onToken, (id) => AIService.Chat(question, history, id));
}
export function aiDigest(onToken: (e: AiEvent) => void): Promise<void> {
  return stream<AiEvent>("ai", onToken, (id) => AIService.Digest(id));
}
export function aiTranslate(
  articleId: number,
  lang: string,
  engine: string,
  onEvent: (e: TranslateEvent) => void,
): Promise<void> {
  return stream<TranslateEvent>("translate", onEvent, (id) =>
    AIService.Translate(articleId, lang, engine, id),
  );
}

// ── settings ──
export const getSetting = (key: string) => SettingService.GetSetting(key) as Promise<string | null>;
export const setSetting = (key: string, value: string) => SettingService.SetSetting(key, value) as Promise<void>;

/** Translate an article's list preview (title + snippet) into lang via
 *  engine, cached backend-side per (article, lang, engine). */
export const translateArticlePreview = (id: number, lang: string, engine: string) =>
  AIService.TranslateArticlePreview(id, lang, engine) as unknown as Promise<ArticlePreviewTranslation>;

/** Repaint the native window backing (macOS resize strips) to the theme's
 *  paper colour. */
export const setWindowBackground = (r: number, g: number, b: number) =>
  SystemService.SetWindowBackground(r, g, b) as Promise<void>;
// ── fonts (downloadable fonts are added by URL; there is no built-in catalog) ──
export const listInstalledFonts = () =>
  (FontService.ListInstalled() as Promise<InstalledFont[] | null>).then((r) => r ?? []);
export const deleteFont = (id: string) => FontService.DeleteFont(id) as Promise<void>;
/** Download an open-source font by URL (e.g. a GitHub release asset), reporting
 *  progress over the "font" event channel. */
export function addCustomFont(
  url: string,
  family: string,
  axis: string,
  onProgress: (p: FontProgress) => void,
): Promise<void> {
  return stream<FontProgress>("font", onProgress, (sid) => FontService.AddCustomFont(url, family, axis, sid));
}

// ── storage ──
export interface StorageStats {
  dbBytes: number;
  articleCount: number;
  feedCount: number;
}
export const storageStats = () => StorageService.StorageStats() as Promise<StorageStats>;
export const cleanupArticles = (days: number) => StorageService.CleanupArticles(days) as Promise<number>;
export const vacuumDb = () => StorageService.VacuumDB() as Promise<void>;
export const resetSettings = () => StorageService.ResetSettings() as Promise<void>;
export const clearAllData = () => StorageService.ClearAllData() as Promise<void>;

// ── network ──
export const applyNetworkSettings = () => SettingService.ApplyNetworkSettings() as Promise<void>;

// ── GReader sync (FreshRSS / Miniflux) ──
export type GReaderProvider = "freshrss" | "miniflux";
export interface FreshRssStatus {
  connected: boolean;
  url: string | null;
  provider: GReaderProvider;
}
export const freshrssStatus = () => SyncService.FreshrssStatus() as Promise<FreshRssStatus>;
export const freshrssConnect = (
  url: string,
  username: string,
  password: string,
  provider: GReaderProvider = "freshrss",
) => SyncService.FreshrssConnect(url, username, password, provider) as Promise<void>;
export const freshrssDisconnect = () => SyncService.FreshrssDisconnect() as Promise<void>;
export const freshrssSync = () => SyncService.FreshrssSync() as Promise<number>;

// ── app identity / update check ──
export const appInfo = () => SystemService.AppInfo() as Promise<AppInfo>;
export const checkForUpdate = () => SystemService.CheckForUpdate() as Promise<UpdateInfo>;

// ── tray ──
export const refreshTray = () => SystemService.RefreshTray() as Promise<void>;

// ── deep links ──
export const takePendingDeepLink = () => SystemService.TakePendingDeepLink() as Promise<string | null>;

// ── window fullscreen ──
// Toggles system-level fullscreen (macOS: full-screen space, hides menu bar
// + traffic lights; Windows/Linux: frameless full-monitor). Returns the
// resulting state so the UI ref can sync to the backend.
export const toggleFullscreen = () => SystemService.ToggleFullscreen() as Promise<boolean>;
export const isFullscreen = () => SystemService.IsFullscreen() as Promise<boolean>;

// ── tags ──
export const listTags = () =>
  (TagService.ListTags() as Promise<Tag[] | null>).then((r) =>
    (r ?? []).filter((tg) => !pendingTagDeletes.has(tg.id)),
  );
export const createTag = (name: string) => TagService.CreateTag(name) as Promise<number>;
export const renameTag = (id: number, name: string) => TagService.RenameTag(id, name) as Promise<void>;
export const setTagColor = (id: number, color: string) => TagService.SetTagColor(id, color) as Promise<void>;
export const deleteTag = (id: number) => TagService.DeleteTag(id) as Promise<void>;
export const reorderTags = (ids: number[]) => TagService.ReorderTags(ids) as Promise<void>;
export const setArticleTag = (articleId: number, tagId: number, on: boolean) =>
  TagService.SetArticleTag(articleId, tagId, on) as Promise<void>;

// ── filter rules ──
export const listRules = () =>
  (RuleService.ListRules() as Promise<Rule[] | null>).then((r) => r ?? []);
export const createRule = (
  name: string,
  feedId: number | null,
  field: RuleField,
  query: string,
  action: RuleAction,
) => RuleService.CreateRule(name, feedId, field, query, action) as Promise<number>;
export const updateRule = (
  id: number,
  name: string,
  enabled: boolean,
  feedId: number | null,
  field: RuleField,
  query: string,
  action: RuleAction,
) => RuleService.UpdateRule(id, name, enabled, feedId, field, query, action) as Promise<void>;
export const deleteRule = (id: number) => RuleService.DeleteRule(id) as Promise<void>;
export const previewRule = (feedId: number | null, field: RuleField, query: string) =>
  (
    RuleService.PreviewRule(feedId, field, query) as Promise<{ count: number; samples: string[] | null } | null>
  ).then((r) => ({ count: r?.count ?? 0, samples: r?.samples ?? [] }));
export const applyRuleToExisting = (
  feedId: number | null,
  field: RuleField,
  query: string,
  action: RuleAction,
) => RuleService.ApplyRuleToExisting(feedId, field, query, action) as Promise<number>;

// ── highlights / annotations ──
export interface NewHighlight {
  articleId: number;
  quote: string;
  prefix: string;
  suffix: string;
  textOffset: number;
  color: string;
  note: string;
}
export const createHighlight = (h: NewHighlight) =>
  HighlightService.CreateHighlight(h.articleId, h.quote, h.prefix, h.suffix, h.textOffset, h.color, h.note) as Promise<number>;
export const listHighlights = (articleId: number) =>
  (HighlightService.ListHighlights(articleId) as Promise<Highlight[] | null>).then((r) => r ?? []);
export const listAllHighlights = () =>
  (HighlightService.ListAllHighlights() as Promise<HighlightWithContext[] | null>).then((r) => r ?? []);
export const searchHighlights = (query: string) =>
  (HighlightService.SearchHighlights(query) as Promise<HighlightWithContext[] | null>).then((r) => r ?? []);
export const exportAllHighlightsMarkdown = () =>
  HighlightService.ExportAllMarkdown() as Promise<string>;
/** Batch delete for the browser's multi-select (one IPC call, chunked SQL). */
export const deleteHighlights = (ids: number[]) =>
  HighlightService.DeleteHighlights(ids) as Promise<void>;
export const searchHighlightsInRange = (fromISO: string, toISO: string) =>
  (HighlightService.SearchInRange(fromISO, toISO) as Promise<HighlightWithContext[] | null>).then((r) => r ?? []);
export const exportRangeMarkdown = (fromISO: string, toISO: string) =>
  HighlightService.ExportRangeMarkdown(fromISO, toISO) as Promise<string>;
export const updateHighlightNote = (id: number, note: string) =>
  HighlightService.UpdateHighlightNote(id, note) as Promise<void>;
export const setHighlightColor = (id: number, color: string) =>
  HighlightService.SetHighlightColor(id, color) as Promise<void>;
export const deleteHighlight = (id: number) => HighlightService.DeleteHighlight(id) as Promise<void>;

// ── newsletter sources ──
export const addNewsletterSource = (input: NewsletterInput) =>
  NewsletterService.AddNewsletterSource(input) as Promise<Feed>;
export const listNewsletterSources = () =>
  (NewsletterService.ListNewsletterSources() as Promise<NewsletterSource[] | null>).then((r) => r ?? []);
export const removeNewsletterSource = (feedId: number) =>
  NewsletterService.RemoveNewsletterSource(feedId) as Promise<void>;

// ── in-app original-page view (issue #49) ──
// A frameless child window floated over the reader pane (Wails3 has no overlay
// child-webview). Bounds are logical CSS px relative to the main window content
// top-left — what getBoundingClientRect returns inside the main webview.
export interface PageViewBounds { x: number; y: number; width: number; height: number; }
export const openPageView = (url: string, b: PageViewBounds) =>
  PageViewService.OpenPageView(url, b.x, b.y, b.width, b.height) as Promise<void>;
export const setPageViewBounds = (b: PageViewBounds) =>
  PageViewService.SetPageViewBounds(b.x, b.y, b.width, b.height) as Promise<void>;
export const closePageView = () => PageViewService.ClosePageView() as Promise<void>;
// Temporarily hide / re-show the page-view window while a DOM modal is open, so
// the native child window doesn't float above in-webview overlays.
export const setPageViewSuspended = (suspended: boolean) =>
  PageViewService.SetPageViewSuspended(suspended) as Promise<void>;
