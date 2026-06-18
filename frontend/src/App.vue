<script setup lang="ts">
// Root shell: the 3-pane window (Sidebar | ArticleList | Reader) + PlayerBar,
// the modal stack, appearance/startup effects, the global keyboard map, and the
// toast region. Ported from papr's React App.tsx.

import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useQueryClient } from "@tanstack/vue-query";
import { Browser, Events } from "@wailsio/runtime";
import * as api from "./api";
import { useUi, PANEL_BOUNDS, type DarkShade } from "./stores/ui";
import { usePlayer } from "./stores/player";
import { resolveAccent } from "./lib/accents";
import { resolveReaderFont, resolveUiFont, fontFaceCss } from "./lib/fonts";
import { useToasts, toast as toastApi, reportError } from "./stores/toasts";
import { useArticleActions } from "./composables/articleActions";
import { currentList } from "./lib/currentList";
import { checkForUpdates } from "./lib/updater";
import { isMac } from "./lib/platform";
import type { ArticleQuery, ArticleSummary, Feed } from "./types";
import Sidebar from "./components/Sidebar.vue";
import ArticleList from "./components/ArticleList.vue";
const HighlightsPane = defineAsyncComponent(() => import("./components/HighlightsPane.vue"));
const AIAssistant = defineAsyncComponent(() => import("./components/AIAssistant.vue"));
import Reader from "./components/Reader.vue";
import ResizeHandle from "./components/ResizeHandle.vue";
import CommandPalette from "./components/CommandPalette.vue";
import SettingsDialog from "./components/SettingsDialog.vue";
import FeedEditDialog from "./components/FeedEditDialog.vue";
import SourceHub from "./components/SourceHub.vue";
import PromptDialog from "./components/PromptDialog.vue";
import PlayerBar from "./components/PlayerBar.vue";
import Icon from "./components/Icon.vue";

const { t } = useI18n();
const qc = useQueryClient();
const ui = useUi();
const player = usePlayer();
const toasts = useToasts();
const actions = useArticleActions();

// Accent palettes live in lib/accents.ts, shared with the Appearance settings
// swatches so the picker always shows exactly what gets applied. Default is
// gold — the same ember as the dock icon. Cool presets stay in the picker;
// papr's terracotta paper is a different product and stays out of this map.
const DARK_PAPER: Record<DarkShade, string> = { default: "#12110F", dimmer: "#0C0B0A", black: "#000000" };

const refreshing = ref(false);
const cpOpen = ref(false);
const settings = reactive<{ open: boolean; section?: string }>({ open: false });
// The feed whose lightweight edit dialog is open (sidebar right-click → Edit…).
const feedEditId = ref<number | null>(null);
// The Source Hub (发现订阅源) — the one surface for browsing the curated
// directory, searching it, pasting URLs, and IMAP newsletters. Every former
// add-feed / explore entry opens it; hubUrl prefills its input (deep links).
const hub = ref(false);
const hubUrl = ref<string | undefined>(undefined);
const newFolder = ref(false);
const aiAssistant = reactive<{ open: boolean; mode: "ask" | "digest" | "chat" }>({ open: false, mode: "chat" });

// Suspend the in-app page-view window while any global modal is open — the
// native child window (Reader's web mode) otherwise floats above these
// in-webview overlays and covers them. Resumes once they all close.
watch(
  () => cpOpen.value || settings.open || hub.value || newFolder.value || aiAssistant.open,
  (overlay) => {
    api.setPageViewSuspended(overlay).catch(() => {});
  },
);

const showToast = toastApi.show;

// ── platform → document root ──
// macOS overlays the traffic-light buttons on the webview's top-left; the
// sidebar/list-header reserve a top strip via `:root[data-platform="mac"]`
// CSS rules, so this attribute MUST be set or the brand row sits under the
// traffic lights. (papr set this in main.tsx; it was dropped in the port.)
document.documentElement.dataset.platform = isMac ? "mac" : "other";

