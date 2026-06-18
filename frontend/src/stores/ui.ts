// Global UI state (Pinia). Server data (feeds, articles…) lives in Vue Query;
// this store holds view selection plus the appearance preferences the settings
// controls drive. Ported from papr's zustand src/store.ts.

import { defineStore } from "pinia";
import { computed, ref, watch } from "vue";
import i18n from "../i18n";
import * as api from "../api";
import type { ArticleQuery } from "../types";
import type { InstalledFont } from "../../bindings/hive/internal/db/models";

export type Theme = "light" | "dark" | "auto";
export type ResolvedTheme = "light" | "dark";
export type DarkShade = "default" | "dimmer" | "black";
export type Accent =
  | "gold" | "azure" | "cyan" | "emerald" | "indigo" | "violet" | "slate"
  // "custom" pairs with the customAccent hex below.
  | "custom";

/** Every accent key the store accepts (persistence whitelist + picker order). */
export const ACCENT_KEYS = [
  "gold", "azure", "cyan", "emerald", "indigo", "violet", "slate", "custom",
] as const;
export type Density = "compact" | "cozy" | "spacious";
export type ViewMode = "list" | "card";
export type StartupView = "all" | "unread" | "starred" | "last";
export type ReaderFont = "serif" | "sans" | "hyperlegible";

export const READER_FONTS: Record<ReaderFont, { stack: string; adjust: string }> = {
  serif: { stack: "var(--serif)", adjust: "0px" },
  sans: { stack: "var(--ui)", adjust: "-1.5px" },
  hyperlegible: { stack: "'Atkinson Hyperlegible', var(--ui)", adjust: "-1.5px" },
};

export const READER_BOUNDS = {
  size: { min: 14, max: 22 },
  leading: { min: 130, max: 200 },
  width: { min: 520, max: 1920 },
} as const;

/** Factory defaults the in-reader Aa panel's Reset restores. */
export const READER_DEFAULTS = {
  font: "sans",
  size: 17,
  leading: 170,
  width: 1080,
} as const;

/** Valid ranges for the draggable pane widths — shared by the resize handles,
 *  persistence validation, and the `setPanel` write guard so they stay in
 *  lockstep. `sidebar` is the panel width (the fixed icon rail is separate); the
 *  AI drawer can grow wide but never crowd out the reading sheet. hive's article
 *  list is a `1fr` column (it auto-fills), so it has no width handle. */
export const PANEL_BOUNDS = {
  sidebar: { min: 180, max: 360 },
  ai: { min: 280, max: 560 },
} as const;

function clamp(n: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, n));
}

/** How an article opens by default: reader view, auto-extracted full text, or
 *  the embedded web view. Individual feeds can override it (Feed.openMode). */
export type OpenMode = "reader" | "extracted" | "web";
export const OPEN_MODES: readonly OpenMode[] = ["reader", "extracted", "web"];

export interface Prefs {
  showSidebarCounts: boolean;
  showCardThumbs: boolean;
  reduceMotion: boolean;
  showReadingTime: boolean;
  // Show the in-article Original / Bilingual / Translation switch above a
  // translated body (the toolbar popover always offers it; this is the optional
  // always-visible shortcut).
  showTransSwitch: boolean;
  markReadOnOpen: boolean;
  markReadOnScroll: boolean;
  /** How an article opens by default: reader view, auto-extracted full text,
   *  or the original page in the native web view. Individual feeds can
   *  override it (Feed.openMode). */
  defaultOpenMode: OpenMode;
  startupView: StartupView;
  hideReadOnStartup: boolean;
  /** Sidebar "unread only" mode — hide feeds with no unread articles. */
  sidebarUnreadOnly: boolean;
}

const ls = {
  oneOf: <T extends string>(k: string, allowed: readonly T[], fallback: T): T => {
    const v = localStorage.getItem(k);
    return v != null && (allowed as readonly string[]).includes(v) ? (v as T) : fallback;
  },
  num: (k: string, fallback: number, min: number, max: number) => {
    const v = localStorage.getItem(k);
    if (v == null) return fallback;
    const n = Number(v);
    if (!Number.isFinite(n)) return fallback;
    return clamp(n, min, max);
  },
  bool: (k: string, fallback: boolean) => {
    const v = localStorage.getItem(k);
    return v == null ? fallback : v === "1";
  },
  // A #rrggbb hex — the shape <input type="color"> emits; anything else
  // (corrupt value, older format) falls back rather than reaching the CSS.
  hex: (k: string, fallback: string) => {
    const v = localStorage.getItem(k);
    return v != null && /^#[0-9a-fA-F]{6}$/.test(v) ? v : fallback;
  },
  set: (k: string, v: string | number | boolean) =>
    localStorage.setItem(k, typeof v === "boolean" ? (v ? "1" : "0") : String(v)),
};

