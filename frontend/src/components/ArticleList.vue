<script setup lang="ts">
// The middle pane: a virtualized, paginated/infinite article browse with
// card/list view modes, right-click context menu, delayed hover preview and
// keyboard navigation. Ported 1:1 from papr's React ArticleList.tsx — same CSS
// class names and behavior, React hooks mapped to the Vue equivalents.
//
//   useInfiniteQuery (@tanstack/vue-query) drives pagination; its queryKey is
//   ["articles", query, unreadOnly, sortOldest] — the SAME shape papr uses, and
//   the one articleActions/currentList read & patch. Keep these in lockstep.
//
//   @tanstack/vue-virtual's useVirtualizer takes a *reactive* options object
//   (we pass a computed, so `count`/`estimateSize` track state) and returns a
//   Ref<Virtualizer> — hence the `.value` access throughout.

import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/vue-query";
import { useVirtualizer } from "@tanstack/vue-virtual";
import { useI18n } from "vue-i18n";
import { Browser } from "@wailsio/runtime";
import * as api from "../api";
import { useUi } from "../stores/ui";
import { useArticleActions } from "../composables/articleActions";
import { relTime } from "../lib/feedMeta";
import { isMac, modCombo } from "../lib/platform";
import { reportError, toast } from "../stores/toasts";
import { clampToViewport } from "../lib/viewport";
import { listTranslationKey, useListTranslation } from "../stores/listTranslation";
import type { ArticleSummary, Feed } from "../types";
import Icon from "./Icon.vue";
import FeedAvatar from "./FeedAvatar.vue";
import ContextMenu, { type MenuEntry } from "./ContextMenu.vue";

const PAGE = 60;

interface Hover {
  article: ArticleSummary;
  top: number;
  left: number;
}

const emit = defineEmits<{ (e: "toast", msg: string): void }>();

const { t } = useI18n();
const qc = useQueryClient();
const ui = useUi();
const actions = useArticleActions(toast.error);

const feeds = useQuery({ queryKey: ["feeds"], queryFn: api.listFeeds });
// Shares the sidebar's ["smartCounts"] cache — used to show the true unread
// total in the header instead of just how many rows have loaded.
const counts = useQuery({ queryKey: ["smartCounts"], queryFn: api.smartCounts });
const feedById = computed(() => {
  const m: Record<number, Feed> = {};
  for (const f of feeds.data.value ?? []) m[f.id] = f;
  return m;
});

// ── list preview translation (viewport-driven) ──────────────────────────────
// Off/auto lives in the DB settings (like the reader's translate prefs), so
// the choice follows the library rather than the browser profile.
const listTransSetting = useQuery({
  queryKey: ["setting", "list_translate_mode"],
  queryFn: () => api.getSetting("list_translate_mode"),
});
const targetLangSetting = useQuery({
  queryKey: ["setting", "translate_target_lang"],
  queryFn: () => api.getSetting("translate_target_lang"),
});
const engineSetting = useQuery({
  queryKey: ["setting", "translate_engine"],
  queryFn: () => api.getSetting("translate_engine"),
});
const { locale } = useI18n();
const listTranslate = computed(() => listTransSetting.data.value === "auto");
const targetLang = computed(() => targetLangSetting.data.value || locale.value);
const translateEngine = computed(() => engineSetting.data.value || "llm");
const translationSettingsReady = computed(
  () => targetLangSetting.isFetched.value && engineSetting.isFetched.value,
);
const listJobs = useListTranslation();

function toggleListTranslate(): void {
  const mode = listTranslate.value ? "off" : "auto";
  api
    .setSetting("list_translate_mode", mode)
    .then(() => qc.setQueryData(["setting", "list_translate_mode"], mode))
    .catch((e) => reportError(e));
}

