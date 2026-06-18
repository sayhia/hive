<script setup lang="ts">
// The reading pane. Shows the article body (sanitized contentHtml /
// extractedHtml, or a cached/streaming translation, via v-html), title, meta,
// and a toolbar (extract full-text, AI summary drawer, translate split-button +
// options popover, reading settings / Aa, star, read-later, tags,
// audio enclosure → player, open in browser). Mounts HighlightLayer over the
// body. Ported from papr's Reader.tsx (React → Vue 3 <script setup>).

import { useMutation, useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Browser, Events } from "@wailsio/runtime";
import * as api from "../api";
import { LANGUAGES } from "../i18n";
import { useUi, READER_BOUNDS, READER_DEFAULTS } from "../stores/ui";
import { usePlayer } from "../stores/player";
import { useTranslationJobs } from "../stores/translation";
import { reportError, toast } from "../stores/toasts";
import { useArticleActions } from "../composables/articleActions";
import { downloadBlob, imageFilename } from "../lib/download";
import { imageDataUrl } from "../lib/imageBytes";
import { fullDate } from "../lib/feedMeta";
import { isMac } from "../lib/platform";
import { tagColor } from "../lib/tagColors";
import { makeLinkClickHandler } from "../lib/readerLinks";
import { currentList } from "../lib/currentList";
import type { ArticleDetail } from "../types";
import Icon from "./Icon.vue";
import TagPicker from "./TagPicker.vue";
import HighlightLayer from "./HighlightLayer.vue";
import ContextMenu, { type MenuEntry } from "./ContextMenu.vue";
import AIDrawer from "./AIDrawer.vue";
import ImageViewer from "./ImageViewer.vue";
import ReaderSettingsPopover from "./ReaderSettingsPopover.vue";

// ── pure helpers (ported 1:1 from papr's Reader.tsx) ─────────────────────────

function youtubeId(url: string | null): string | null {
  if (!url) return null;
  const m = url.match(/[?&]v=([\w-]{11})/) || url.match(/youtu\.be\/([\w-]{11})/);
  return m ? m[1] : null;
}

function needsImageProxy(src: string): boolean {
  try {
    const host = new URL(src).hostname.toLowerCase();
    return host === "cdnfile.sspai.com" || host === "rssfile.sspai.com";
  } catch {
    return false;
  }
}

/** Plain, entity-decoded text of an HTML body — for the reading-time estimate. */
function bodyPlainText(html: string): string {
  if (!html) return "";
  // DOMParser documents are inert — nothing here executes or loads.
  return new DOMParser().parseFromString(html, "text/html").body.textContent ?? "";
}

/** CJK ideographs + Japanese kana + Korean Hangul — scripts read by the
 *  character, not the whitespace-delimited word. */
const CJK_CHAR = /[぀-ヿ㐀-鿿가-힯豈-﫿]/u;
/** Global-flagged variant of `CJK_CHAR` for stripping every CJK glyph. */
const CJK_CHAR_GLOBAL = new RegExp(CJK_CHAR.source, "gu");

/** Estimate reading time in minutes for an article body's plain text. A mixed-
 *  script estimate: CJK by the character (~480 chars/min), latin by the
 *  whitespace-delimited word (~220 wpm); the two contributions are summed. */
function estimateReadMinutes(text: string): number {
  let cjkChars = 0;
  for (const ch of text) {
    if (CJK_CHAR.test(ch)) cjkChars++;
  }
  const latinWords = text
    .replace(CJK_CHAR_GLOBAL, " ")
    .trim()
    .split(/\s+/)
    .filter(Boolean).length;
  const minutes = cjkChars / 480 + latinWords / 220;
  return Math.max(2, Math.round(minutes));
}

/** Generic wrappers that carry no readable text — mirrors translate.go's
 *  unwrapTags so the bilingual block split lines up with the backend's batches. */
const UNWRAP_TAGS = new Set(["DIV", "ARTICLE", "SECTION", "MAIN"]);

/** Split body HTML into its top-level blocks, unwrapping a lone generic
 *  container first (the "everything in one <div>" feed shape) so the blocks pair
 *  up with the translated body's blocks. DOMParser documents are inert. */
function topLevelBlocks(html: string): string[] {
  if (!html) return [];
  const doc = new DOMParser().parseFromString(html, "text/html");
  let root: Element = doc.body;
  for (;;) {
    const kids = Array.from(root.children);
    if (kids.length === 1 && UNWRAP_TAGS.has(kids[0].tagName)) {
      root = kids[0];
      continue;
    }
    break;
  }
  return Array.from(root.children).map((el) => el.outerHTML);
}

/** Interleave pre-split original + translated blocks for the bilingual view:
 *  each original block is followed by its translation. Blocks pair by index (the
 *  engine preserves block order); any unpaired tail (e.g. while a long
 *  translation is still streaming) shows the original alone. Takes already-split
 *  block arrays so the (stable, possibly large) original isn't re-parsed on every
 *  streaming batch — only the growing translation is. */
function interleaveBlocks(o: string[], tr: string[]): string {
  const n = Math.max(o.length, tr.length);
  let out = "";
  for (let i = 0; i < n; i++) {
    out += '<div class="bi-pair">';
    if (o[i]) out += `<div class="bi-src">${o[i]}</div>`;
    if (tr[i]) out += `<div class="bi-tr">${tr[i]}</div>`;
    out += "</div>";
  }
  return out;
}

/** Coarse "is this article in a different script than the target?" check for
 *  auto-translate: compares CJK vs. latin dominance in a body-text sample. It
 *  only distinguishes scripts, not languages (so en↔fr won't trip it), which is
 *  enough for the common case of reading a foreign-script feed. */
function scriptMismatch(text: string, target: string): boolean {
  let cjk = 0;
  let latin = 0;
  for (const ch of text.slice(0, 2000)) {
    if (CJK_CHAR.test(ch)) cjk++;
    else if (ch >= "A" && ch <= "z" && /[a-zA-Z]/.test(ch)) latin++;
  }
  if (cjk + latin < 20) return false; // too little signal to act on
  const cjkDominant = cjk > latin;
  // Targets zh/ja want CJK output; anything else (en, …) wants latin.
  return target === "zh" || target === "ja" ? !cjkDominant : cjkDominant;
}

// Success toasts route up to the shell (papr threaded an `onToast` prop; App.vue
// mounts `<Reader @toast="…" />`). Error toasts go straight to the global store,
// the way papr called `toast.error` / `reportError` directly.
const emit = defineEmits<{
  (e: "toast", msg: string): void;
  (e: "open-settings", section?: string): void;
}>();

// ── stores / query client ────────────────────────────────────────────────────
const { t, locale } = useI18n();
const qc = useQueryClient();
const ui = useUi();
const player = usePlayer();
const jobs = useTranslationJobs();
const actions = useArticleActions(toast.error);

const id = computed(() => ui.selectedArticleId);

// ── component state ──────────────────────────────────────────────────────────
const scrolled = ref(false);
// 0..1 reading position, drives the thin progress bar at the top of the reader.
const progress = ref(0);
// Which body to show when an extraction exists follows the "auto-extract"
// setting: off (the default) shows the feed's own content and extraction is
// opt-in via the toolbar button; on shows the extracted full text.
const showExtracted = ref(ui.prefs.defaultOpenMode === "extracted");
// Translation display mode: the original, the full translation, or a bilingual
// (interleaved original + translation) view. `showTranslation` is the read-only
// "showing some translation" shorthand used across the toolbar/body logic.
type TransView = "original" | "translation" | "bilingual";
const transView = ref<TransView>("original");
const showTranslation = computed(() => transView.value !== "original");
// The last non-original mode shown, so the toolbar button toggles back into it
// (a user reading bilingual gets bilingual back, not plain translation).
const lastMode = ref<Exclude<TransView, "original">>("translation");
watch(transView, (v) => {
  if (v !== "original") lastMode.value = v;
});
// Whether the translate popover (engine / language / mode / auto) is open.
const transMenu = ref(false);
// In-reader Aa panel (font / size / leading / width / theme).
const typeMenu = ref(false);
const moreMenu = ref(false);
const morePopRef = ref<HTMLDivElement>();
const tagPick = ref<{ x: number; y: number } | null>(null);
const ctxMenu = ref<{
  x: number;
  y: number;
  // Set when the right-click landed on an article image / over a text selection.
  imageUrl?: string;
  selection?: string;
} | null>(null);
const heroBroken = ref(false);
// data: URL of a hero image recovered through the backend after the webview
// failed to load it directly. A data: URL (not blob:) so the bytes stay inline
// and survive the webview dropping blob backing data under memory pressure —
// the same fix as body images.
const heroDataUrl = ref<string | null>(null);
const proxiedBody = ref<{ source: string; html: string } | null>(null);
// Reading vs. the article's original web page, shown in a native child window
// floated over the reader pane (Wails3 has no overlay child-webview).
const viewMode = ref<"reader" | "web">("reader");