const PREF_KEYS: (keyof Prefs)[] = [
  "showSidebarCounts",
  "showCardThumbs",
  "reduceMotion",
  "showReadingTime",
  "showTransSwitch",
  "markReadOnOpen",
  "markReadOnScroll",
  "defaultOpenMode",
  "startupView",
  "hideReadOnStartup",
  "sidebarUnreadOnly",
];

function mirrorTheme(theme: ResolvedTheme): void {
  // Mirror the *resolved* theme (auto → light/dark) so the native launch
  // background tracks the effective appearance, not the abstract preference.
  api.setSetting("theme", theme).catch(() => {});
}

// Watches `prefers-color-scheme: dark` so the "auto" theme can follow the OS
// without a reload. Returns a ref that reflects the live media-query state and
// stays subscribed for the lifetime of the app — Pinia stores are singletons.
function watchSystemDark(): { prefersDark: import("vue").Ref<boolean> } {
  const mq = typeof window !== "undefined" && typeof window.matchMedia === "function"
    ? window.matchMedia("(prefers-color-scheme: dark)")
    : null;
  const prefersDark = ref<boolean>(mq?.matches ?? false);
  if (mq) {
    const onChange = (e: MediaQueryListEvent) => { prefersDark.value = e.matches; };
    if (typeof mq.addEventListener === "function") mq.addEventListener("change", onChange);
    else if (typeof (mq as any).addListener === "function") (mq as any).addListener(onChange);
  }
  return { prefersDark };
}
function mirrorDarkShade(shade: DarkShade): void {
  api.setSetting("dark_shade", shade).catch(() => {});
}

function loadReaderFont(): string {
  // Any stored value is valid: a bundled key, "system:<name>", or
  // "downloaded:<id>" — the resolver maps an unknown/deleted one back to a
  // bundled face. Empty falls back to the legacy serif flag, else sans (hive's
  // cool-tech default).
  const v = localStorage.getItem("readerFont");
  if (v) return v;
  return localStorage.getItem("useSerif") === "1" ? "serif" : "sans";
}

function loadPrefs(): Prefs {
  return {
    showSidebarCounts: ls.bool("pref.showSidebarCounts", true),
    showCardThumbs: ls.bool("pref.showCardThumbs", true),
    reduceMotion: ls.bool("pref.reduceMotion", false),
    showReadingTime: ls.bool("pref.showReadingTime", true),
    showTransSwitch: ls.bool("pref.showTransSwitch", false),
    markReadOnOpen: ls.bool("pref.markReadOnOpen", true),
    markReadOnScroll: ls.bool("pref.markReadOnScroll", false),
    // Migrates the older boolean "auto-extract full text" toggle: a user who
    // had it on keeps auto-extraction as their default open mode.
    defaultOpenMode: ls.oneOf<OpenMode>(
      "pref.defaultOpenMode",
      OPEN_MODES,
      ls.bool("pref.autoExtract", false) ? "extracted" : "reader",
    ),
    startupView: ls.oneOf<StartupView>("pref.startupView", ["all", "unread", "starred", "last"], "unread"),
    hideReadOnStartup: ls.bool("pref.hideReadOnStartup", false),
    sidebarUnreadOnly: ls.bool("pref.sidebarUnreadOnly", false),
  };
}