// The translated preview for a row, when list translation is on and done.
function rowJob(a: ArticleSummary) {
  if (!listTranslate.value) return undefined;
  return listJobs.jobs[listTranslationKey(a.id, targetLang.value, translateEngine.value)];
}
function rowTitle(a: ArticleSummary): string {
  const j = rowJob(a);
  return (j?.status === "done" && j.title) || a.title;
}
function rowSnippet(a: ArticleSummary): string | null {
  const j = rowJob(a);
  return (j?.status === "done" && j.snippet) || a.snippet;
}
function rowTranslating(a: ArticleSummary): boolean {
  const j = rowJob(a);
  return j?.status === "queued" || j?.status === "translating";
}

const menu = ref<{ x: number; y: number; article: ArticleSummary } | null>(null);
const hover = ref<Hover | null>(null);
let hoverTimer: number | undefined;

const browse = useInfiniteQuery({
  // MUST match articleActions' optimistic patch key (prefix ["articles"]) and
  // currentList's exact read key ["articles", query, unreadOnly, sortOldest].
  // vue-query unwraps these computeds, so the cached key is the resolved-value
  // tuple those consumers look up — keep this shape in lockstep with them.
  queryKey: [
    "articles",
    computed(() => ui.query),
    computed(() => ui.unreadOnly),
    computed(() => ui.sortOldest),
  ],
  initialPageParam: 0,
  queryFn: ({ pageParam }) =>
    api.listArticles(ui.query, ui.unreadOnly, null, ui.sortOldest, PAGE, pageParam as number),
  getNextPageParam: (last: ArticleSummary[], all: ArticleSummary[][]) =>
    last.length < PAGE ? undefined : all.length * PAGE,
});

const items = computed<ArticleSummary[]>(
  () => browse.data.value?.pages.flat() ?? [],
);

const scrollRef = ref<HTMLDivElement | null>(null);

// Card-grid metrics per information-density setting (Settings → Appearance):
// `min` sets the column-width threshold (→ lane count), `h` the card height,
// `gap` the row + column gutter, and `thumb`/`lines` shrink the hero image +
// snippet so the content still fits the shorter cards. The stylesheet reads
// h/gap/thumb/lines as the --card-* variables set on the scroll container
// (cardVars below); JS derives the column count (min) and the virtualizer lane
// step (h + gap) from the SAME map, so the grid and CSS never drift.
const CARD_DENSITY = {
  compact: { min: 260, h: 250, gap: 16, thumb: 106, lines: 2, noThumbLines: 7 },
  cozy: { min: 300, h: 284, gap: 20, thumb: 120, lines: 3, noThumbLines: 9 },
  spacious: { min: 340, h: 340, gap: 24, thumb: 156, lines: 4, noThumbLines: 12 },
} as const;
const cardMetric = computed(
  () => CARD_DENSITY[ui.density as keyof typeof CARD_DENSITY] ?? CARD_DENSITY.cozy,
);

const rowEstimate = computed(() =>
  ui.viewMode === "card"
    ? cardMetric.value.h + cardMetric.value.gap
    : ui.density === "compact"
      ? 78
      : ui.density === "spacious"
        ? 122
        : 98,
);

// Card view is a responsive multi-column grid: the virtualizer's `lanes` give us
// one lane per column (uniform card height, so the lanes lay out as clean rows).
// List view stays single-column (lanes = 1, unchanged behaviour).
const cols = ref(1);
function recomputeCols() {
  if (ui.viewMode !== "card") {
    cols.value = 1;
    return;
  }
  const w = scrollRef.value?.clientWidth ?? 0;
  cols.value = Math.max(1, Math.min(5, Math.floor(w / cardMetric.value.min)));
}

// CSS custom properties the card stylesheet reads (card height, gutter, hero
// height, snippet line-clamp) — set on the scroll container only in card mode,
// mirroring cardMetric so the density setting drives the visual card too.
const cardVars = computed((): Record<string, string> => {
  if (ui.viewMode !== "card") return {};
  const m = cardMetric.value;
  return {
    "--card-h": `${m.h}px`,
    "--card-gap": `${m.gap}px`,
    "--card-thumb-h": `${m.thumb}px`,
    "--card-snippet-lines": String(m.lines),
    "--card-snippet-lines-nothumb": String(m.noThumbLines),
  };
});