const scrollRef = ref<HTMLDivElement>();
const bodyRef = ref<HTMLDivElement>();
// Host element the native original-page child window is positioned over.
const pageHostRef = ref<HTMLElement | null>(null);
// True while the original-page window is loading (it's kept hidden until the
// page finishes, so we show a spinner over the reserved host area meanwhile).
const pvLoading = ref(false);
// Article id we already auto-marked read via scroll.
let scrollMarked: number | null = null;
// The extracted article id auto-extract has already attempted.
let autoExtracted: number | null = null;

const playingSrc = computed(() => (player.playing ? player.track?.src ?? null : null));

// ── article detail query ─────────────────────────────────────────────────────
const article = useQuery({
  queryKey: ["article", id],
  queryFn: () => api.getArticle(id.value as number),
  enabled: computed(() => id.value != null),
});
const a = computed<ArticleDetail | undefined>(() => article.data.value);

const readMinutes = computed(() =>
  estimateReadMinutes(bodyPlainText(a.value?.extractedHtml || a.value?.contentHtml || "")),
);

// ── translation settings (default target language + engine) ──────────────────
const translateSetting = useQuery({
  queryKey: ["setting", "translate_target_lang"],
  queryFn: () => api.getSetting("translate_target_lang"),
});
const defaultLang = computed(() => translateSetting.data.value || locale.value);
const engineSetting = useQuery({
  queryKey: ["setting", "translate_engine"],
  queryFn: () => api.getSetting("translate_engine"),
});
const defaultEngine = computed(() => engineSetting.data.value || "llm");
// Global auto-translate default; a feed may override it (a.feedAutoTranslate).
const autoSetting = useQuery({
  queryKey: ["setting", "translate_auto"],
  queryFn: () => api.getSetting("translate_auto"),
});
// `null` = follow the default; a string = a temporary per-article override.
const tmpLang = ref<string | null>(null);
const tmpEngine = ref<string | null>(null);
const targetLang = computed(() => tmpLang.value ?? defaultLang.value);
const engine = computed(() => tmpEngine.value ?? defaultEngine.value);

// Background translation jobs run independently of this view.
const job = computed(() => (id.value != null ? jobs.jobs[id.value] : undefined));

// Cached (lang, engine) renderings for this article — a previously translated
// copy shows instantly, no new job (⑤). Re-fetched when a job finishes.
const translationsQuery = useQuery({
  queryKey: computed(() => ["translations", id.value]),
  queryFn: () => api.getTranslations(id.value as number),
  enabled: computed(() => id.value != null),
});
const cachedEntry = computed(() =>
  (translationsQuery.data.value ?? []).find(
    (tc) => tc.lang === targetLang.value && tc.engine === engine.value,
  ),
);

// The LLM engine needs an AI key; the keyless MT engines (Google/DeepL/Bing)
// always work. Drives a hint in the popover so picking "AI model" with no key
// configured isn't a silent dead-end.
const aiKeyQuery = useQuery({
  queryKey: ["setting", "ai_api_key"],
  queryFn: () => api.getSetting("ai_api_key"),
});
const llmNeedsKey = computed(() => engine.value === "llm" && !aiKeyQuery.data.value);

// ── derived body + translation state ─────────────────────────────────────────
function extractedBeatsFeed(art: ArticleDetail | undefined): boolean {
  if (!art?.extractedHtml) return false;
  const extractedLen = bodyPlainText(art.extractedHtml).trim().length;
  const feedLen = bodyPlainText(art.contentHtml || "").trim().length;
  return extractedLen >= 80 && extractedLen >= feedLen + 80;
}
const hasExtracted = computed(() => extractedBeatsFeed(a.value));
const canTranslate = computed(() => !!(a.value?.extractedHtml || a.value?.contentHtml));
// Article id whose auto-extract last failed or only returned the feed stub —
// don't loop. The body stays on the feed excerpt with no extra banner.
const extractFailedId = ref<number | null>(null);
/** True when default/per-feed open mode wants the page's full text and the
 *  feed body is only a stub. Kick off extract in the background. */
function articleNeedsExtract(art: ArticleDetail | undefined): boolean {
  const mode = art?.feedOpenMode ?? ui.prefs.defaultOpenMode;
  if (mode !== "extracted" || !art || !art.url || art.extractedHtml) return false;
  if (art.feedOpenMode !== "extracted") {
    const plain = bodyPlainText(art.contentHtml || "").trim();
    if (plain.length >= 800) return false;
  }
  return true;
}
const baseBody = computed(() => {
  if (showExtracted.value && extractedBeatsFeed(a.value)) return a.value!.extractedHtml!;
  return a.value?.contentHtml || "";
});
// Original split into blocks, memoised — bilingual reuses this across the many
// re-renders a streaming translation triggers instead of re-parsing the body.
const baseBlocks = computed(() => topLevelBlocks(baseBody.value));
// A job matches only when BOTH the target language AND the engine agree (⑤).
const jobForTarget = computed(() =>
  job.value && job.value.lang === targetLang.value && job.value.engine === engine.value
    ? job.value
    : undefined,
);
const translating = computed(() => jobForTarget.value?.status === "translating");
// The live job's HTML wins over the cache (it's the fresher, in-progress copy).
const translatedBody = computed(() => jobForTarget.value?.html || cachedEntry.value?.html || "");
const translatedTitle = computed(() => jobForTarget.value?.title || cachedEntry.value?.title || "");
const hasTranslation = computed(() => !!translatedBody.value);
// The heading: the translated title while a translation/bilingual view is shown.
const displayTitle = computed(() =>
  showTranslation.value && translatedTitle.value ? translatedTitle.value : a.value?.title ?? "",
);
const body = computed(() => {
  if (!showTranslation.value) return baseBody.value;
  const tr = translatedBody.value;
  // Before the first batch lands, keep the original readable (the progress bar
  // signals the work) instead of flashing an empty "translating" placeholder —
  // in bilingual, that's the original blocks awaiting their translations.
  if (!tr) {
    return transView.value === "bilingual" ? interleaveBlocks(baseBlocks.value, []) : baseBody.value;
  }
  return transView.value === "bilingual" ? interleaveBlocks(baseBlocks.value, topLevelBlocks(tr)) : tr;
});
const displayBody = computed(() =>
  proxiedBody.value?.source === body.value ? proxiedBody.value.html : body.value,
);

// Effective auto-translate: the feed override, else the global setting (④).
const autoTranslateOn = computed(() => {
  const f = a.value?.feedAutoTranslate;
  if (f === 0) return false;
  if (f === 1) return true;
  return autoSetting.data.value === "1";
});

const ytId = computed(() => (a.value?.sourceType === "youtube" ? youtubeId(a.value.url) : null));

// Effective open mode: the owning feed's override (rides on the article
// payload, like feedAutoTranslate), else the global default. Only meaningful
// once the article has loaded.
const effectiveOpenMode = computed(
  () => a.value?.feedOpenMode ?? ui.prefs.defaultOpenMode,
);
// The article id the open mode has been applied for this open (see below).
let openModeApplied: number | null = null;

// ── reset scroll + extraction view on article change ─────────────────────────
watch(id, () => {
  openModeApplied = null;
  showExtracted.value = ui.prefs.defaultOpenMode === "extracted";
  transView.value = "original";
  transMenu.value = false;
  typeMenu.value = false;
  moreMenu.value = false;
  viewMode.value = "reader";
  scrolled.value = false;
  showToTop.value = false;
  progress.value = 0;
  tagPick.value = null;
  heroBroken.value = false;
  heroDataUrl.value = null;
  scrollMarked = null;
  tmpLang.value = null;
  tmpEngine.value = null;
  nextTick(() => {
    if (scrollRef.value) scrollRef.value.scrollTop = 0;
    progress.value = 0;
    scrolled.value = false;
    showToTop.value = false;
  });
});

// Apply the effective open mode once per article-open, after the detail
// payload (which carries feedOpenMode) is available. Declared after the reset
// watcher so it wins their shared trigger; the once-per-open guard keeps the
// toolbar toggles working afterwards and stops a refetch from yanking the
// view back. Watches id as well as the payload so reopening the same article
// (payload already cached, no id transition on `a`) still applies the mode.
watch(
  [id, () => a.value?.id],
  () => {
    const art = a.value;
    if (!art || art.id !== id.value || openModeApplied === art.id) return;
    openModeApplied = art.id;
    if (effectiveOpenMode.value === "web" && art.url) viewMode.value = "web";
    else showExtracted.value = effectiveOpenMode.value === "extracted";
  },
  { immediate: true },
);