// ── appearance → document root ──
watch(
  // The *resolved* theme (auto → light/dark, live via prefers-color-scheme):
  // stamping the raw "auto" pref onto data-theme would match no CSS rule and
  // leave the app light on a dark OS, and it must re-run when the OS flips.
  () => [ui.resolvedTheme, ui.darkShade, ui.accent, ui.customAccent, ui.density],
  () => {
    const root = document.documentElement;
    root.dataset.theme = ui.resolvedTheme;
    root.dataset.darkShade = ui.darkShade;
    root.dataset.density = ui.density;
    // Preset key → its palette; "custom" → a palette derived from the user's
    // hex via the same recipe (lib/accents.ts).
    const a = resolveAccent(ui.accent, ui.customAccent);
    const dark = ui.resolvedTheme === "dark";
    root.style.setProperty("--accent", dark ? a.dAccent : a.accent);
    root.style.setProperty("--accent-soft", dark ? a.dSoft : a.soft);
    root.style.setProperty("--accent-ink", dark ? a.dInk : a.ink);
    // Mirror the theme's paper colour onto the native window backing, so the
    // strip a fast macOS resize exposes beyond the webview's last frame
    // matches the theme instead of flashing light paper in dark mode. The
    // cold-start colour comes from the mirrored settings (main.go
    // startupBackground); this keeps it live across theme changes.
    const hex = dark ? DARK_PAPER[ui.darkShade] : "#F0EEE9";
    const n = parseInt(hex.slice(1), 16);
    api.setWindowBackground((n >> 16) & 0xff, (n >> 8) & 0xff, n & 0xff).catch(() => {});
  },
  { immediate: true },
);

watch(() => ui.prefs.reduceMotion, (v) => {
  document.documentElement.dataset.reduceMotion = String(v);
}, { immediate: true });

// ── draggable pane width → CSS var ──
// The sidebar resize handle writes to the store; this mirrors the value onto
// the document root, driving `.window`'s sidebar grid column.
watch(
  () => ui.sidebarWidth,
  (w) => {
    document.documentElement.style.setProperty("--col-sidebar", `${w}px`);
  },
  { immediate: true },
);

// ── reader typography → CSS vars ──
// resolveReaderFont handles bundled keys (serif/sans/hyperlegible),
// "system:<name>", and "downloaded:<id>" — a raw READER_FONTS[key]
// lookup would return undefined for the last two and blow up on
// `.stack`. installedFonts feeds it the family map for downloaded ids.
watch(
  () => [ui.readerFont, ui.readerSize, ui.readerLeading, ui.readerWidth, ui.installedFonts],
  () => {
    const root = document.documentElement.style;
    const font = resolveReaderFont(ui.readerFont, ui.installedFonts);
    root.setProperty("--reader-font", font.stack);
    root.setProperty("--reader-font-adjust", font.adjust);
    root.setProperty("--reader-size", `${ui.readerSize}px`);
    root.setProperty("--reader-leading", String(ui.readerLeading / 100));
    root.setProperty("--reader-width", `${ui.readerWidth}px`);
  },
  { immediate: true },
);

// ── UI-font override → --ui ──
// resolveUiFont returns null for "default" (the bundled Inter Tight
// stays in effect via the stylesheet's own --ui declaration). For
// "system:<name>" or "downloaded:<id>" it returns a stack that does
// NOT reference var(--ui) — a self-reference would be circular since
// we're overriding --ui itself. We remove the property when the user
// picks "default" so the stylesheet's default takes over cleanly.
watch(
  () => [ui.uiFont, ui.installedFonts],
  () => {
    const root = document.documentElement.style;
    const stack = resolveUiFont(ui.uiFont, ui.installedFonts);
    if (stack) root.setProperty("--ui", stack);
    else root.removeProperty("--ui");
  },
  { immediate: true },
);

// ── @font-face block for downloaded fonts ──
// Downloaded fonts are files served by the Go middleware at
// /userfonts/<file>; a `@font-face { src: url(...) }` block registers
// the family with the browser so `font-family: '<family>'` in the
// resolved stacks actually finds a face. Without this, picking a
// downloaded font would silently fall through to the fallback tail.
// The block goes into a single <style id="hl-fontface"> tag that we
// keep in sync with the ui.installedFonts list.
watch(
  () => ui.installedFonts,
  (installed) => {
    const id = "hl-fontface";
    let el = document.getElementById(id) as HTMLStyleElement | null;
    if (!el) {
      el = document.createElement("style");
      el.id = id;
      document.head.appendChild(el);
    }
    el.textContent = fontFaceCss(installed);
  },
  { immediate: true, deep: true },
);