const virtualizer = useVirtualizer(
  computed(() => ({
    count: items.value.length,
    getScrollElement: () => scrollRef.value,
    estimateSize: () => rowEstimate.value,
    overscan: 8,
    lanes: cols.value,
    // Card grid: breathing room between the list header and the first row
    // (the virtualizer offsets every row and grows totalSize to match, so the
    // scroll math stays correct). List view needs no top inset.
    paddingStart: ui.viewMode === "card" ? cardMetric.value.gap : 0,
  })),
);

const vItems = computed(() => virtualizer.value.getVirtualItems());

// Load the next page as the end approaches. Keyed on the last visible index
// (a primitive) rather than getVirtualItems() — which returns a fresh array
// every render and would re-run this unconditionally.
watch(
  () => [
    virtualizer.value.range?.endIndex,
    items.value.length,
    browse.hasNextPage.value,
    browse.isFetchingNextPage.value,
  ],
  () => {
    const last = virtualizer.value.getVirtualItems().at(-1);
    if (
      last &&
      last.index >= items.value.length - 6 &&
      browse.hasNextPage.value &&
      !browse.isFetchingNextPage.value
    ) {
      browse.fetchNextPage();
    }
  },
);

// Queue preview translations for the visible rows (first screenful capped at
// 16 per pass, like papr) whenever the range, data, or translate prefs move.
watch(
  () => [
    listTranslate.value,
    translationSettingsReady.value,
    virtualizer.value.range?.startIndex,
    virtualizer.value.range?.endIndex,
    items.value,
    targetLang.value,
    translateEngine.value,
  ],
  () => {
    if (!listTranslate.value || !translationSettingsReady.value) return;
    const visible = vItems.value
      .filter((vi) => vi.index >= 0 && vi.index < items.value.length)
      .slice(0, 16)
      .map((vi) => items.value[vi.index])
      .filter(Boolean);
    listJobs.enqueueVisible(visible, targetLang.value, translateEngine.value);
  },
);

// Keep the keyboard-selected article visible.
watch(
  () => ui.selectedArticleId,
  (selectedId) => {
    if (selectedId == null) return;
    const i = items.value.findIndex((a) => a.id === selectedId);
    if (i >= 0) virtualizer.value.scrollToIndex(i, { align: "auto" });
  },
);

onBeforeUnmount(() => window.clearTimeout(hoverTimer));

// Recompute the card-grid column count on mount, container resize, and when the
// list/card view mode flips.
let colsObserver: ResizeObserver | undefined;
onMounted(() => {
  recomputeCols();
  colsObserver = new ResizeObserver(recomputeCols);
  if (scrollRef.value) {
    colsObserver.observe(scrollRef.value);
    // Dismiss the hover preview on scroll: a trackpad / wheel scroll recycles
    // the hovered row without firing mouseleave, so a pending or shown preview
    // would otherwise linger over the now-recycled (unrelated) row.
    scrollRef.value.addEventListener("scroll", leaveHover, { passive: true });
  }
});
onBeforeUnmount(() => {
  colsObserver?.disconnect();
  scrollRef.value?.removeEventListener("scroll", leaveHover);
});

// Re-flow the virtualizer after a layout change (view-mode flip or density
// change): both alter row height and lane count, so the cached per-row
// measurements must be dropped — otherwise stale list-row sizes stack the
// fixed-height cards and overlap them, and a narrow single-column card view
// won't trip the virtualizer's own lane-change reset. Afterwards, anchor the
// first visible row back to the top so the change doesn't fling the list to a
// random offset (the rows are now a different height).
function reflowGrid() {
  const top = virtualizer.value.range?.startIndex ?? 0;
  recomputeCols();
  nextTick(() => {
    virtualizer.value.measure();
    if (top > 0) {
      nextTick(() => virtualizer.value.scrollToIndex(top, { align: "start" }));
    }
  });
}
watch(() => ui.viewMode, reflowGrid);
watch(() => ui.density, reflowGrid);