// ── drive the native original-page child window while in web mode ────────────
// It floats above the DOM, so we measure the host rect and keep the window
// aligned to it across window/sidebar resizes. (Switching articles resets
// viewMode to "reader" above, so the URL is stable within a session.) `flush:
// "post"` so `.reader-webview-host` exists in the DOM when viewMode → "web".
const articleUrl = computed(() => a.value?.url ?? null);
watch(
  [viewMode, articleUrl],
  (_v, _o, onCleanup) => {
    const host = pageHostRef.value;
    if (viewMode.value !== "web" || !articleUrl.value || !host) return;
    pvLoading.value = true;
    // The page-view window stays hidden until its page finishes loading; the Go
    // side emits this once it's ready, so we drop the spinner as the window
    // appears (no black/white flash, just spinner → page).
    const offReady = Events.On("page-view-ready", () => {
      pvLoading.value = false;
    });
    const bounds = () => {
      const r = host.getBoundingClientRect();
      return { x: r.left, y: r.top, width: r.width, height: r.height };
    };
    let open = false;
    let cancelled = false;
    api.openPageView(articleUrl.value, bounds()).then(
      () => {
        open = true;
        // If teardown ran while this open was still in flight, the cleanup's
        // closePageView fired against a not-yet-created window (a no-op) and
        // would leave this one orphaned above the DOM — close it now.
        if (cancelled) api.closePageView().catch(() => {});
      },
      () => {},
    );
    const sync = () => {
      if (open && !cancelled) api.setPageViewBounds(bounds()).catch(() => {});
    };
    const ro = new ResizeObserver(sync);
    ro.observe(host);
    window.addEventListener("resize", sync);
    onCleanup(() => {
      cancelled = true;
      pvLoading.value = false;
      offReady();
      ro.disconnect();
      window.removeEventListener("resize", sync);
      api.closePageView().catch(() => {});
    });
  },
  { flush: "post" },
);

// ── recover article-body images the webview fails to load ────────────────────
// The webview sends no Referer — right for blacklist-style hotlink protection
// but fatal on hosts that *require* one. A failed image gets one retry through
// the backend, then is hidden; the <img> is swapped to an inline data: URL,
// with the original kept in data-papr-src for the context-menu actions. A data:
// URL (not blob:) is deliberate — WKWebView/WebView2 silently drop a blob:'s
// backing data under memory pressure (e.g. layer recompositing while
// scrolling), so a recovered image carried by a blob: vanishes when the user
// scrolls away and back; the inline data: bytes always repaint. Proxy-eligible
// images are already rewritten up front by the proxiedBody watch, so they are
// not refetched here.
watch(
  [
    () => a.value?.id,
    () => a.value?.url,
    showExtracted,
    () => a.value?.extractedHtml,
    showTranslation,
    () => translatedBody.value,
    displayBody,
  ],
  (_v, _o, onCleanup) => {
    const el = bodyRef.value;
    if (!el) return;
    const pageUrl = a.value?.url;
    const timers: number[] = [];
    let alive = true;
    const recover = async (img: HTMLImageElement) => {
      const src = img.getAttribute("src") || "";
      if (img.dataset.paprRetried || !/^https?:\/\//.test(src)) {
        img.style.display = "none";
        return;
      }
      img.dataset.paprRetried = "1";
      try {
        const buf = await api.fetchImage(src, pageUrl);
        if (!alive) return;
        img.dataset.paprSrc = src;
        img.src = imageDataUrl(src, buf);
      } catch {
        img.style.display = "none";
      }
    };
    const recoverIfBroken = (img: HTMLImageElement) => {
      if (img.dataset.paprRetried) return;
      if (img.complete && img.naturalWidth === 0) void recover(img);
    };
    const onError = (e: Event) => void recover(e.currentTarget as HTMLImageElement);
    const watched: HTMLImageElement[] = [];
    el.querySelectorAll("img").forEach((img) => {
      img.addEventListener("error", onError);
      watched.push(img);
      recoverIfBroken(img);
    });
    // The webview can finish a parser-inserted image before this binds, and not
    // every broken image reports that state synchronously. A few delayed sweeps
    // make the fallback deterministic without retrying images still loading.
    [250, 1000, 2500].forEach((delay) => {
      timers.push(window.setTimeout(() => watched.forEach(recoverIfBroken), delay));
    });
    onCleanup(() => {
      alive = false;
      timers.forEach(window.clearTimeout);
      watched.forEach((img) => img.removeEventListener("error", onError));
    });
  },
  { flush: "post" },
);

// ── proactive proxy for the reader hero ──────────────────────────────────────
watch(
  [() => a.value?.id, () => a.value?.imageUrl, () => a.value?.url, heroDataUrl, heroBroken],
  (_v, _o, onCleanup) => {
    const art = a.value;
    if (!art?.imageUrl || heroDataUrl.value || heroBroken.value || !needsImageProxy(art.imageUrl))
      return;
    let alive = true;
    const articleId = art.id;
    api
      .fetchImage(art.imageUrl, art.url)
      .then((buf) => {
        if (!alive || ui.selectedArticleId !== articleId) return;
        heroDataUrl.value = imageDataUrl(art.imageUrl!, buf);
      })
      .catch(() => {
        if (!alive || ui.selectedArticleId !== articleId) return;
        heroBroken.value = true;
      });
    onCleanup(() => {
      alive = false;
    });
  },
);

// ── mark as read once when an unread article is opened (opt-in) ──────────────
watch(
  () => a.value?.id,
  () => {
    const art = a.value;
    if (art && !art.isRead && ui.prefs.markReadOnOpen) actions.setRead(art.id, true);
  },
);

// ── extract full-text mutation ───────────────────────────────────────────────
// The extracted article id travels as the mutation variable, not via the `a`
// closure: extraction is async and the user can switch articles before it
// resolves.
const extract = useMutation({
  mutationFn: (articleId: number) => api.extractFulltext(articleId),
  onSuccess: (html, articleId) => {
    const prev = qc.getQueryData(["article", articleId]) as ArticleDetail | undefined;
    const extractedLen = bodyPlainText(html || "").trim().length;
    const feedLen = bodyPlainText(prev?.contentHtml || "").trim().length;
    const stillStub = extractedLen < 80 || extractedLen < feedLen + 80;
    if (stillStub) extractFailedId.value = articleId;
    qc.invalidateQueries({ queryKey: ["article", articleId] });
    // The backend dropped this article's now-stale (summary) translations when it
    // stored the full text — refetch so the cache doesn't keep showing one.
    qc.invalidateQueries({ queryKey: ["translations", articleId] });
    // The live store job also still holds the summary translation, and
    // translatedBody prefers it over the cache — drop it so it can't linger.
    jobs.clear(articleId);
    if (!stillStub && extractFailedId.value === articleId) extractFailedId.value = null;
    if (ui.selectedArticleId !== articleId) return;
    if (!stillStub) {
      showExtracted.value = true;
      emit("toast", t("reader.fullTextExtracted"));
    }
    if (showTranslation.value) {
      // A translation is on screen — re-translate the now-full text. Mark it
      // handled so the auto-translate watch doesn't also fire.
      autoTranslatedId = articleId;
      run(targetLang.value, engine.value, transView.value === "bilingual" ? "bilingual" : "translation");
    } else {
      // Not viewing a translation — let auto-translate re-evaluate the full text.
      autoTranslatedId = null;
    }
  },
  onError: (_e, articleId) => {
    extractFailedId.value = articleId;
  },
});

// ── proxy image URLs before injecting the HTML (Referer-requiring hosts) ─────
// The fetched bytes are inlined as data: URLs (not blob:) so they survive the
// webview dropping blob backing data while scrolling.
watch(
  [body, () => a.value?.url],
  (_v, _o, onCleanup) => {
    const src = body.value;
    if (!src) {
      proxiedBody.value = null;
      return;
    }
    const doc = new DOMParser().parseFromString(src, "text/html");
    const imgs = Array.from(doc.body.querySelectorAll("img")).filter((img) => {
      const s = img.getAttribute("src") || "";
      return /^https?:\/\//.test(s) && needsImageProxy(s);
    });
    if (imgs.length === 0) {
      proxiedBody.value = null;
      return;
    }
    let alive = true;
    Promise.all(
      imgs.map(async (img) => {
        const s = img.getAttribute("src") || "";
        try {
          const buf = await api.fetchImage(s, a.value?.url);
          if (!alive) return;
          img.dataset.paprSrc = s;
          img.setAttribute("src", imageDataUrl(s, buf));
          img.removeAttribute("srcset");
          img.removeAttribute("referrerpolicy");
        } catch {
          if (!alive) return;
          img.style.display = "none";
        }
      }),
    ).then(() => {
      if (alive) proxiedBody.value = { source: src, html: doc.body.innerHTML };
    });
    onCleanup(() => {
      alive = false;
    });
  },
  { immediate: true },
);

// ── when a translation finishes, refetch so persisted html lands in cache ────
watch(
  () => job.value?.status,
  (status) => {
    if (id.value == null || !job.value) return;
    if (status === "done") {
      // The completed copy is now persisted — refetch the cache so it survives
      // navigation away and back, replacing the live job as the source.
      qc.invalidateQueries({ queryKey: ["translations", id.value] });
    } else if (status === "error") {
      // The translation failed (a toast already surfaced why) — drop back to the
      // original so the view isn't stuck on an empty "translating…" state.
      transView.value = "original";
    }
  },
);