// ── Seed installedFonts on mount ──
// FontService.ListInstalled reads the installed_fonts DB table, which
// is what @font-face and the family resolvers depend on. Without
// this, a fresh app open shows every "downloaded:..." choice as its
// fallback tail (Inter Tight etc.) until the settings dialog is
// opened, since the settings page is currently the only place that
// calls ListInstalled.
onMounted(() => {
  api.listInstalledFonts().then(ui.setInstalledFonts).catch(() => {});
});

// ── toast dwell timer ──
// One live timer at a time: clear the previous toast's dwell timer before
// arming the next, so a stale timer can't dismiss a freshly-shown toast early
// (and they don't leak when toasts fire in quick succession).
let toastTimer: number | undefined;
watch(() => toasts.current, (cur) => {
  if (toastTimer) window.clearTimeout(toastTimer);
  if (!cur) return;
  const id = cur.id;
  toastTimer = window.setTimeout(() => toasts.dismiss(id), cur.duration);
});

function openSettings(section?: string) {
  settings.open = true;
  settings.section = section;
}

/** Sidebar "Edit…" on a feed → the lightweight per-feed dialog, opened in place
 *  rather than dragging the whole Settings window over the reading view. */
function editFeed(feedId: number) {
  feedEditId.value = feedId;
}

function applyStartupView() {
  const { startupView, hideReadOnStartup } = ui.prefs;
  const labels: Record<string, string> = {
    all: t("smart.all"),
    unread: t("smart.unread"),
    starred: t("smart.starred"),
    readLater: t("smart.readLater"),
  };
  if (startupView !== "last" && labels[startupView]) {
    ui.select({ kind: startupView } as ArticleQuery, labels[startupView]);
  } else if (startupView === "last") {
    try {
      const raw = localStorage.getItem("lastView");
      if (raw) {
        const saved = JSON.parse(raw) as { query?: ArticleQuery; label?: string };
        if (saved.query?.kind) {
          const label = labels[saved.query.kind] ?? saved.label ?? "";
          ui.select(saved.query, label);
        }
      }
    } catch { /* ignore corrupt value */ }
  }
  if (hideReadOnStartup && !ui.unreadOnly) ui.toggleUnreadOnly();
}

let refreshingGuard = false;
// With no `scope` this refreshes every feed (toolbar button); the sidebar's
// per-feed / per-folder context menus pass `{ feedId }` / `{ folderId }`.
function doRefresh(scope?: { feedId?: number; folderId?: number }) {
  if (refreshingGuard) return;
  refreshingGuard = true;
  refreshing.value = true;
  // The in-progress toast names the actual scope — a single-feed refresh
  // shouldn't claim to be refreshing every feed.
  showToast(
    scope?.feedId != null
      ? t("app.refreshingFeed")
      : scope?.folderId != null
        ? t("app.refreshingFolder")
        : t("app.refreshing"),
  );
  api
    .refreshFeeds(undefined, scope)
    .then((n) => {
      actions.refreshAfterFetch();
      showToast(n > 0 ? t("app.foundNew", { count: n }) : t("app.upToDate"));
    })
    .catch(reportError)
    .finally(() => {
      refreshingGuard = false;
      refreshing.value = false;
    });
}

async function markAllRead() {
  try {
    const n = await api.markAllRead(ui.query);
    actions.refreshAfterBulk();
    showToast(n > 0 ? t("app.markedRead", { count: n }) : t("app.nothingToMark"));
  } catch (e) {
    reportError(e);
  }
}

function handleCommand(action: string) {
  switch (action) {
    case "mark-all-read": markAllRead(); break;
    case "toggle-theme": ui.setTheme(ui.theme === "light" ? "dark" : "light"); break;
    case "toggle-focus": ui.setFocusMode(!ui.focusMode); break;
    case "toggle-ai": if (ui.selectedArticleId != null) ui.setAiOpen(!ui.aiOpen); break;
    case "refresh": doRefresh(); break;
    case "add-feed": hub.value = true; break;
    case "new-folder": newFolder.value = true; break;
    case "opml": openSettings("subscriptions"); break;
    case "open-settings": openSettings(); break;
    case "ai-ask": aiAssistant.mode = "ask"; aiAssistant.open = true; break;
    case "ai-digest": aiAssistant.mode = "digest"; aiAssistant.open = true; break;
  }
}