// Dismiss any hover preview (shown or still pending) when the list contents
// change — switching feed/folder/tag, or toggling the unread / sort filters.
// The hovered row is unmounted by the re-render without firing `mouseleave`,
// so without this the preview lingers over the new list (or a pending timer
// fires later and measures a now-detached row, placing the preview at 0,0).
watch(
  () => [ui.query, ui.unreadOnly, ui.sortOldest],
  () => {
    window.clearTimeout(hoverTimer);
    hover.value = null;
  },
);

// Jump back to the top of the list whenever the sidebar selection changes.
// The scroll container stays mounted across the query swap, so without this
// a new feed/folder/tag opens scrolled to wherever the *previous* list was
// left — burying its newest articles below the fold. `scrollToOffset(0)` also
// resets the virtualizer's internal offset, keeping its rendered window in
// sync with the DOM scroll position.
watch(
  () => ui.query,
  () => virtualizer.value.scrollToOffset(0),
);

const markAll = async () => {
  try {
    const n = await api.markAllRead(ui.query);
    actions.refreshAfterBulk();
    emit(
      "toast",
      n > 0
        ? t("articleList.markedReadToast", { count: n })
        : t("articleList.nothingToMark"),
    );
  } catch (e) {
    reportError(e);
  }
};

const onHover = (a: ArticleSummary, e: MouseEvent) => {
  window.clearTimeout(hoverTimer);
  // Hold the row element, not a rect snapshot: the preview only appears 650ms
  // later, and the list is scrollable — measuring at hover time would anchor
  // the preview to where the row *was*, so a scroll during the delay (a common
  // "scroll, then pause on a row" gesture) leaves it floating over unrelated
  // rows or off-screen. Re-measure inside the timer instead, when the preview
  // actually opens, so it tracks the row's live position.
  const row = e.currentTarget as HTMLElement;
  hoverTimer = window.setTimeout(() => {
    const rect = row.getBoundingClientRect();
    hover.value = { article: a, top: rect.top + 4, left: rect.right + 12 };
  }, 650);
};
const leaveHover = () => {
  window.clearTimeout(hoverTimer);
  hover.value = null;
};

const openArticle = (id: number) => ui.openArticle(id);

// ⌘/Ctrl+click (and middle-click, via auxclick) opens the original in the
// system browser without touching the in-app selection — the "peek at the
// source, keep my place" staple.
function onRowClick(a: ArticleSummary, e: MouseEvent): void {
  if ((e.metaKey || e.ctrlKey) && a.url) {
    Browser.OpenURL(a.url).catch(() => {});
    return;
  }
  openArticle(a.id);
}
function onRowAux(a: ArticleSummary, e: MouseEvent): void {
  if (e.button === 1 && a.url) {
    e.preventDefault();
    Browser.OpenURL(a.url).catch(() => {});
  }
}
const openMenu = (a: ArticleSummary, e: MouseEvent) => {
  e.preventDefault();
  menu.value = { x: e.clientX, y: e.clientY, article: a };
};

const articleMenu = (a: ArticleSummary): MenuEntry[] => [
  { icon: "open", label: t("articleList.menuOpen"), shortcut: "⏎", onClick: () => ui.openArticle(a.id) },
  ...(a.url
    ? ([
        {
          icon: "globe",
          label: t("articleList.menuOpenInBrowser"),
          shortcut: modCombo("O"),
          onClick: () => Browser.OpenURL(a.url!).catch(() => {}),
        },
      ] as MenuEntry[])
    : []),
  { separator: true },
  {
    icon: a.isStarred ? "star-fill" : "star",
    label: a.isStarred ? t("articleList.menuUnstar") : t("articleList.menuStar"),
    shortcut: "S",
    onClick: () => actions.setStarred(a.id, !a.isStarred),
  },
  {
    icon: a.readLater ? "bookmark-fill" : "bookmark",
    label: a.readLater
      ? t("articleList.menuRemoveReadLater")
      : t("articleList.menuAddReadLater"),
    shortcut: "B",
    onClick: () => actions.setReadLater(a.id, !a.readLater),
  },
  {
    icon: a.isRead ? "circle" : "check",
    label: a.isRead ? t("articleList.menuMarkUnread") : t("articleList.menuMarkRead"),
    shortcut: "U",
    onClick: () => actions.setRead(a.id, !a.isRead),
  },
  ...(a.url
    ? ([
        { separator: true },
        {
          icon: "copy",
          label: t("articleList.menuCopyLink"),
          onClick: () =>
            navigator.clipboard
              .writeText(a.url!)
              .then(() => emit("toast", t("articleList.linkCopied")), () => {}),
        },
      ] as MenuEntry[])
    : []),
];