// ── auto-extract a summary-only feed item into the full page ─────────────────
// Runs when the effective open mode is "extracted", so a stub is upgraded to
// the full page the moment it's opened.
watch(
  [() => a.value?.id, () => a.value?.extractedHtml, () => ui.prefs.defaultOpenMode],
  () => {
    const art = a.value;
    if (!articleNeedsExtract(art) || !art) return;
    if (autoExtracted === art.id || extractFailedId.value === art.id) return;
    if (extract.isPending.value && extract.variables.value === art.id) return;
    autoExtracted = art.id;
    extract.mutate(art.id);
  },
  { immediate: true },
);

// Warm the next/prev articles so ← → often lands on text that's already extracted.
const prefetching = new Set<number>();
watch(
  () => a.value?.id,
  (articleId) => {
    if (articleId == null) return;
    if ((a.value?.feedOpenMode ?? ui.prefs.defaultOpenMode) !== "extracted") return;
    const items = currentList(qc);
    const idx = items.findIndex((it) => it.id === articleId);
    if (idx < 0) return;
    for (const n of [items[idx + 1], items[idx - 1]]) {
      if (!n?.id || prefetching.has(n.id) || n.id === autoExtracted) continue;
      prefetching.add(n.id);
      const nid = n.id;
      api
        .getArticle(nid)
        .then(async (detail) => {
          qc.setQueryData(["article", nid], detail);
          if (!articleNeedsExtract(detail)) return;
          await api.extractFulltext(nid);
          await qc.invalidateQueries({ queryKey: ["article", nid] });
        })
        .catch(() => {
          prefetching.delete(nid);
        });
    }
  },
);

// ── mark read once the foot is reached ───────────────────────────────────────
// Also fires for an article short enough to need no scrolling (no `scroll`
// event), so a fully-visible short article is still marked read.
const markReadIfAtFoot = () => {
  const el = scrollRef.value;
  const art = a.value;
  if (!el || !ui.prefs.markReadOnScroll || !art || art.isRead) return;
  if (scrollMarked === art.id) return;
  if (el.scrollHeight - el.scrollTop - el.clientHeight < 120) {
    scrollMarked = art.id;
    actions.setRead(art.id, true);
  }
};

const onScroll = () => {
  const el = scrollRef.value;
  if (!el) return;
  // Hysteresis (deadband 4–12px) so a micro-scroll near the threshold doesn't
  // flicker the toolbar title in and out.
  scrolled.value = el.scrollTop > (scrolled.value ? 4 : 12);
  // Reveal the back-to-top once about a viewport has gone by (not a fixed 800).
  showToTop.value = el.scrollTop > Math.max(420, el.clientHeight * 0.65);
  const max = el.scrollHeight - el.clientHeight;
  progress.value = max > 0 ? Math.min(1, Math.max(0, el.scrollTop / max)) : 0;
  markReadIfAtFoot();
};

// Back-to-top affordance for long articles. Honors reduce-motion (no smooth).
const showToTop = ref(false);
const scrollToTop = () => {
  scrollRef.value?.scrollTo({ top: 0, behavior: ui.prefs.reduceMotion ? "auto" : "smooth" });
};

// A short article that fits the viewport never fires `scroll`, so check the foot
// condition once the body has laid out. Deferred briefly so body images have a
// chance to load before `scrollHeight` is measured.
watch(
  [showExtracted, () => a.value?.extractedHtml, () => a.value?.contentHtml],
  (_v, _o, onCleanup) => {
    const timer = window.setTimeout(markReadIfAtFoot, 400);
    onCleanup(() => window.clearTimeout(timer));
  },
  { flush: "post" },
);

// ── toolbar / context-menu actions ───────────────────────────────────────────
const copyLink = () => {
  if (!a.value?.url) return;
  navigator.clipboard.writeText(a.value.url).then(
    () => emit("toast", t("reader.linkCopied")),
    () => {},
  );
};
const copyText = (text: string, toastKey: string) => {
  navigator.clipboard.writeText(text).then(() => emit("toast", t(toastKey)), () => {});
};
// Save a feed image to disk. The bytes are fetched in Go (not the webview) so the
// request's Referer can walk the hotlink-protection fallbacks.
const saveImage = async (url: string) => {
  try {
    const buf = await api.fetchImage(url, a.value?.url);
    downloadBlob(new Blob([buf]), imageFilename(url));
  } catch {
    toast.error(t("reader.imageSaveFailed"));
  }
};
const share = () => {
  const art = a.value;
  if (!art?.url) return;
  if (navigator.share) {
    navigator.share({ title: art.title, url: art.url }).catch((e) => {
      // A user-cancelled share rejects with AbortError — only fall back to
      // copying the link on a genuine failure.
      if ((e as Error)?.name !== "AbortError") copyLink();
    });
  } else {
    copyLink();
  }
};
const openInBrowser = (url: string) => {
  Browser.OpenURL(url).catch(() => {});
};
// The page-view address bar shows the URL decoded (it's stored percent-encoded,
// e.g. %E8%A7%84…), falling back to the raw value if a sequence is malformed.
// Opening in the browser still uses the original a.url.
const prettyUrl = (u: string): string => {
  try {
    return decodeURI(u);
  } catch {
    return u;
  }
};

// Translate into `lang` with `eng`, then show it as `mode` (translation or
// bilingual). Starts a fresh job (the store skips only a duplicate in-flight
// one); a cached copy for the target is picked up automatically by translatedBody.
const run = (lang: string, eng: string, mode: TransView = "translation") => {
  if (!canTranslate.value || !a.value) return;
  jobs.translate(a.value.id, lang, eng);
  transView.value = mode === "original" ? "translation" : mode;
};

// Switch view mode. Original just hides; translation/bilingual reuse a cached or
// in-flight copy for the current (lang, engine), else kick off a new job.
const setMode = (mode: TransView) => {
  if (mode === "original" || hasTranslation.value || translating.value) {
    transView.value = mode;
    return;
  }
  run(targetLang.value, engine.value, mode);
};

// The non-original mode currently shown (defaults to plain translation), so an
// engine/language change keeps the user in bilingual if that's where they were.
const liveMode = (): TransView => (transView.value === "bilingual" ? "bilingual" : "translation");

// Pick a mode from the popover, then close it so the result is visible.
const pickMode = (mode: TransView) => {
  setMode(mode);
  transMenu.value = false;
};

// Close the translate / reading-settings popover on Escape — listener only
// while one is open. Capture + stop so Esc doesn't also close the reader.
watch([transMenu, typeMenu, moreMenu], ([transOpen, typeOpen, moreOpen], _prev, onCleanup) => {
  if (!transOpen && !typeOpen && !moreOpen) return;
  const onKey = (e: KeyboardEvent) => {
    if (e.key !== "Escape") return;
    e.stopPropagation();
    e.preventDefault();
    transMenu.value = false;
    typeMenu.value = false;
    moreMenu.value = false;
  };
  window.addEventListener("keydown", onKey, true);
  onCleanup(() => window.removeEventListener("keydown", onKey, true));
});

function closeReaderMenus() {
  transMenu.value = false;
  typeMenu.value = false;
  moreMenu.value = false;
  tagPick.value = null;
}

function toggleTypeMenu() {
  const next = !typeMenu.value;
  closeReaderMenus();
  typeMenu.value = next;
}

function toggleTransMenu() {
  const next = !transMenu.value;
  closeReaderMenus();
  transMenu.value = next;
}

function toggleMoreMenu() {
  const next = !moreMenu.value;
  closeReaderMenus();
  moreMenu.value = next;
}

watch(moreMenu, async (on) => {
  if (!on) return;
  await nextTick();
  morePopRef.value
    ?.querySelector<HTMLButtonElement>('[role="menuitem"]:not([disabled])')
    ?.focus();
});

function onMoreKey(e: KeyboardEvent) {
  const root = morePopRef.value;
  if (!root) return;
  const items = Array.from(
    root.querySelectorAll<HTMLButtonElement>('[role="menuitem"]'),
  ).filter((el) => !el.disabled);
  if (items.length === 0) return;
  const i = items.indexOf(document.activeElement as HTMLButtonElement);
  if (e.key === "ArrowDown") {
    e.preventDefault();
    items[(Math.max(i, -1) + 1) % items.length].focus();
  } else if (e.key === "ArrowUp") {
    e.preventDefault();
    items[(i - 1 + items.length) % items.length].focus();
  } else if (e.key === "Home") {
    e.preventDefault();
    items[0].focus();
  } else if (e.key === "End") {
    e.preventDefault();
    items[items.length - 1].focus();
  }
}

// Full-screen image viewer: the article's image srcs + the one to open on.
// Null when closed.
const viewer = ref<{ srcs: string[]; index: number } | null>(null);