function navigateFeed(feed: Feed) {
  ui.select({ kind: "feed", value: feed.id }, feed.title);
}
function navigateArticle(a: ArticleSummary) {
  ui.select({ kind: "feed", value: a.feedId }, a.feedTitle);
  ui.openArticle(a.id);
}
function onOpenHighlight(payload: { highlightId: number; articleId: number }): void {
  ui.openHighlight(payload.highlightId, payload.articleId);
}

// ── Reader-overlay navigation ───────────────────────────────────────────────
// The overlay's ‹ ›  buttons and the ← → keyboard shortcuts flip between
// articles in the current list. Position is looked up on demand — the
// computed re-runs when selectedArticleId changes (each nav), which is
// when we actually need it.
const readerPos = computed(() => {
  if (ui.selectedArticleId == null) return { idx: -1, len: 0 };
  const items = currentList(qc);
  return { idx: items.findIndex((a) => a.id === ui.selectedArticleId), len: items.length };
});
const hasPrev = computed(
  () => ui.middlePane === "articles" && readerPos.value.idx > 0,
);
const hasNext = computed(
  () =>
    ui.middlePane === "articles" &&
    readerPos.value.idx >= 0 &&
    readerPos.value.idx < readerPos.value.len - 1,
);
function readerNav(delta: number) {
  const items = currentList(qc);
  const idx = items.findIndex((a) => a.id === ui.selectedArticleId);
  const next = items[idx + delta];
  if (next) ui.openArticle(next.id);
}
function closeReader() {
  ui.openArticle(null);
}

function openExternal(url: string) {
  try {
    Browser.OpenURL(url);
  } catch {
    window.open(url, "_blank");
  }
}

// ── global keyboard shortcuts ──
function onKey(e: KeyboardEvent) {
  const tag = (e.target as HTMLElement)?.tagName;
  const inField = tag === "INPUT" || tag === "TEXTAREA";
  const mod = e.metaKey || e.ctrlKey;

  if (mod && e.key.toLowerCase() === "k") {
    e.preventDefault();
    const isCp = !!document.querySelector(".cp-backdrop");
    if (!isCp && document.querySelector(".settings-backdrop, .modal-backdrop, .tag-picker, .hl-popover, .image-viewer")) return;
    cpOpen.value = !cpOpen.value;
    return;
  }
  if (mod && e.key === ",") {
    e.preventDefault();
    const isSettings = !!document.querySelector(".settings-backdrop");
    if (!isSettings && document.querySelector(".cp-backdrop, .modal-backdrop, .tag-picker, .hl-popover, .image-viewer")) return;
    settings.open = !settings.open;
    return;
  }
  if (mod && e.key.toLowerCase() === "r") {
    e.preventDefault();
    doRefresh();
    return;
  }
  if (mod) return;
  if (inField) return;
  if (document.querySelector(".cp-backdrop, .settings-backdrop, .modal-backdrop, .ctx-menu, .tag-picker, .hl-popover, .hl-toolbar, .image-viewer, .rs-pop, .tr-pop, .tb-more-pop")) return;

  const items = currentList(qc);
  const idx = items.findIndex((a) => a.id === ui.selectedArticleId);
  const sel = idx >= 0 ? items[idx] : undefined;
  const go = (delta: number) => {
    if (items.length === 0) return;
    const next = items[Math.min(items.length - 1, Math.max(0, idx + delta))];
    if (next) ui.openArticle(next.id);
  };

  switch (e.key.toLowerCase()) {
    case "j": e.preventDefault(); go(idx < 0 ? 0 : 1); break;
    case "k": e.preventDefault(); go(-1); break;
    case "o": if (sel?.url) { e.preventDefault(); openExternal(sel.url); } break;
    case "s":
      if (sel) {
        e.preventDefault();
        actions.setStarred(sel.id, !sel.isStarred);
        showToast(sel.isStarred ? t("app.starRemoved") : t("app.starred"), "S");
      }
      break;
    case "b":
      if (sel) {
        e.preventDefault();
        actions.setReadLater(sel.id, !sel.readLater);
        showToast(sel.readLater ? t("app.readLaterRemoved") : t("app.readLaterAdded"), "B");
      }
      break;
    case "u": if (sel) { e.preventDefault(); actions.setRead(sel.id, !sel.isRead); } break;
    case "c":
      if (sel?.url) {
        e.preventDefault();
        navigator.clipboard.writeText(sel.url).then(
          () => showToast(t("app.linkCopied"), "C"),
          () => {},
        );
      }
      break;
    case "i": if (ui.selectedArticleId != null) { e.preventDefault(); ui.setAiOpen(!ui.aiOpen); } break;
    case "f": e.preventDefault(); ui.setFocusMode(!ui.focusMode); break;
    case "v": e.preventDefault(); ui.toggleUnreadOnly(); break;
    case "a":
      if (e.shiftKey) { e.preventDefault(); markAllRead(); }
      else { e.preventDefault(); hub.value = true; }
      break;
    case "d": if (e.shiftKey) { e.preventDefault(); ui.setTheme(ui.theme === "light" ? "dark" : "light"); } break;
    // Gmail-style: `/` reaches the command palette without a modifier.
    case "/": e.preventDefault(); cpOpen.value = true; break;
    // `?` opens the AI assistant in Ask mode — a natural pair with `/`.
    case "?": e.preventDefault(); aiAssistant.mode = "ask"; aiAssistant.open = true; break;
    // ← / → flip between articles inside the Reader overlay. Gated on
    // middlePane === "articles" because entering the reader from the
    // Highlights panel has no natural "next article" semantics.
    case "arrowleft":
    case "[":
      if (ui.selectedArticleId != null && ui.middlePane === "articles") {
        e.preventDefault();
        readerNav(-1);
      }
      break;
    case "arrowright":
    case "]":
      if (ui.selectedArticleId != null && ui.middlePane === "articles") {
        e.preventDefault();
        readerNav(1);
      }
      break;
    // Esc: close the Reader overlay first if it's open, else drop focus mode
    // and any AI drawer. Order matters — a user in fullscreen reading a
    // long article expects Esc to close the article, not exit fullscreen.
    case "escape":
      if (ui.selectedArticleId != null) closeReader();
      else { ui.setFocusMode(false); ui.setAiOpen(false); }
      break;
  }
}