const showCount = computed(() => {
  // The unread smart view shows every unread article, so surface the true total
  // (the sidebar's count) rather than just the loaded "60+". Other views keep
  // the loaded count — their true totals depend on the active filter.
  if (ui.query.kind === "unread" && counts.data.value) {
    return t("articleList.countArticles", { count: counts.data.value.unread, suffix: "" });
  }
  return t("articleList.countArticles", {
    count: items.value.length,
    suffix: browse.hasNextPage.value ? "+" : "",
  });
});

const headerTitle = computed(() =>
  ui.query.kind === "feed" || ui.query.kind === "folder" || ui.query.kind === "tag"
    ? ui.queryLabel
    : t(`smart.${ui.query.kind}`),
);

// Arrow-key navigation for the listbox (in addition to the global j/k).
const onListKeyDown = (e: KeyboardEvent) => {
  if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(e.key)) return;
  if (items.value.length === 0) return;
  e.preventDefault();
  const cur = items.value.findIndex((x) => x.id === ui.selectedArticleId);
  const next =
    e.key === "Home"
      ? 0
      : e.key === "End"
        ? items.value.length - 1
        : e.key === "ArrowDown"
          ? Math.min(items.value.length - 1, cur < 0 ? 0 : cur + 1)
          : Math.max(0, cur < 0 ? 0 : cur - 1);
  ui.openArticle(items.value[next].id);
};

// ── Card-view thumbnail state. The virtualizer recycles row DOM, so the broken
// flag is keyed per article id and reset when the id (URL) changes. ──
const brokenThumbs = ref<Set<number>>(new Set());
const thumbVisible = (a: ArticleSummary) => !!a.imageUrl && !brokenThumbs.value.has(a.id);
// True when this article renders a card hero image — gates whether the unread
// dot / star live as image-corner badges (thumb present) or inline in the head.
const cardThumb = (a: ArticleSummary) =>
  ui.viewMode === "card" && ui.prefs.showCardThumbs && thumbVisible(a);
const onThumbError = (a: ArticleSummary) => {
  const s = new Set(brokenThumbs.value);
  s.add(a.id);
  brokenThumbs.value = s;
};

// ── Hover preview placement. The card is a fixed 340px wide; the 192px height
// pairs with the 8px margin to keep the historical `innerHeight - 200` pull-
// back. The shared helper bounds both edges so a narrow/short window can't
// shove the preview off the top-left corner. ──
const hoverPos = computed(() => {
  if (!hover.value) return { left: 0, top: 0 };
  return clampToViewport({
    x: hover.value.left,
    y: hover.value.top,
    width: 340,
    height: 192,
    margin: 8,
  });
});
const hoverMeta = computed(() => {
  const a = hover.value?.article;
  if (!a) return "";
  return [a.author, relTime(a.publishedAt)].filter(Boolean).join(" · ");
});
</script>