// Article-body clicks: an image opens the full-screen viewer, with the
// article's other images available for ← / → navigation; anything else falls
// through to the link handler (in-page anchors, external links). Rebuilt per
// click so it always closes over the current article's URL.
const onBodyClick = (e: MouseEvent) => {
  const img = (e.target as HTMLElement).closest("img");
  const root = bodyRef.value;
  if (img && root?.contains(img)) {
    // Use the src the DOM actually renders — a proxied data: URL when the
    // original hotlink-protected host needed a Referer — and skip hidden /
    // broken images so the gallery matches what the reader shows.
    const imgs = Array.from(root.querySelectorAll<HTMLImageElement>("img")).filter(
      (im) => im.style.display !== "none" && (im.currentSrc || im.getAttribute("src")),
    );
    const index = imgs.indexOf(img as HTMLImageElement);
    if (index >= 0) {
      e.preventDefault();
      viewer.value = { srcs: imgs.map((im) => im.currentSrc || im.src), index };
      return;
    }
  }
  makeLinkClickHandler(a.value?.url ?? null)(e);
};

// Leaving the article (or the reader) closes the gallery with it.
watch(() => a.value?.id, () => (viewer.value = null));

// Space / ⇧Space page the article. The overlay's scroll container is a div,
// not the document, so the browser's built-in space-to-scroll never fires in
// the reader — the key was simply dead here. Reader mode only (the web mode
// is a native view with its own scrolling), and never over inputs or modals.
function readerScrollBlocked(e: KeyboardEvent): boolean {
  const target = e.target as HTMLElement | null;
  const tag = target?.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || target?.isContentEditable) return true;
  if (viewMode.value !== "reader") return true;
  return !!document.querySelector(".cp-backdrop, .settings-backdrop, .modal-backdrop, .ctx-menu, .tag-picker, .hl-popover, .hl-toolbar, .image-viewer, .rs-pop, .tr-pop, .tb-more-pop");
}

function onSpaceKey(e: KeyboardEvent): void {
  const pageKey = e.key === " " || e.key === "PageDown" || e.key === "PageUp";
  if (!pageKey || e.metaKey || e.ctrlKey || e.altKey) return;
  if (readerScrollBlocked(e)) return;
  const el = scrollRef.value;
  if (!el) return;
  e.preventDefault();
  // Slight overlap between pages keeps a line of context across the fold.
  const page = el.clientHeight * 0.88;
  const up = e.key === "PageUp" || (e.key === " " && e.shiftKey);
  el.scrollBy({
    top: up ? -page : page,
    behavior: ui.prefs.reduceMotion ? "auto" : "smooth",
  });
}

function onHomeEndKey(e: KeyboardEvent): void {
  if (e.key !== "Home" && e.key !== "End") return;
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  if (readerScrollBlocked(e)) return;
  const el = scrollRef.value;
  if (!el) return;
  e.preventDefault();
  el.scrollTo({
    top: e.key === "Home" ? 0 : el.scrollHeight,
    behavior: ui.prefs.reduceMotion ? "auto" : "smooth",
  });
}
// ⌘/Ctrl + − / + / 0 adjust type size while reading (and beat webview zoom).
function onTypeSizeKey(e: KeyboardEvent): void {
  if (!(e.metaKey || e.ctrlKey) || e.altKey) return;
  if (!a.value || viewMode.value !== "reader") return;
  const target = e.target as HTMLElement | null;
  const tag = target?.tagName;
  if (tag === "TEXTAREA" || target?.isContentEditable) return;
  if (tag === "INPUT" && (target as HTMLInputElement).type !== "range") return;
  if (document.querySelector(".cp-backdrop, .settings-backdrop, .modal-backdrop")) return;
  const k = e.key;
  let next: number | null = null;
  if (k === "=" || k === "+" || k === "Add") {
    next = ui.readerSize + 1;
  } else if (k === "-" || k === "_" || k === "Subtract") {
    next = ui.readerSize - 1;
  } else if (k === "0") {
    next = READER_DEFAULTS.size;
  } else {
    return;
  }
  e.preventDefault();
  e.stopPropagation();
  ui.setReader({
    readerSize: Math.min(READER_BOUNDS.size.max, Math.max(READER_BOUNDS.size.min, next)),
  });
}
onMounted(() => {
  window.addEventListener("keydown", onSpaceKey);
  window.addEventListener("keydown", onHomeEndKey);
  window.addEventListener("keydown", onTypeSizeKey, true);
});
onBeforeUnmount(() => {
  window.removeEventListener("keydown", onSpaceKey);
  window.removeEventListener("keydown", onHomeEndKey);
  window.removeEventListener("keydown", onTypeSizeKey, true);
});

const onTagBtn = (e: MouseEvent) => {
  const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
  const next = tagPick.value ? null : { x: r.left, y: r.bottom + 6 };
  closeReaderMenus();
  tagPick.value = next;
};

// Gutter clicks used to close the overlay. After the side margins grew to
// clear the ‹ › arrows, that became too easy to do by accident — Esc / ✕
// are the dismissals.

const openHeroViewer = () => {
  const art = a.value;
  const src = heroDataUrl.value ?? art?.imageUrl;
  if (!src) return;
  const bodyImgs = bodyRef.value
    ? Array.from(bodyRef.value.querySelectorAll<HTMLImageElement>("img"))
        .filter((im) => im.style.display !== "none" && (im.currentSrc || im.getAttribute("src")))
        .map((im) => im.currentSrc || im.src)
    : [];
  const srcs = [src, ...bodyImgs.filter((s) => s !== src)];
  viewer.value = { srcs, index: 0 };
};

const onExtractBtn = () => {
  if (!a.value) return;
  if (hasExtracted.value) showExtracted.value = !showExtracted.value;
  else extract.mutate(a.value.id);
};

const onTranslateBtn = () => {
  // Split-button primary: flip back to the original when a translation is
  // showing, else restore the last mode used (translation or bilingual).
  if (showTranslation.value) transView.value = "original";
  else setMode(lastMode.value);
};

// Keyboard: "t" toggles the translation (mirrors the toolbar button). App.vue
// owns the other reader shortcuts and leaves "t" alone, so a local listener
// claims it — skipped while a field/overlay has focus or there's nothing to do.
function onTransKey(e: KeyboardEvent) {
  if (e.key.toLowerCase() !== "t" || e.metaKey || e.ctrlKey || e.altKey) return;
  const el = e.target as HTMLElement | null;
  if (el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable)) return;
  if (document.querySelector(".cp-backdrop, .settings-backdrop, .modal-backdrop, .ctx-menu, .tag-picker, .hl-popover, .hl-toolbar, .image-viewer, .rs-pop, .tr-pop, .tb-more-pop")) return;
  if (!a.value || !canTranslate.value) return;
  e.preventDefault();
  onTranslateBtn();
}
onMounted(() => window.addEventListener("keydown", onTransKey));
onBeforeUnmount(() => window.removeEventListener("keydown", onTransKey));

// Per-article engine / language overrides. After the override lands, reuse a
// cached copy for the new target if there is one, else translate afresh —
// staying in whichever non-original mode the reader was viewing.
const onEngineChange = (v: string) => {
  tmpEngine.value = v;
  // Re-translate only if a translation is already on screen; otherwise just
  // record the override for the next translate.
  if (showTranslation.value) setMode(liveMode());
};
const onLangChange = (v: string) => {
  tmpLang.value = v;
  if (showTranslation.value) setMode(liveMode());
};

// Engine label for the "translated via …" caption (⑤).
const engineLabel = (e: string) =>
  e === "llm" ? t("reader.translateEngineLlm") : e.charAt(0).toUpperCase() + e.slice(1);

// Per-feed auto-translate override (④): null follows global, 1 on, 0 off.
const setFeedAuto = async (mode: number | null) => {
  const art = a.value;
  if (!art) return;
  try {
    await api.setFeedAutoTranslate(art.feedId, mode);
    qc.invalidateQueries({ queryKey: ["article", art.id] });
    qc.invalidateQueries({ queryKey: ["feeds"] });
  } catch (err) {
    reportError(err);
  }
};

// ── auto-translate on open (④) ───────────────────────────────────────────────
// When the effective auto-translate is on and the article is in a different
// script than the target, show it translated on open (reusing the cache, or
// kicking off a job). Guarded per-article so it never fights a manual switch
// back to the original.
// True while full-text extraction is fetching, or is still expected to run, for
// a summary-only article. Auto-translate waits for it so it translates the full
// text rather than the feed's summary (which the extraction is about to replace).
function extractPending(): boolean {
  const art = a.value;
  if (!art || art.extractedHtml) return false;
  if (extract.isPending.value) return true;
  if (effectiveOpenMode.value !== "extracted" || !art.url) return false;
  // Mirrors the auto-extract watcher: an explicit per-feed "extracted"
  // extracts unconditionally; the global default only upgrades stubs.
  return (
    art.feedOpenMode === "extracted" ||
    bodyPlainText(art.contentHtml || "").trim().length < 800
  );
}
let autoTranslatedId: number | null = null;
watch(
  [
    () => a.value?.id,
    autoTranslateOn,
    () => baseBody.value,
    cachedEntry,
    () => extract.isPending.value,
  ],
  () => {
    const art = a.value;
    if (!art || !autoTranslateOn.value || !canTranslate.value) return;
    if (autoTranslatedId === art.id) return;
    if (extractPending()) return; // wait for the full text before translating
    if (!scriptMismatch(bodyPlainText(baseBody.value), targetLang.value)) return;
    autoTranslatedId = art.id;
    setMode("translation"); // shows the cached copy if present, else translates
  },
  { immediate: true },
);