// ── lifecycle: events, startup, update check ──
const offs: Array<() => void> = [];
onMounted(() => {
  applyStartupView();

  offs.push(Events.On("feeds-updated", () => {
    qc.invalidateQueries({ queryKey: ["feeds"] });
    qc.invalidateQueries({ queryKey: ["counts"] });
    qc.invalidateQueries({ queryKey: ["smartCounts"] });
    qc.invalidateQueries({ queryKey: ["articles"] });
  }));
  offs.push(Events.On("tray-open-settings", () => { settings.open = true; }));
  offs.push(Events.On("deep-link-subscribe", (e: { data: unknown }) => {
    hubUrl.value = String((e as any)?.data ?? "");
    hub.value = true;
  }));
  // The backend swapped in a freshly downloaded feed directory — re-render
  // any open hub with it.
  offs.push(Events.On("directory-updated", () => {
    qc.invalidateQueries({ queryKey: ["directory"] });
  }));

  api.takePendingDeepLink().then((url) => {
    if (url) { hubUrl.value = url; hub.value = true; }
  }).catch(() => {});

  window.setTimeout(() => void checkForUpdates(true), 4000);
  window.addEventListener("keydown", onKey);
});

onBeforeUnmount(() => {
  window.removeEventListener("keydown", onKey);
  for (const off of offs) { try { off(); } catch { /* ignore */ } }
});

function createFolder(name: string) {
  api.createFolder(name)
    .then(() => { qc.invalidateQueries({ queryKey: ["folders"] }); showToast(t("app.folderCreated")); })
    .catch(reportError);
}
</script>