<template>
  <div
    class="list"
    :class="{ 'list-cards': ui.viewMode === 'card' }"
    role="region"
    aria-labelledby="article-list-title"
  >
    <div class="list-header" :data-tauri-drag-region="isMac ? true : undefined">
      <h1 class="list-title" id="article-list-title">
        <!-- Smart views re-translate live; feed/folder/tag keep their own title. -->
        {{ headerTitle }}
        <span class="count">{{ browse.isLoading.value ? t("common.loading") : showCount }}</span>
      </h1>
      <div class="list-meta">
        <button
          :class="`list-meta-btn ${!ui.sortOldest ? 'on' : ''}`"
          @click="ui.toggleSort()"
          :title="t('articleList.sort')"
        >
          <Icon :name="ui.sortOldest ? 'arrow-up' : 'arrow-down'" :size="12" />
          {{ ui.sortOldest ? t("articleList.oldestFirst") : t("articleList.newestFirst") }}
        </button>
        <button
          :class="`list-meta-btn ${ui.unreadOnly ? 'on' : ''}`"
          @click="ui.toggleUnreadOnly()"
          :title="t('articleList.hideRead')"
        >
          <Icon :name="ui.unreadOnly ? 'eye-off' : 'eye'" :size="12" />
          {{ ui.unreadOnly ? t("articleList.unreadOnly") : t("smart.all") }}
        </button>
        <button
          :class="`list-meta-btn ${listTranslate ? 'on' : ''}`"
          @click="toggleListTranslate()"
          :title="t('articleList.listTranslate')"
          :aria-pressed="listTranslate"
        >
          <Icon name="translate" :size="12" />
          {{ listTranslate ? t("articleList.listTranslateOn") : t("articleList.listTranslateOff") }}
        </button>
        <div style="flex: 1" />
        <button class="list-meta-btn" @click="markAll" :title="t('articleList.markAllRead')">
          <Icon name="check-all" :size="12" />
          {{ t("articleList.markRead") }}
        </button>
      </div>
    </div>

    <div class="list-scroll" ref="scrollRef" :style="cardVars">
      <div v-if="browse.isLoading.value">
        <div class="sk-art" v-for="i in 7" :key="i">
          <div class="sk-line" style="width: 40%" />
          <div class="sk-line" style="width: 92%; height: 12px" />
          <div class="sk-line" style="width: 70%" />
        </div>
      </div>

      <!-- A failed fetch must not masquerade as "all caught up". -->
      <div
        v-if="!browse.isLoading.value && browse.isError.value && items.length === 0"
        class="empty"
        style="height: 240px"
      >
        <div class="glyph"><Icon name="alert" :size="22" /></div>
        <div>{{ t("articleList.loadError") }}</div>
        <button
          class="empty-retry"
          @click="browse.refetch()"
          :disabled="browse.isFetching.value"
        >
          <Icon name="refresh" :size="12" />
          {{ t("common.retry") }}
        </button>
      </div>

      <div
        v-if="!browse.isLoading.value && !browse.isError.value && items.length === 0"
        class="empty"
        style="height: 240px"
      >
        <div class="glyph"><Icon name="check" :size="22" /></div>
        <div>{{ t("articleList.emptyState") }}</div>
        <!-- A self-inflicted dead end: the unread-only filter emptied the
             list. Hand over the way out instead of a bare "all done". -->
        <button
          v-if="ui.unreadOnly"
          class="empty-retry"
          @click="ui.toggleUnreadOnly()"
        >
          <Icon name="eye" :size="12" />
          {{ t("articleList.showAllCta") }}
        </button>
      </div>

      <div
        v-if="!browse.isLoading.value && items.length > 0"
        role="listbox"
        :tabindex="0"
        aria-labelledby="article-list-title"
        :aria-activedescendant="
          ui.selectedArticleId != null ? `option-article-${ui.selectedArticleId}` : undefined
        "
        @keydown="onListKeyDown"
        :style="{
          height: `${virtualizer.getTotalSize()}px`,
          position: 'relative',
          width: '100%',
        }"
      >
        <!--
          Key by the virtual slot index, not the article id. The window of
          rendered rows is a fixed band that slides as you scroll, so keying by
          index keeps the same ~dozen DOM nodes mounted and just swaps content +
          transform. Keying by a.id remounts a node every time the window slides,
          restarting measureElement from its estimate each time. (It also collides
          when offset pagination returns the same article on two pages.)
        -->
        <div
          v-for="vi in vItems"
          :key="vi.index"
          :data-index="vi.index"
          :ref="(el) => { if (el && ui.viewMode !== 'card') virtualizer.measureElement(el as Element); }"
          :style="{
            position: 'absolute',
            top: 0,
            left: ui.viewMode === 'card' ? `${(vi.lane / cols) * 100}%` : 0,
            width: ui.viewMode === 'card' ? `${100 / cols}%` : '100%',
            height: ui.viewMode === 'card' ? `${cardMetric.h + cardMetric.gap}px` : undefined,
            padding: ui.viewMode === 'card'
              ? `0 ${cardMetric.gap / 2}px ${cardMetric.gap}px`
              : undefined,
            boxSizing: 'border-box',
            transform: `translateY(${vi.start}px)`,
          }"
        >
          <div
            :class="`art ${ui.viewMode === 'card' ? 'card' : ''} ${
              ui.selectedArticleId === items[vi.index].id ? 'active' : ''
            } ${items[vi.index].isRead ? 'read' : ''}`"
            role="option"
            :id="`option-article-${items[vi.index].id}`"
            :aria-selected="ui.selectedArticleId === items[vi.index].id"
            @click="onRowClick(items[vi.index], $event)"
            @auxclick="onRowAux(items[vi.index], $event)"
            @contextmenu="openMenu(items[vi.index], $event)"
            @mouseenter="ui.viewMode !== 'card' && onHover(items[vi.index], $event)"
            @mouseleave="leaveHover"
          >
            <div v-if="cardThumb(items[vi.index])" class="art-thumb">
              <img
                :src="items[vi.index].imageUrl!"
                alt=""
                loading="lazy"
                @error="onThumbError(items[vi.index])"
                style="position: absolute; inset: 0; width: 100%; height: 100%; object-fit: cover"
              />
              <span v-if="!items[vi.index].isRead" class="art-thumb-dot" aria-hidden="true" />
              <span
                v-if="items[vi.index].isStarred || items[vi.index].readLater"
                class="art-thumb-chip"
                aria-hidden="true"
              >
                <Icon
                  :name="items[vi.index].isStarred ? 'star-fill' : 'bookmark-fill'"
                  :size="11"
                />
              </span>
            </div>
            <div class="art-head">
              <FeedAvatar
                v-if="ui.viewMode === 'card'"
                class="art-avatar"
                :feed="feedById[items[vi.index].feedId]"
                :title="items[vi.index].feedTitle"
                :size="18"
              />
              <span
                v-if="!items[vi.index].isRead && !cardThumb(items[vi.index])"
                class="art-dot"
              />
              <span class="art-feed">{{ items[vi.index].feedTitle }}</span>
              <span
                v-if="feedById[items[vi.index].feedId] && feedById[items[vi.index].feedId].sourceType !== 'rss'"
                class="src-badge"
                >{{ feedById[items[vi.index].feedId].sourceType }}</span
              >
              <span class="art-sep">·</span>
              <span class="art-time">{{ relTime(items[vi.index].publishedAt) }}</span>
              <span
                v-if="items[vi.index].isStarred && !cardThumb(items[vi.index])"
                class="art-star"
              >
                <Icon name="star-fill" :size="12" />
              </span>
              <span
                v-if="items[vi.index].readLater && !items[vi.index].isStarred && !cardThumb(items[vi.index])"
                class="art-star"
              >
                <Icon name="bookmark-fill" :size="12" />
              </span>
            </div>
            <h3 class="art-title" :class="{ 'art-translating': rowTranslating(items[vi.index]) }">
              {{ rowTitle(items[vi.index]) }}
            </h3>
            <p v-if="rowSnippet(items[vi.index])" class="art-snippet">{{ rowSnippet(items[vi.index]) }}</p>
          </div>
        </div>
      </div>
      <div style="height: 60px" />
    </div>

    <div
      v-if="hover"
      class="hover-preview"
      :style="{ top: `${hoverPos.top}px`, left: `${hoverPos.left}px` }"
    >
      <div class="hp-feed">{{ hover.article.feedTitle }}</div>
      <div class="hp-title">{{ rowTitle(hover.article) }}</div>
      <div v-if="rowSnippet(hover.article)" class="hp-body">{{ rowSnippet(hover.article) }}</div>
      <div class="hp-meta">{{ hoverMeta }}</div>
    </div>

    <ContextMenu
      v-if="menu"
      :x="menu.x"
      :y="menu.y"
      :items="articleMenu(menu.article)"
      :on-close="() => (menu = null)"
    />
  </div>
</template>