const playEnclosure = (url: string) => {
  const art = a.value;
  if (!art) return;
  player.play({ articleId: art.id, title: art.title, feedTitle: art.feedTitle, src: url });
};

const selectTag = (tagId: number, name: string) => {
  ui.select({ kind: "tag", value: tagId }, name);
};

// The article-tag chip's accent is driven by a `--tag-c` CSS custom property
// (papr set it via an inline style cast to CSSProperties). A typed string-map
// keeps the custom property out of the strict CSSProperties keys.
const tagChipStyle = (color: string): Record<string, string> => ({
  "--tag-c": tagColor(color),
});

const onContextMenu = (e: MouseEvent) => {
  e.preventDefault();
  // Capture what the click landed on so the menu can add image- and selection-
  // specific actions (the native menu is suppressed app-wide; see main.ts).
  const img = (e.target as HTMLElement).closest("img") as HTMLImageElement | null;
  const sel = window.getSelection();
  const selection = sel && !sel.isCollapsed ? sel.toString().trim() : "";
  ctxMenu.value = {
    x: e.clientX,
    y: e.clientY,
    // data-papr-src holds the real address when the image was recovered through
    // the backend and src is an inline data: URL.
    imageUrl: img?.dataset.paprSrc || img?.currentSrc || img?.getAttribute("src") || undefined,
    selection: selection || undefined,
  };
};

// Audio / video enclosures, split by mime type.
const audioEnclosures = computed(
  () => a.value?.enclosures.filter((e) => e.mimeType?.startsWith("audio")) ?? [],
);
const videoEnclosures = computed(
  () => a.value?.enclosures.filter((e) => e.mimeType?.startsWith("video")) ?? [],
);

// True when the body opens with its own visual media (image / video / iframe)
// before any real text. Such an article already leads with a strong visual, so
// prepending the list-thumbnail hero on top of it just shows a second, often
// unrelated image — e.g. a body that starts with a <video> cover while the
// feed's media:thumbnail is a different png. Walking in document order (media
// element before the first non-whitespace text node) is what the plain
// body.includes(imageUrl) guard can't catch, since the hero image and the
// body's lead media are distinct URLs there.
function bodyLeadsWithMedia(html: string): boolean {
  if (!html) return false;
  const doc = new DOMParser().parseFromString(html, "text/html");
  const walker = doc.createTreeWalker(
    doc.body,
    NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT,
  );
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    if (node.nodeType === Node.TEXT_NODE) {
      if ((node.textContent ?? "").trim().length > 0) return false;
    } else if (node instanceof Element) {
      if (["IMG", "VIDEO", "IFRAME", "PICTURE"].includes(node.tagName)) return true;
    }
  }
  return false;
}
const leadsWithMedia = computed(() => bodyLeadsWithMedia(baseBody.value));

// Skip the hero when the body already embeds the same image, so feeds that
// repeat their lead image don't show it twice — and when the body opens with
// its own image/video, where a separate thumbnail on top would just be a
// redundant (often mismatched) picture.
const showHero = computed(
  () =>
    !!a.value?.imageUrl &&
    !heroBroken.value &&
    !body.value.includes(a.value.imageUrl) &&
    !leadsWithMedia.value &&
    !ytId.value,
);

// Hero <img> error: retry through the backend's Referer-fallback fetch; a
// failing data: URL means the recovered bytes weren't a renderable image — give
// up.
const onHeroError = () => {
  const art = a.value;
  if (!art?.imageUrl) return;
  if (heroDataUrl.value) {
    heroBroken.value = true;
    return;
  }
  const articleId = art.id;
  api
    .fetchImage(art.imageUrl, art.url)
    .then((buf) => {
      if (ui.selectedArticleId !== articleId) return;
      heroDataUrl.value = imageDataUrl(art.imageUrl!, buf);
    })
    .catch(() => {
      if (ui.selectedArticleId !== articleId) return;
      heroBroken.value = true;
    });
};

// Context-menu entries, assembled the way papr built its `items` array.
const ctxItems = computed<MenuEntry[]>(() => {
  const m = ctxMenu.value;
  const art = a.value;
  if (!m || !art) return [];
  const items: MenuEntry[] = [];
  if (m.selection) {
    items.push({
      icon: "copy",
      label: t("reader.ctxCopy"),
      onClick: () => copyText(m.selection!, "reader.textCopied"),
    });
  }
  if (m.imageUrl) {
    items.push(
      { icon: "arrow-down", label: t("reader.ctxSaveImage"), onClick: () => saveImage(m.imageUrl!) },
      {
        icon: "copy",
        label: t("reader.ctxCopyImageAddress"),
        onClick: () => copyText(m.imageUrl!, "reader.imageAddressCopied"),
      },
    );
  }
  if (m.selection || m.imageUrl) items.push({ separator: true });
  items.push({
    icon: ui.aiOpen ? "sparkle-fill" : "sparkle",
    label: t("reader.tbAiSummary"),
    onClick: () => ui.setAiOpen(!ui.aiOpen),
  });
  if (canTranslate.value) {
    items.push({
      icon: "translate",
      label: showTranslation.value ? t("reader.tbShowOriginal") : t("reader.tbTranslate"),
      onClick: () => (showTranslation.value ? (transView.value = "original") : setMode("translation")),
    });
  }
  items.push({ separator: true });
  if (art.url) {
    items.push({ icon: "copy", label: t("reader.tbCopyLink"), onClick: copyLink });
  }
  items.push({ separator: true });
  items.push({
    icon: ui.focusMode ? "eye-off" : "focus",
    label: t("reader.tbFocusMode"),
    onClick: () => ui.setFocusMode(!ui.focusMode),
  });
  items.push({
    icon: "settings",
    label: t("reader.type.title"),
    onClick: () => {
      transMenu.value = false;
      typeMenu.value = true;
    },
  });
  return items;
});

// Inline <kbd> style for the empty-state hint (papr's `kbd` object).
const kbdStyle = {
  fontFamily: "var(--mono)",
  fontSize: "10px",
  padding: "1px 5px",
  border: "1px solid var(--hair)",
  borderRadius: "3px",
};
</script>