<template>
  <div class="app-shell">
    <div class="window" :class="{ focus: ui.focusMode }">
      <Sidebar
        :refreshing="refreshing"
        @add-feed="hub = true"
        @explore="hub = true"
        @open-settings="openSettings"
        @edit-feed="editFeed"
        @new-folder="newFolder = true"
        @search-click="cpOpen = true"
        @refresh="doRefresh"
        @select-highlights="ui.showHighlights()"
        @ai-assistant="aiAssistant.mode = 'chat'; aiAssistant.open = true"
      />
      <HighlightsPane
        v-if="ui.middlePane === 'highlights'"
        @open-highlight="onOpenHighlight"
      />
      <ArticleList v-else @toast="(s: string) => showToast(s)" />
      <!-- Sidebar resize handle at the rail+panel | list boundary. Hidden in
           focus mode (the sidebar + list collapse to the reader). -->
      <div
        v-if="!ui.focusMode"
        class="resize-handle-slot"
        :style="{ left: 'calc(var(--col-rail) + var(--col-sidebar))' }"
      >
        <ResizeHandle
          :width="ui.sidebarWidth"
          side="right"
          :min="PANEL_BOUNDS.sidebar.min"
          :max="PANEL_BOUNDS.sidebar.max"
          :label="t('app.resizeSidebar')"
          @resize="(w: number) => ui.setPanel({ sidebarWidth: w })"
        />
      </div>
    </div>
    <PlayerBar />
  </div>

  <!-- Full-screen reading overlay (replaces the reader pane). ‹ › / ← →
       flip between articles in the current list; Esc / ✕ closes. -->
  <div
    v-if="ui.selectedArticleId != null"
    class="reader-overlay"
    :class="{ 'reader-focus': ui.focusMode, 'has-player': !!player.track }"
  >
    <Reader @toast="(s: string) => showToast(s)" @open-settings="(s?: string) => openSettings(s)" />
    <button
      class="reader-nav prev"
      :disabled="!hasPrev"
      @click="readerNav(-1)"
      :title="t('reader.emptyHintPrefix') + ' ← ' + t('reader.emptyHintSuffix')"
      :aria-label="t('reader.emptyHintPrefix') + ' ← ' + t('reader.emptyHintSuffix')"
    >
      <Icon name="chevron-down" :size="22" />
    </button>
    <button
      class="reader-nav next"
      :disabled="!hasNext"
      @click="readerNav(1)"
      :title="t('reader.emptyHintPrefix') + ' → ' + t('reader.emptyHintSuffix')"
      :aria-label="t('reader.emptyHintPrefix') + ' → ' + t('reader.emptyHintSuffix')"
    >
      <Icon name="chevron-down" :size="22" />
    </button>
    <button
      class="reader-overlay-close"
      @click="closeReader"
      :title="t('common.close')"
      :aria-label="t('common.close')"
    >
      <Icon name="x" :size="16" />
    </button>
  </div>

  <CommandPalette
    :open="cpOpen"
    @close="cpOpen = false"
    @action="handleCommand"
    @navigate-feed="navigateFeed"
    @navigate-article="navigateArticle"
  />

  <SettingsDialog
    v-if="settings.open"
    :initial-section="settings.section"
    @close="settings.open = false"
    @add-feed="settings.open = false; hub = true"
  />

  <FeedEditDialog
    v-if="feedEditId != null"
    :feed-id="feedEditId"
    @close="feedEditId = null"
  />

  <SourceHub
    v-if="hub"
    :initial-url="hubUrl"
    @close="hub = false; hubUrl = undefined"
  />

  <AIAssistant
    v-if="aiAssistant.open"
    :initial-mode="aiAssistant.mode"
    @close="aiAssistant.open = false"
    @open-settings="(s?: string) => { aiAssistant.open = false; openSettings(s); }"
  />

  <PromptDialog
    v-if="newFolder"
    :title="t('app.newFolderTitle')"
    :placeholder="t('app.folderNamePlaceholder')"
    @submit="(v: string) => { createFolder(v); newFolder = false; }"
    @cancel="newFolder = false"
  />

  <div role="status" aria-live="polite">
    <div
      v-if="toasts.current"
      :key="toasts.current.id"
      class="toast"
      :class="{ 'toast-error': toasts.current.tone === 'error' }"
    >
      <span v-if="toasts.current.tone === 'error'" class="toast-ico" aria-hidden="true">
        <Icon name="alert" :size="14" />
      </span>
      <span class="toast-text">{{ toasts.current.text }}</span>
      <kbd v-if="toasts.current.kbd" aria-hidden="true">{{ toasts.current.kbd }}</kbd>
      <button
        v-if="toasts.current.action"
        class="toast-action"
        @click="toasts.current.action.run(); toasts.dismiss(toasts.current.id)"
      >
        {{ toasts.current.action.label }}
      </button>
      <button
        v-if="toasts.current.tone === 'error' || toasts.current.action"
        class="toast-dismiss"
        :aria-label="t('common.close')"
        @click="toasts.dismiss(toasts.current.id)"
      >
        <Icon name="x" :size="13" />
      </button>
    </div>
  </div>
</template>