export const useUi = defineStore("ui", () => {
  const query = ref<ArticleQuery>({ kind: "all" });
  const queryLabel = ref<string>(i18n.global.t("smart.all"));
  const selectedArticleId = ref<number | null>(null);
  const unreadOnly = ref(false);
  const sortOldest = ref(false);
  const middlePane = ref<"articles" | "highlights">("articles");
  const pendingHighlightId = ref<number | null>(null);

  const theme = ref<Theme>(ls.oneOf<Theme>("theme", ["light", "dark", "auto"], "light"));
  const { prefersDark } = watchSystemDark();
  const resolvedTheme = computed<ResolvedTheme>(() =>
    theme.value === "auto" ? (prefersDark.value ? "dark" : "light") : theme.value,
  );
  const darkShade = ref<DarkShade>(ls.oneOf<DarkShade>("darkShade", ["default", "dimmer", "black"], "default"));
  const accent = ref<Accent>(ls.oneOf<Accent>("accent", ACCENT_KEYS, "gold"));
  // The user-picked hex behind the "custom" accent. Kept even while a preset
  // is active so re-selecting custom restores the last pick.
  const customAccent = ref<string>(ls.hex("customAccent", "#3f76e4"));
  const density = ref<Density>(ls.oneOf<Density>("density", ["compact", "cozy", "spacious"], "cozy"));
  const viewMode = ref<ViewMode>(ls.oneOf<ViewMode>("viewMode", ["list", "card"], "list"));
  const readerFont = ref<string>(loadReaderFont());
  const uiFont = ref<string>(localStorage.getItem("uiFont") ?? "default");
  // Downloaded fonts (from the backend) — used to resolve a "downloaded:<id>"
  // choice to its family and to drive the @font-face block. App.vue seeds this
  // on mount and refreshes it after a download / delete.
  const installedFonts = ref<InstalledFont[]>([]);
  const readerSize = ref(ls.num("readerSize", READER_DEFAULTS.size, READER_BOUNDS.size.min, READER_BOUNDS.size.max));
  const readerLeading = ref(ls.num("readerLeading", READER_DEFAULTS.leading, READER_BOUNDS.leading.min, READER_BOUNDS.leading.max));
  const readerWidth = ref(ls.num("readerWidth", READER_DEFAULTS.width, READER_BOUNDS.width.min, READER_BOUNDS.width.max));

  // Draggable pane widths (px) — the sidebar panel and the AI summary drawer.
  const sidebarWidth = ref(ls.num("sidebarWidth", 232, PANEL_BOUNDS.sidebar.min, PANEL_BOUNDS.sidebar.max));
  const aiWidth = ref(ls.num("aiWidth", 360, PANEL_BOUNDS.ai.min, PANEL_BOUNDS.ai.max));

  const prefs = ref<Prefs>(loadPrefs());
  const focusMode = ref(false);
  const aiOpen = ref(false);

  function select(q: ArticleQuery, label: string) {
    ls.set("lastView", JSON.stringify({ query: q, label }));
    query.value = q;
    queryLabel.value = label;
    selectedArticleId.value = null;
    middlePane.value = "articles";
  }
  function showHighlights() {
    middlePane.value = "highlights";
    selectedArticleId.value = null;
  }
  // Open the article identified by `articleId` and stash the highlight id so
  // HighlightLayer can scroll to it once the marks are applied. Does NOT touch
  // middlePane: when the user closes the Reader overlay, they return to the
  // HighlightsPane that they came from.
  function openHighlight(highlightId: number, articleId: number) {
    pendingHighlightId.value = highlightId;
    selectedArticleId.value = articleId;
  }
  function consumePendingHighlight(): number | null {
    const id = pendingHighlightId.value;
    pendingHighlightId.value = null;
    return id;
  }
  const openArticle = (id: number | null) => (selectedArticleId.value = id);
  const toggleUnreadOnly = () => (unreadOnly.value = !unreadOnly.value);
  const toggleSort = () => (sortOldest.value = !sortOldest.value);

  const setTheme = (t: Theme) => {
    ls.set("theme", t);
    theme.value = t;
    mirrorTheme(t === "auto" ? (prefersDark.value ? "dark" : "light") : t);
  };
  const setDarkShade = (s: DarkShade) => { ls.set("darkShade", s); mirrorDarkShade(s); darkShade.value = s; };
  const setAccent = (a: Accent) => { ls.set("accent", a); accent.value = a; };
  // Picking a custom colour also selects the custom accent — one gesture.
  const setCustomAccent = (hex: string) => {
    if (!/^#[0-9a-fA-F]{6}$/.test(hex)) return;
    ls.set("customAccent", hex);
    customAccent.value = hex;
    setAccent("custom");
  };
  const setDensity = (d: Density) => { ls.set("density", d); density.value = d; };
  const setViewMode = (v: ViewMode) => { ls.set("viewMode", v); viewMode.value = v; };
  const setReaderFont = (v: string) => { ls.set("readerFont", v); readerFont.value = v; };
  const setUiFont = (v: string) => { ls.set("uiFont", v); uiFont.value = v; };
  const setInstalledFonts = (v: InstalledFont[]) => { installedFonts.value = v; };
  function setReader(p: { readerSize?: number; readerLeading?: number; readerWidth?: number }) {
    if (p.readerSize != null) {
      readerSize.value = clamp(p.readerSize, READER_BOUNDS.size.min, READER_BOUNDS.size.max);
      ls.set("readerSize", readerSize.value);
    }
    if (p.readerLeading != null) {
      readerLeading.value = clamp(p.readerLeading, READER_BOUNDS.leading.min, READER_BOUNDS.leading.max);
      ls.set("readerLeading", readerLeading.value);
    }
    if (p.readerWidth != null) {
      readerWidth.value = clamp(p.readerWidth, READER_BOUNDS.width.min, READER_BOUNDS.width.max);
      ls.set("readerWidth", readerWidth.value);
    }
  }
  function resetReader() {
    setReaderFont(READER_DEFAULTS.font);
    setReader({
      readerSize: READER_DEFAULTS.size,
      readerLeading: READER_DEFAULTS.leading,
      readerWidth: READER_DEFAULTS.width,
    });
  }
  const isReaderDefault = computed(() =>
    readerFont.value === READER_DEFAULTS.font &&
    readerSize.value === READER_DEFAULTS.size &&
    readerLeading.value === READER_DEFAULTS.leading &&
    readerWidth.value === READER_DEFAULTS.width,
  );
  // Clamp on write so neither a drag past the handle's guard nor a stale
  // persisted value (from an older build with different limits) can push an
  // out-of-range width into the store or its --col-*/--ai-width CSS variable.
  function setPanel(p: { sidebarWidth?: number; aiWidth?: number }) {
    if (p.sidebarWidth != null) {
      sidebarWidth.value = clamp(p.sidebarWidth, PANEL_BOUNDS.sidebar.min, PANEL_BOUNDS.sidebar.max);
      ls.set("sidebarWidth", sidebarWidth.value);
    }
    if (p.aiWidth != null) {
      aiWidth.value = clamp(p.aiWidth, PANEL_BOUNDS.ai.min, PANEL_BOUNDS.ai.max);
      ls.set("aiWidth", aiWidth.value);
    }
  }
  function setPref(patch: Partial<Prefs>) {
    for (const k of PREF_KEYS) {
      if (patch[k] !== undefined) ls.set(`pref.${k}`, patch[k] as string | boolean);
    }
    prefs.value = { ...prefs.value, ...patch };
  }
  // System-level fullscreen. Hitting F / the reader toolbar / the ⌘K
  // command flips the actual OS window into fullscreen (macOS full-screen
  // space; Windows/Linux frameless). Local ref stays in sync via the
  // boolean the backend returns after the toggle, so `focusMode`
  // reflects the true window state even when the user triggered it
  // natively (green traffic-light click, F11 on Windows).
  async function setFocusMode(v: boolean) {
    if (v === focusMode.value) return;
    try {
      focusMode.value = await api.toggleFullscreen();
    } catch { /* swallow — worst case, ref stays as-is */ }
  }
  const setAiOpen = (v: boolean) => (aiOpen.value = v);

  // Seed the backend theme copy so the native launch background is themed.
  // Use the resolved value so "auto" mirrors the OS preference at launch.
  mirrorTheme(resolvedTheme.value);
  mirrorDarkShade(darkShade.value);

  // Keep the backend mirror in sync when the OS flips appearance while the
  // user has "auto" selected — setTheme already mirrors on explicit changes.
  watch(resolvedTheme, (v) => { mirrorTheme(v); });

  return {
    query, queryLabel, selectedArticleId, unreadOnly, sortOldest,
    middlePane, pendingHighlightId,
    theme, resolvedTheme, darkShade, accent, customAccent, density, viewMode, readerFont, uiFont, installedFonts,
    readerSize, readerLeading, readerWidth,
    sidebarWidth, aiWidth,
    prefs, focusMode, aiOpen,
    select, openArticle, toggleUnreadOnly, toggleSort,
    showHighlights, openHighlight, consumePendingHighlight,
    setTheme, setDarkShade, setAccent, setCustomAccent, setDensity, setViewMode, setReaderFont, setReader, resetReader, isReaderDefault, setPanel,
    setUiFont, setInstalledFonts,
    setPref, setFocusMode, setAiOpen,
  };
});