<template>
  <!-- No article selected -->
  <div v-if="id == null" class="reader" role="main">
    <div v-if="isMac" class="reader-toolbar" data-tauri-drag-region />
    <div class="empty" :style="{ flex: 1 }">
      <div class="glyph">
        <Icon name="rss" :size="22" />
      </div>
      <div>{{ t("reader.emptySelectArticle") }}</div>
      <div :style="{ fontSize: '11.5px', color: 'var(--muted-2)' }">
        {{ t("reader.emptyHintPrefix") }} <kbd :style="kbdStyle">J</kbd> /
        <kbd :style="kbdStyle">K</kbd> {{ t("reader.emptyHintSuffix") }}
      </div>
    </div>
  </div>

  <!-- Selected but detail not loaded yet (fetching or failed) -->
  <div v-else-if="!a" class="reader" role="main">
    <div
      class="reader-toolbar"
      v-bind="isMac ? { 'data-tauri-drag-region': true } : {}"
    />
    <div v-if="article.isError.value" class="empty" :style="{ flex: 1 }">
      <div class="glyph">
        <Icon name="alert" :size="22" />
      </div>
      <div>{{ t("reader.loadError") }}</div>
      <button class="empty-retry" @click="article.refetch()" :disabled="article.isFetching.value">
        <Icon name="refresh" :size="12" />
        {{ t("common.retry") }}
      </button>
    </div>
    <div v-else class="reader-scroll">
      <div class="article reader-content" aria-hidden="true">
        <div class="sk-line" :style="{ width: '28%' }" />
        <div class="sk-line" :style="{ width: '82%', height: '24px', marginBottom: '18px' }" />
        <div class="sk-line" :style="{ width: '44%', marginBottom: '30px' }" />
        <div
          v-for="i in 9"
          :key="i"
          class="sk-line"
          :style="{ width: (i - 1) % 3 === 2 ? '58%' : '100%', height: '12px' }"
        />
      </div>
    </div>
  </div>

  <!-- Loaded article -->
  <div
    v-else
    class="reader"
    :class="{ 'has-pop': typeMenu || transMenu || moreMenu || tagPick }"
    role="main"
  >
    <div class="reader-progress" :class="{ on: progress > 0.012 }" aria-hidden="true">
      <div class="reader-progress-fill" :style="{ transform: `scaleX(${progress})` }"></div>
    </div>
    <div
      :class="['reader-toolbar', scrolled ? 'scrolled' : '']"
      v-bind="isMac ? { 'data-tauri-drag-region': true } : {}"
    >
      <div class="tb-cluster">
      <button
        :class="['tb-btn', a.isStarred ? 'on' : '']"
        @click="actions.setStarred(a.id, !a.isStarred)"
        :title="t('reader.tbStar')"
        :aria-label="t('reader.tbStar')"
        :aria-pressed="a.isStarred"
      >
        <Icon :name="a.isStarred ? 'star-fill' : 'star'" :size="16" />
      </button>
      <button
        :class="['tb-btn', a.readLater ? 'on' : '']"
        @click="actions.setReadLater(a.id, !a.readLater)"
        :title="t('reader.tbReadLater')"
        :aria-label="t('reader.tbReadLater')"
        :aria-pressed="a.readLater"
      >
        <Icon :name="a.readLater ? 'bookmark-fill' : 'bookmark'" :size="16" />
      </button>
      <button
        :class="['tb-btn', a.tags.length > 0 ? 'on' : '']"
        @click="onTagBtn"
        :title="t('reader.tbTags')"
        :aria-label="t('reader.tbTags')"
        aria-haspopup="menu"
        :aria-expanded="tagPick != null"
      >
        <Icon name="tag" :size="16" />
      </button>
      </div>
      <span class="tb-sep" aria-hidden="true" />
      <div class="tb-cluster">
      <button
        :class="[
          'tb-btn',
          hasExtracted && showExtracted ? 'on' : '',
          extract.isPending.value ? 'spinning' : '',
        ]"
        @click="onExtractBtn"
        :disabled="extract.isPending.value || (!hasExtracted && !a.url)"
        :title="hasExtracted ? t('reader.tbToggleFullText') : t('reader.tbExtractFullText')"
        :aria-label="hasExtracted ? t('reader.tbToggleFullText') : t('reader.tbExtractFullText')"
        :aria-pressed="hasExtracted ? showExtracted : undefined"
        :aria-busy="extract.isPending.value"
      >
        <Icon name="text" :size="16" />
      </button>
      <!-- AI summary: toggles the per-article summary drawer (also I / right-click). -->
      <button
        :class="['tb-btn', ui.aiOpen ? 'on' : '']"
        :title="t('reader.tbAiSummary')"
        :aria-label="t('reader.tbAiSummary')"
        :aria-pressed="ui.aiOpen"
        @click="ui.setAiOpen(!ui.aiOpen)"
      >
        <Icon :name="ui.aiOpen ? 'sparkle-fill' : 'sparkle'" :size="16" />
      </button>
      <!-- Translate: split button — primary toggles the translation; the caret
           opens the engine / language / mode / auto-translate popover (①). -->
      <div class="tb-trans">
        <button
          :class="['tb-btn', 'tb-trans-main', showTranslation ? 'on' : '']"
          :title="t('reader.tbTranslate')"
          :aria-label="t('reader.tbTranslate')"
          :aria-pressed="showTranslation"
          :disabled="!canTranslate"
          @click="onTranslateBtn"
        >
          <Icon v-if="!translating" name="translate" :size="16" />
          <Icon v-else name="refresh" :size="16" class="spinning" />
        </button>
        <button
          :class="[
            'tb-btn',
            'tb-trans-caret',
            showTranslation ? 'on' : '',
            transMenu ? 'open' : '',
          ]"
          :title="t('reader.translateOptions')"
          :aria-label="t('reader.translateOptions')"
          :aria-expanded="transMenu"
          :disabled="!canTranslate"
          @click="toggleTransMenu"
        >
          <Icon name="chevron-down" :size="12" />
        </button>
        <template v-if="transMenu">
          <div class="tr-pop-backdrop" @click="transMenu = false"></div>
          <div class="tr-pop" role="dialog" :aria-label="t('reader.translateOptions')">
            <div class="tr-pop-modes" role="group" :aria-label="t('reader.tbTranslate')">
              <button :class="{ on: transView === 'original' }" @click="pickMode('original')">
                {{ t("reader.original") }}
              </button>
              <button :class="{ on: transView === 'bilingual' }" @click="pickMode('bilingual')">
                {{ t("reader.bilingual") }}
              </button>
              <button :class="{ on: transView === 'translation' }" @click="pickMode('translation')">
                {{ t("reader.translation") }}
              </button>
            </div>
            <label class="tr-pop-row">
              <span>{{ t("reader.translateTitle") }}</span>
              <select
                class="s-select"
                :value="targetLang"
                :disabled="translating"
                @change="onLangChange(($event.target as HTMLSelectElement).value)"
              >
                <option v-for="l in LANGUAGES" :key="l.code" :value="l.code">{{ l.label }}</option>
              </select>
            </label>
            <label class="tr-pop-row">
              <span>{{ t("reader.translateEngine") }}</span>
              <select
                class="s-select"
                :value="engine"
                :disabled="translating"
                @change="onEngineChange(($event.target as HTMLSelectElement).value)"
              >
                <option value="llm">{{ t("reader.translateEngineLlm") }}</option>
                <option value="google">Google</option>
                <option value="deepl">DeepL</option>
                <option value="bing">Bing</option>
              </select>
            </label>
            <div v-if="llmNeedsKey" class="tr-pop-note tr-pop-warn">
              {{ t("reader.translateNeedsKey") }}
            </div>
            <div v-if="translating" class="tr-pop-note">
              {{ t("reader.translating")
              }}{{
                jobForTarget && jobForTarget.total > 0
                  ? ` ${jobForTarget.done}/${jobForTarget.total}`
                  : ""
              }}
            </div>
            <div v-else-if="hasTranslation" class="tr-pop-note">
              {{ t("reader.translatedVia", { engine: engineLabel(engine) }) }}
            </div>
            <div class="tr-pop-auto">
              <span>{{ t("reader.autoTranslateFeed") }}</span>
              <div class="tr-pop-seg" role="group">
                <button
                  :class="{ on: (a.feedAutoTranslate ?? null) === null }"
                  @click="setFeedAuto(null)"
                >
                  {{ t("reader.autoDefault") }}
                </button>
                <button :class="{ on: a.feedAutoTranslate === 1 }" @click="setFeedAuto(1)">
                  {{ t("reader.autoOn") }}
                </button>
                <button :class="{ on: a.feedAutoTranslate === 0 }" @click="setFeedAuto(0)">
                  {{ t("reader.autoOff") }}
                </button>
              </div>
            </div>
          </div>
        </template>
      </div>
      </div>
      <!-- Keyed by article id so popovers reset cleanly when the reader switches
           articles. -->
      <HighlightLayer :key="a.id" :article-id="a.id" :body-el="bodyRef ?? null" />
      <!-- Doubles as the flex spacer: empty until scrolled, then fades in the
           article title so the toolbar reads as a command/title bar. -->
      <div
        class="tb-title"
        :class="{ show: scrolled }"
        :title="scrolled ? t('reader.backToTop') : undefined"
        :role="scrolled ? 'button' : undefined"
        @click="scrolled && scrollToTop()"
      >{{ displayTitle }}</div>
      <div class="tb-cluster">
      <div class="tb-more">
        <button
          type="button"
          :class="['tb-btn', moreMenu ? 'on' : '']"
          :title="t('reader.tbMore')"
          :aria-label="t('reader.tbMore')"
          :aria-expanded="moreMenu"
          aria-haspopup="menu"
          @click="toggleMoreMenu"
        >
          <Icon name="more" :size="16" />
        </button>
        <template v-if="moreMenu">
          <div class="tr-pop-backdrop" @click="moreMenu = false"></div>
          <div
            ref="morePopRef"
            class="tb-more-pop"
            role="menu"
            :aria-label="t('reader.tbMore')"
            @keydown="onMoreKey"
          >
            <button
              type="button"
              role="menuitem"
              :disabled="!a.url"
              @click="share(); moreMenu = false"
            >
              <Icon name="share" :size="14" />
              {{ t("reader.tbShare") }}
            </button>
            <button
              type="button"
              role="menuitem"
              :disabled="!a.url"
              @click="copyLink(); moreMenu = false"
            >
              <Icon name="copy" :size="14" />
              {{ t("reader.tbCopyLink") }}
            </button>
            <button
              type="button"
              role="menuitem"
              :aria-pressed="ui.focusMode"
              @click="ui.setFocusMode(!ui.focusMode); moreMenu = false"
            >
              <Icon :name="ui.focusMode ? 'eye-off' : 'focus'" :size="14" />
              {{ t("reader.tbFocusMode") }}
            </button>
          </div>
        </template>
      </div>
      <div class="tb-type">
        <button
          type="button"
          :class="['tb-btn', typeMenu ? 'on' : '']"
          :title="t('reader.type.title')"
          :aria-label="t('reader.type.title')"
          :aria-expanded="typeMenu"
          aria-haspopup="dialog"
          @click="toggleTypeMenu"
        >
          <span class="tb-aa" aria-hidden="true">Aa</span>
        </button>
        <template v-if="typeMenu">
          <div class="tr-pop-backdrop" @click="typeMenu = false"></div>
          <ReaderSettingsPopover
            @close="typeMenu = false"
            @open-settings="emit('open-settings', 'reading')"
          />
        </template>
      </div>
      </div>
      <template v-if="a.url">
        <span class="tb-sep" aria-hidden="true" />
        <div class="tb-cluster">
          <button
            class="tb-btn"
            :class="{ on: viewMode === 'web' }"
            :title="t('reader.tbWebView')"
            :aria-label="t('reader.tbWebView')"
            :aria-pressed="viewMode === 'web'"
            @click="viewMode = viewMode === 'web' ? 'reader' : 'web'"
          >
            <Icon name="eye" :size="16" />
          </button>
          <button
            class="tb-btn"
            :title="t('reader.tbOpenInBrowser')"
            :aria-label="t('reader.tbOpenInBrowser')"
            @click="openInBrowser(a.url!)"
          >
            <Icon name="open" :size="16" />
          </button>
        </div>
      </template>
    </div>

    <!-- Thin bar under the toolbar while a translation streams: determinate by
         batch count, or an indeterminate slide before the first batch lands. -->
    <div
      v-if="translating"
      class="tr-bar"
      :class="{ indeterminate: !(jobForTarget && jobForTarget.total) }"
      aria-hidden="true"
    >
      <div
        class="tr-bar-fill"
        :style="
          jobForTarget && jobForTarget.total
            ? { transform: `scaleX(${jobForTarget.done / jobForTarget.total})` }
            : undefined
        "
      ></div>
    </div>

    <!-- Original web page, in a native child window floated over the host div
         below. The window is NOT in the DOM — the host only reserves the space;
         the watch above measures it and positions the window to match. -->
    <div v-if="viewMode === 'web' && a.url" class="reader-webview">
      <div class="reader-webview-bar">
        <Icon name="globe" :size="14" class="reader-webview-icon" />
        <span class="reader-webview-url">{{ prettyUrl(a.url) }}</span>
        <button class="reader-webview-open" @click="openInBrowser(a.url!)">
          {{ t("reader.tbOpenInBrowser") }}
        </button>
        <button
          class="reader-webview-close"
          :title="t('reader.tbReaderView')"
          :aria-label="t('reader.tbReaderView')"
          @click="viewMode = 'reader'"
        >
          <Icon name="x" :size="15" />
        </button>
      </div>
      <div class="reader-webview-host" ref="pageHostRef"></div>
      <!-- Loading veil spans the whole content area (address bar included), so the
           pane reads as one clean field until the native page fades in. -->
      <div v-if="pvLoading" class="reader-webview-loading">
        <span class="reader-webview-spinner" aria-hidden="true"></span>
        <span class="reader-webview-loading-label">{{ t("common.loading") }}</span>
      </div>
    </div>

    <div
      v-else
      class="reader-scroll"
      ref="scrollRef"
      @scroll="onScroll"
      @contextmenu="onContextMenu"
    >
      <article class="article reader-content" :key="a.id">
        <button
          type="button"
          class="article-feed"
          :title="t('reader.viewAllFromFeed')"
          @click="ui.select({ kind: 'feed', value: a.feedId }, a.feedTitle)"
        >
          <Icon name="rss" :size="13" />
          {{ a.feedTitle }}
        </button>
        <h1 class="article-title">{{ displayTitle }}</h1>
        <div class="article-meta">
          <span v-if="a.author" class="author">{{ a.author }}</span>
          <span v-if="a.author && a.publishedAt">·</span>
          <span v-if="a.publishedAt">{{ fullDate(a.publishedAt) }}</span>
          <template v-if="ui.prefs.showReadingTime">
            <span>·</span>
            <span>{{ t("reader.readMinutes", { count: readMinutes }) }}</span>
          </template>
          <template v-if="extract.isPending.value">
            <span>·</span>
            <span class="meta-extracting">
              <span class="meta-extract-dot" aria-hidden="true" />
              {{ t("reader.extractingFullText") }}
            </span>
          </template>
          <!-- Always-visible cue that the body is a translation (esp. auto-translated). -->
          <template v-if="showTranslation && hasTranslation">
            <span>·</span>
            <span class="meta-trans">
              <Icon name="translate" :size="12" />
              {{ t("reader.translatedVia", { engine: engineLabel(engine) }) }}
            </span>
          </template>
        </div>

        <div v-if="a.tags.length > 0" class="article-tags">
          <button
            v-for="tag in a.tags"
            :key="tag.id"
            class="article-tag"
            :style="tagChipStyle(tag.color)"
            @click="selectTag(tag.id, tag.name)"
          >
            <span class="tag-dot" />
            {{ tag.name }}
          </button>
        </div>

        <!-- AI summary: inline card at the top of the article body, shown when
             toggled (toolbar ✦ / I key). The article's :key remounts it per
             article, so it re-streams for each. -->
        <AIDrawer
          v-if="ui.aiOpen"
          :article="a"
          :on-close="() => ui.setAiOpen(false)"
          :on-open-settings="() => emit('open-settings', 'ai')"
        />

        <iframe
          v-if="ytId"
          class="article-embed"
          :src="`https://www.youtube-nocookie.com/embed/${ytId}`"
          :title="a.title"
          referrerpolicy="strict-origin-when-cross-origin"
          allowfullscreen
        />
        <img
          v-else-if="showHero"
          class="article-hero"
          :src="heroDataUrl ?? a.imageUrl!"
          alt=""
          :data-papr-src="heroDataUrl ? a.imageUrl : undefined"
          referrerpolicy="no-referrer"
          @click="openHeroViewer"
          @error="onHeroError"
        />

        <button
          v-for="(e, i) in audioEnclosures"
          :class="['episode', playingSrc === e.url ? 'playing' : '']"
          :key="`a${i}`"
          @click="playEnclosure(e.url)"
        >
          <span class="episode-play">
            <Icon :name="playingSrc === e.url ? 'pause' : 'play'" :size="15" />
          </span>
          <span class="episode-text">
            {{ playingSrc === e.url ? t("reader.episodePlaying") : t("reader.episodePlay") }}
          </span>
        </button>
        <div v-for="(e, i) in videoEnclosures" class="enclosure" :key="`v${i}`">
          <video controls :src="e.url" />
        </div>

        <!-- Optional always-visible Original / Bilingual / Translation switch
             (settings → Reading). The toolbar popover always offers these too. -->
        <div
          v-if="ui.prefs.showTransSwitch && (hasTranslation || translating)"
          class="tr-toggle"
          role="group"
          :aria-label="t('reader.tbTranslate')"
        >
          <button
            :class="{ on: transView === 'original' }"
            :aria-pressed="transView === 'original'"
            @click="setMode('original')"
          >
            {{ t("reader.original") }}
          </button>
          <button
            :class="{ on: transView === 'bilingual' }"
            :aria-pressed="transView === 'bilingual'"
            @click="setMode('bilingual')"
          >
            {{ t("reader.bilingual") }}
          </button>
          <button
            :class="{ on: transView === 'translation' }"
            :aria-pressed="transView === 'translation'"
            @click="setMode('translation')"
          >
            {{ t("reader.translation") }}
          </button>
          <span v-if="translating" class="tr-progress">
            {{ t("reader.translating")
            }}{{
              jobForTarget && jobForTarget.total > 0
                ? ` ${jobForTarget.done}/${jobForTarget.total}`
                : ""
            }}
          </span>
        </div>

        <div
          class="article-body"
          ref="bodyRef"
          @click="onBodyClick"
          v-html="displayBody || `<p><em>${t('reader.noContent')}</em></p>`"
        />
      </article>
    </div>

    <!-- Back-to-top for long articles (reader view only; the web view is a
         native child window). Fades in once ~a screenful has scrolled past. -->
    <Transition name="totop-fade">
      <button
        v-if="showToTop && viewMode !== 'web'"
        class="reader-totop"
        :title="t('reader.backToTop')"
        :aria-label="t('reader.backToTop')"
        @click="scrollToTop"
      >
        <Icon name="arrow-down" :size="17" />
      </button>
    </Transition>

    <ImageViewer
      v-if="viewer"
      :srcs="viewer.srcs"
      :index="viewer.index"
      @close="viewer = null"
    />

    <TagPicker
      v-if="tagPick"
      :article-id="a.id"
      :attached="a.tags.map((tg) => tg.id)"
      :x="tagPick.x"
      :y="tagPick.y"
      :on-close="() => (tagPick = null)"
    />

    <ContextMenu
      v-if="ctxMenu"
      :x="ctxMenu.x"
      :y="ctxMenu.y"
      :items="ctxItems"
      :on-close="() => (ctxMenu = null)"
    />
  </div>
</template>
