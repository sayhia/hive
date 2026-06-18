<script setup lang="ts">
// Left navigation pane. Vue port of papr's React Sidebar.tsx — same CSS class
// names (.sidebar, .sb-item, .sb-folder, .sb-count, .ctx-menu via ContextMenu…)
// and the same behaviour: smart views with live counts, expandable folders,
// feeds with favicon + unread badge, tags, per-row context menus, drag-to-move
// feeds between folders, drag-to-reorder tags, and Undo for destructive deletes.
//
// Differences from papr that are intentional in this codebase:
//   • There is no `useArticleActions` hook here, so the cache invalidation that
//     papr's `refreshAfterBulk` performed is inlined as `refreshAfterBulk()`.
//   • Smart counts live under the ["smartCounts"] query key (papr used
//     ["counts"]); both the read and the invalidations use ["smartCounts"].
//   • Folder creation is delegated to the parent via the `new-folder` emit
//     (the parent owns that dialog); feed/folder/tag *rename* and tag *create*
//     still use the inline PromptDialog, as in papr.
//   • `isMac` / `modCombo` are inlined (no lib/platform.ts in this project).

import { computed, onMounted, ref, watch } from "vue";
import { useQuery, useQueryClient } from "@tanstack/vue-query";
import { useI18n } from "vue-i18n";
import * as api from "../api";
import { useUi } from "../stores/ui";
import { withUndo, reportError, toast } from "../stores/toasts";
import {
  pendingFeedDeletes,
  pendingFolderDeletes,
  pendingTagDeletes,
} from "../lib/pendingDeletes";
import { tagColor, TAG_PALETTE } from "../lib/tagColors";
import type { ArticleQuery, Feed, Folder, Tag } from "../types";
import Icon from "./Icon.vue";
import ContextMenu, { type MenuEntry } from "./ContextMenu.vue";
import FeedAvatar from "./FeedAvatar.vue";
import PromptDialog from "./PromptDialog.vue";

// ── props / emits ──
// `refreshing` reflects the parent-owned "refresh all" run; the parent owns the
// dialogs the trigger buttons open and the global search overlay.
const props = defineProps<{
  refreshing: boolean;
}>();

const emit = defineEmits<{
  (e: "add-feed"): void;
  /** Opens the Add-feed dialog on its Explore tab. */
  (e: "explore"): void;
  (e: "open-settings", section?: string): void;
  /** Open Settings → Subscriptions with this feed's edit panel expanded. */
  (e: "edit-feed", feedId: number): void;
  (e: "new-folder"): void;
  (e: "search-click"): void;
  /** Opens the app-level AI assistant (Ask / Digest). */
  (e: "ai-assistant"): void;
  /** Refresh feeds. No scope refreshes everything (the toolbar button); pass
   *  `{ feedId }` or `{ folderId }` for the per-source context menus. */
  (e: "refresh", scope?: { feedId?: number; folderId?: number }): void;
  /** Switches the middle pane to the all-highlights browser. */
  (e: "select-highlights"): void;
}>();

const { t } = useI18n();
const qc = useQueryClient();
const ui = useUi();

// ── platform (inlined; papr keeps this in lib/platform.ts) ──
const isMac =
  typeof navigator !== "undefined" &&
  /Mac|iPhone|iPad|iPod/.test(navigator.platform || navigator.userAgent || "");
const modCombo = (key: string) => (isMac ? `⌘${key}` : `Ctrl+${key}`);

// ── server data ──
const feeds = useQuery({ queryKey: ["feeds"], queryFn: api.listFeeds });
const folders = useQuery({ queryKey: ["folders"], queryFn: api.listFolders });
const counts = useQuery({ queryKey: ["smartCounts"], queryFn: api.smartCounts });
const tags = useQuery({ queryKey: ["tags"], queryFn: api.listTags });

const allFeeds = computed<Feed[]>(() => feeds.data.value ?? []);
const allFolders = computed<Folder[]>(() => folders.data.value ?? []);
const allTags = computed<Tag[]>(() => tags.data.value ?? []);

const showCounts = computed(() => ui.prefs.showSidebarCounts);

const sameQuery = (a: ArticleQuery, b: ArticleQuery) =>
  JSON.stringify(a) === JSON.stringify(b);
const isActive = (q: ArticleQuery) => sameQuery(q, ui.query);
const isHighlightsPane = computed(() => ui.middlePane === "highlights");

// Enter / Space activator for the clickable rows — keyboard parity with click.
const onActivate = (fn: () => void) => (e: KeyboardEvent) => {
  if (e.key === "Enter" || e.key === " ") {
    e.preventDefault();
    fn();
  }
};

// ── collapsed-folders state (persisted) ──
function loadCollapsed(): Record<number, boolean> {
  try {
    return JSON.parse(localStorage.getItem("collapsedFolders") || "{}");
  } catch {
    return {};
  }
}
const collapsed = ref<Record<number, boolean>>(loadCollapsed());
watch(
  collapsed,
  (v) => localStorage.setItem("collapsedFolders", JSON.stringify(v)),
  { deep: true },
);
const toggleFolder = (id: number) => {
  collapsed.value = { ...collapsed.value, [id]: !collapsed.value[id] };
};

// ── context menu + prompt + drag state ──
type MenuBody =
  | { kind: "feed"; feed: Feed }
  | { kind: "folder"; folder: Folder }
  | { kind: "tag"; tag: Tag };
type Menu = MenuBody & { x: number; y: number };

interface PromptState {
  title: string;
  initial: string;
  placeholder: string;
  onSubmit: (v: string) => void;
}

const menu = ref<Menu | null>(null);
const prompt = ref<PromptState | null>(null);
const dragId = ref<number | null>(null);
const dropFolder = ref<number | "none" | null>(null);
const tagDragId = ref<number | null>(null);

// "Unread only" hides feeds with nothing unread, decluttering large sidebars.
// The selected feed is always kept (so it can't vanish under the user when its
// last article is marked read), and the filter is suspended mid-drag so a drag
// target never disappears.
const unreadOnly = computed(() => ui.prefs.sidebarUnreadOnly);
const feedVisible = (f: Feed) =>
  !unreadOnly.value ||
  dragId.value != null ||
  f.unreadCount > 0 ||
  isActive({ kind: "feed", value: f.id });
const visibleFeeds = computed(() => allFeeds.value.filter(feedVisible));
const ungrouped = computed(() => visibleFeeds.value.filter((f) => f.folderId == null));
const tagOverId = ref<number | null>(null);

// Drag-drop outline helpers (used by the feed/tag drop zones in the template).
// A dragleave fires spuriously when the cursor merely crosses onto a child
// element; only treat it as "left the zone" when the related target is outside.
function leftZone(e: DragEvent): boolean {
  const ct = e.currentTarget as HTMLElement | null;
  return !ct || !ct.contains(e.relatedTarget as Node | null);
}
// Flag a folder as a drop target — except the dragged feed's own current folder,
// where the drop is a no-op, so showing an accept outline would be misleading.
function onFolderOver(e: DragEvent, folderId: number): void {
  if (dragId.value == null) return;
  e.preventDefault();
  const feed = allFeeds.value.find((f) => f.id === dragId.value);
  dropFolder.value = feed && feed.folderId === folderId ? null : folderId;
}

// ── cache refresh after a feed/folder/tag mutation ──
// Inlined equivalent of papr's `actions.refreshAfterBulk()`: invalidate only
// the article-bearing caches, not AI summaries / settings / storage stats.
function refreshAfterBulk() {
  // ["smartCounts"] is this Sidebar's own read key; ["counts"] is invalidated
  // too because sibling components (App.vue, Add-feed / Explore dialogs) read
  // smart counts under that legacy key — keep both fresh.
  for (const key of [
    ["smartCounts"],
    ["counts"],
    ["feeds"],
    ["folders"],
    ["tags"],
    ["articles"],
    ["article"],
    ["search"],
    ["cp-search"],
  ]) {
    qc.invalidateQueries({ queryKey: key });
  }
}

const guard = (p: Promise<unknown>, ok: string) =>
  p
    .then(() => {
      refreshAfterBulk();
      toast.show(ok);
    })
    .catch((e) => reportError(e));

// Destructive deletes run behind a 6s Undo window: the row disappears at once
// (optimistic cache edit) but the irreversible backend call is deferred until
// the window closes. The id is parked in the matching pendingDeletes set —
// which api.listFeeds/listFolders/listTags filter — so a mid-window refetch
// (the scheduler's feeds-updated event, a mark-read, window focus) can't
// resurrect the row and flicker it back into the tree. Undo (and a failed
// delete) simply un-parks and refetches: the backend is untouched until
// commit, so the server is the snapshot — nothing stale to restore, nothing
// concurrent to clobber. The optimistic removal also resets a now-dangling
// selection pointing at the entity that just vanished.
function guardDelete(
  makeDelete: () => Promise<unknown>,
  toastText: string,
  kind: "feed" | "folder" | "tag",
  id: number,
) {
  const cacheKey =
    kind === "feed" ? "feeds" : kind === "folder" ? "folders" : "tags";
  const pending =
    kind === "feed"
      ? pendingFeedDeletes
      : kind === "folder"
        ? pendingFolderDeletes
        : pendingTagDeletes;
  // Un-park the id and refetch backend truth. After a successful commit the
  // row is gone server-side; after undo or a failed delete it comes back.
  const resync = () => {
    pending.delete(id);
    qc.invalidateQueries({ queryKey: [cacheKey] });
    if (kind === "folder") qc.invalidateQueries({ queryKey: ["feeds"] });
  };

  withUndo({
    text: toastText,
    apply: () => {
      pending.add(id);
      qc.setQueryData<{ id: number }[]>([cacheKey], (old) =>
        old?.filter((x) => x.id !== id),
      );
      // Deleting a folder orphans its feeds to "uncategorized" (DB FK is
      // ON DELETE SET NULL) — mirror that so they don't briefly vanish from
      // the tree during the undo window.
      if (kind === "folder") {
        qc.setQueryData<Feed[]>(["feeds"], (old) =>
          old?.map((f) => (f.folderId === id ? { ...f, folderId: null } : f)),
        );
      }
      // Reset a selection that just pointed at the deleted entity.
      const q = ui.query;
      if (q.kind === kind && (q as { value?: number }).value === id) {
        ui.select({ kind: "all" }, t("smart.all"));
      } else if (kind === "feed" && ui.selectedArticleId != null) {
        const open = qc.getQueryData<{ feedId: number }>([
          "article",
          ui.selectedArticleId,
        ]);
        if (open?.feedId === id) ui.openArticle(null);
      }
    },
    commit: () => {
      makeDelete()
        .then(() => refreshAfterBulk())
        .catch(reportError)
        .finally(resync);
    },
    revert: resync,
  });
}

// ── drag to move a feed between folders ──
function handleDrop(target: number | null) {
  const feed = allFeeds.value.find((f) => f.id === dragId.value);
  dragId.value = null;
  dropFolder.value = null;
  if (!feed || feed.folderId === target) return;
  const folderName =
    target == null
      ? t("sidebar.uncategorized")
      : allFolders.value.find((f) => f.id === target)?.name ?? "";
  guard(
    api.moveFeed(feed.id, target),
    t("sidebar.toastMoved", { feed: feed.title, folder: folderName }),
  );
}

// ── tag reorder (shared by drag-reorder and the menu's move up/down) ──
// Optimistically apply the new order, persist, then reconcile on either outcome.
function persistTagOrder(next: Tag[]) {
  qc.setQueryData(["tags"], next);
  api
    .reorderTags(next.map((tg) => tg.id))
    .catch((e) => reportError(e))
    .finally(() => qc.invalidateQueries({ queryKey: ["tags"] }));
}

function dropTag(targetId: number) {
  const list = allTags.value;
  const from = list.findIndex((tg) => tg.id === tagDragId.value);
  const to = list.findIndex((tg) => tg.id === targetId);
  tagDragId.value = null;
  tagOverId.value = null;
  if (from < 0 || to < 0 || from === to) return;
  const next = [...list];
  const [moved] = next.splice(from, 1);
  // `drop-above` marks an insertion point *before* the target. After removing
  // the dragged item, every index past `from` shifts down by one — so a
  // downward drag inserts at `to - 1` to land above the target, not below.
  const insertAt = from < to ? to - 1 : to;
  next.splice(insertAt, 0, moved);
  persistTagOrder(next);
}

// Keyboard-reachable counterpart to drag-reorder: swap with a neighbour.
function moveTag(tagId: number, dir: -1 | 1) {
  const list = allTags.value;
  const i = list.findIndex((tg) => tg.id === tagId);
  const j = i + dir;
  if (i < 0 || j < 0 || j >= list.length) return;
  const next = [...list];
  [next[i], next[j]] = [next[j], next[i]];
  persistTagOrder(next);
}

// ── context-menu item builders ──
// Items use ContextMenu.vue's `MenuEntry` shape: an action is
// { icon?, label, shortcut?, danger?, onClick }; { separator: true } draws a
// divider; a swatch row is { swatches: {value,color}[], current, onPick }. The
// menu invokes each item's own `onClick` / `onPick` and then calls `onClose`.
function feedMenu(f: Feed): MenuEntry[] {
  const moves: MenuEntry[] = allFolders.value
    .filter((fo) => fo.id !== f.folderId)
    .map((fo) => ({
      icon: "folder",
      label: t("sidebar.moveToFolder", { folder: fo.name }),
      onClick: () =>
        guard(
          api.moveFeed(f.id, fo.id),
          t("sidebar.toastMovedTo", { folder: fo.name }),
        ),
    }));
  if (f.folderId != null) {
    moves.push({
      icon: "folder",
      label: t("sidebar.moveOutOfFolder"),
      onClick: () => guard(api.moveFeed(f.id, null), t("sidebar.toastMovedOut")),
    });
  }
  // Mark-all-read leads: it's the more common action, so refresh sits below.
  return [
    {
      icon: "check-all",
      label: t("sidebar.markAllRead"),
      onClick: () =>
        guard(
          api.markAllRead({ kind: "feed", value: f.id }),
          t("sidebar.toastMarkedAllRead"),
        ),
    },
    {
      icon: "refresh",
      label: t("sidebar.refreshFeed"),
      onClick: () => emit("refresh", { feedId: f.id }),
    },
    { separator: true },
    {
      icon: "settings",
      label: t("sidebar.editMenu"),
      // Opens Settings → Subscriptions with this feed's panel expanded — name,
      // URL, folder, open mode, refresh interval and auto-translate in one
      // place, rather than the old rename-only prompt.
      onClick: () => emit("edit-feed", f.id),
    },
    ...(moves.length ? [{ separator: true } as MenuEntry, ...moves] : []),
    { separator: true },
    {
      icon: "trash",
      label: t("sidebar.unsubscribe"),
      danger: true,
      onClick: () =>
        guardDelete(
          () => api.deleteFeed(f.id),
          t("sidebar.toastUnsubscribed", { feed: f.title }),
          "feed",
          f.id,
        ),
    },
  ];
}

function folderMenu(folder: Folder): MenuEntry[] {
  // Same ordering rationale as feedMenu: mark-all-read first, refresh below.
  return [
    {
      icon: "check-all",
      label: t("sidebar.markAllRead"),
      onClick: () =>
        guard(
          api.markAllRead({ kind: "folder", value: folder.id }),
          t("sidebar.toastMarkedAllRead"),
        ),
    },
    {
      icon: "refresh",
      label: t("sidebar.refreshFolder"),
      onClick: () => emit("refresh", { folderId: folder.id }),
    },
    { separator: true },
    {
      icon: "settings",
      label: t("sidebar.renameMenu"),
      onClick: () =>
        (prompt.value = {
          title: t("sidebar.renameFolderTitle"),
          initial: folder.name,
          placeholder: t("sidebar.folderNamePlaceholder"),
          onSubmit: (v) =>
            guard(api.renameFolder(folder.id, v), t("sidebar.toastRenamed")),
        }),
    },
    { separator: true },
    {
      icon: "trash",
      label: t("sidebar.deleteFolder"),
      danger: true,
      onClick: () =>
        guardDelete(
          () => api.deleteFolder(folder.id),
          t("sidebar.toastFolderDeleted"),
          "folder",
          folder.id,
        ),
    },
  ];
}

function tagMenu(tag: Tag): MenuEntry[] {
  const list = allTags.value;
  const idx = list.findIndex((tg) => tg.id === tag.id);
  const items: MenuEntry[] = [];
  // Drag-reorder isn't reachable by keyboard — these give it parity.
  if (idx > 0) {
    items.push({
      icon: "arrow-up",
      label: t("sidebar.moveTagUp"),
      onClick: () => moveTag(tag.id, -1),
    });
  }
  if (idx >= 0 && idx < list.length - 1) {
    items.push({
      icon: "arrow-down",
      label: t("sidebar.moveTagDown"),
      onClick: () => moveTag(tag.id, 1),
    });
  }
  if (list.length > 1) items.push({ separator: true });
  items.push({
    icon: "settings",
    label: t("sidebar.renameMenu"),
    onClick: () =>
      (prompt.value = {
        title: t("sidebar.renameTagTitle"),
        initial: tag.name,
        placeholder: t("sidebar.tagNamePlaceholder"),
        onSubmit: (v) =>
          guard(api.renameTag(tag.id, v), t("sidebar.toastRenamed")),
      }),
  });
  items.push({
    swatches: Object.entries(TAG_PALETTE).map(([value, color]) => ({
      value,
      color,
    })),
    current: tag.color,
    // The recolour is instantly visible on the dot, so no toast — just refresh
    // the caches that embed the tag colour.
    onPick: (color) =>
      api
        .setTagColor(tag.id, color)
        .then(() => refreshAfterBulk())
        .catch((e) => reportError(e)),
  });
  items.push({ separator: true });
  items.push({
    icon: "trash",
    label: t("sidebar.deleteTag"),
    danger: true,
    onClick: () =>
      guardDelete(
        () => api.deleteTag(tag.id),
        t("sidebar.toastTagDeleted"),
        "tag",
        tag.id,
      ),
  });
  return items;
}

const menuItems = computed<MenuEntry[]>(() => {
  const m = menu.value;
  if (!m) return [];
  if (m.kind === "feed") return feedMenu(m.feed);
  if (m.kind === "folder") return folderMenu(m.folder);
  return tagMenu(m.tag);
});

// ── prompts: create tag (inline), create folder (delegated to parent) ──
const createTag = () =>
  (prompt.value = {
    title: t("sidebar.newTagTitle"),
    initial: "",
    placeholder: t("sidebar.tagNamePlaceholder"),
    onSubmit: (v) => guard(api.createTag(v), t("sidebar.toastTagCreated")),
  });

// ── per-feed unread aggregate for a collapsed folder ──
function folderUnread(folder: Folder): number {
  return allFeeds.value
    .filter((f) => f.folderId === folder.id)
    .reduce((n, f) => n + f.unreadCount, 0);
}
function feedsInFolder(folder: Folder): Feed[] {
  return visibleFeeds.value.filter((f) => f.folderId === folder.id);
}
// In "unread only" mode an empty folder is hidden — unless a drag is active,
// when it must stay as a drop target.
const visibleFolders = computed(() =>
  allFolders.value.filter(
    (folder) =>
      !unreadOnly.value || dragId.value != null || feedsInFolder(folder).length > 0,
  ),
);

// The ungrouped block doubles as the "move out of folder" drop zone. Rendered
// whenever there are ungrouped feeds, OR while dragging a feed that currently
// lives inside a folder (so a drag-to-ungroup has somewhere to land).
const showUngroupedZone = computed(
  () =>
    ungrouped.value.length > 0 ||
    (dragId.value != null &&
      allFeeds.value.find((f) => f.id === dragId.value)?.folderId != null),
);

// ── selection helpers ──
const selectQuery = (q: ArticleQuery, label: string) => ui.select(q, label);

// ── context-menu open / close ──
function openMenu(e: MouseEvent, m: MenuBody) {
  e.preventDefault();
  menu.value = { ...m, x: e.clientX, y: e.clientY };
}
const closeMenu = () => (menu.value = null);
// ContextMenu.vue takes `onClose` as a prop callback (not an emit) and invokes
// each item's own `onClick` / `onPick` itself, then calls `onClose` — so this
// component just drops the menu state there.

// ── prompt submit / close ──
// PromptDialog.vue already trims and only emits `submit` for a non-empty value,
// and emits `cancel` on dismiss.
function submitPrompt(v: string) {
  const p = prompt.value;
  prompt.value = null;
  if (p && v) p.onSubmit(v);
}
const closePrompt = () => (prompt.value = null);

onMounted(() => {
  // Keep the persisted collapsed map in sync on first mount (no-op if storage
  // already matched; mirrors papr's mount-time localStorage write).
  localStorage.setItem("collapsedFolders", JSON.stringify(collapsed.value));
});
</script>

<template>
  <div class="sidebar" role="navigation">
    <div v-if="isMac" class="titlebar" data-tauri-drag-region />

    <!-- Icon rail: brand mark, smart-view nav, and global actions -->
    <div class="rail">
      <div class="rail-brand">
        <img class="rail-brand-mark" src="/hive.svg" alt="Hive" />
      </div>

      <div class="rail-nav">
        <button
          class="rail-item"
          :class="{ active: ui.middlePane === 'articles' && isActive({ kind: 'all' }) }"
          :aria-current="(ui.middlePane === 'articles' && isActive({ kind: 'all' })) || undefined"
          :title="t('smart.all')"
          :aria-label="t('smart.all')"
          @click="selectQuery({ kind: 'all' }, t('smart.all'))"
        >
          <Icon name="inbox" :size="19" />
        </button>
        <button
          class="rail-item"
          :class="{ active: ui.middlePane === 'articles' && isActive({ kind: 'unread' }) }"
          :aria-current="(ui.middlePane === 'articles' && isActive({ kind: 'unread' })) || undefined"
          :title="t('smart.unread')"
          :aria-label="t('smart.unread')"
          @click="selectQuery({ kind: 'unread' }, t('smart.unread'))"
        >
          <Icon name="unread" :size="19" />
          <span
            v-if="showCounts && counts.data.value && counts.data.value.unread > 0"
            class="rail-badge"
            >{{ counts.data.value.unread > 99 ? "99+" : counts.data.value.unread }}</span
          >
        </button>
        <button
          class="rail-item"
          :class="{ active: ui.middlePane === 'articles' && isActive({ kind: 'starred' }) }"
          :aria-current="(ui.middlePane === 'articles' && isActive({ kind: 'starred' })) || undefined"
          :title="t('smart.starred')"
          :aria-label="t('smart.starred')"
          @click="selectQuery({ kind: 'starred' }, t('smart.starred'))"
        >
          <Icon name="star" :size="18" />
        </button>
        <button
          class="rail-item"
          :class="{ active: ui.middlePane === 'articles' && isActive({ kind: 'readLater' }) }"
          :aria-current="(ui.middlePane === 'articles' && isActive({ kind: 'readLater' })) || undefined"
          :title="t('smart.readLater')"
          :aria-label="t('smart.readLater')"
          @click="selectQuery({ kind: 'readLater' }, t('smart.readLater'))"
        >
          <Icon name="bookmark" :size="18" />
        </button>
        <button
          class="rail-item"
          :class="{ active: isHighlightsPane }"
          :aria-current="isHighlightsPane || undefined"
          :title="t('smart.highlights')"
          :aria-label="t('smart.highlights')"
          type="button"
          @click="emit('select-highlights')"
        >
          <Icon name="highlighter" :size="19" />
        </button>
      </div>

      <div class="rail-spacer" />

      <div class="rail-foot">
        <button
          class="rail-item"
          :title="t('sidebar.refreshAll')"
          :aria-label="t('sidebar.refreshAll')"
          :disabled="props.refreshing"
          :class="{ spinning: props.refreshing }"
          @click="emit('refresh')"
        >
          <Icon name="refresh" :size="18" />
        </button>
        <button
          class="rail-item"
          :title="t('sidebar.explore')"
          :aria-label="t('sidebar.explore')"
          @click="emit('explore')"
        >
          <Icon name="globe" :size="18" />
        </button>
        <button
          class="rail-item"
          :title="t('ai.title')"
          :aria-label="t('ai.title')"
          @click="emit('ai-assistant')"
        >
          <Icon name="sparkle" :size="18" />
        </button>
        <button
          class="rail-item"
          :title="t('sidebar.settings')"
          :aria-label="t('sidebar.settings')"
          @click="emit('open-settings')"
        >
          <Icon name="settings" :size="18" />
        </button>
      </div>
    </div>

    <!-- Feeds / tags panel -->
    <div class="sidebar-panel">
      <div class="sb-brand">
        <span class="sb-brand-name">hive</span>
      </div>

      <div
        class="sidebar-search"
        role="button"
        :tabindex="0"
        @click="emit('search-click')"
        @keydown="onActivate(() => emit('search-click'))($event)"
      >
        <Icon name="search" :size="13" />
        <span>{{ t("sidebar.searchArticles") }}</span>
        <kbd>{{ modCombo("K") }}</kbd>
      </div>

      <div class="sidebar-scroll">

      <!-- Feeds section header -->
      <div class="sb-section-title">
        <span>{{ t("sidebar.feeds") }}</span>
        <span class="sb-section-actions">
          <button
            :class="{ active: unreadOnly }"
            :title="t('sidebar.unreadOnly')"
            :aria-label="t('sidebar.unreadOnly')"
            :aria-pressed="unreadOnly"
            @click="ui.setPref({ sidebarUnreadOnly: !unreadOnly })"
          >
            <Icon :name="unreadOnly ? 'eye-off' : 'eye'" :size="12" />
          </button>
          <button
            :title="t('app.newFolderTitle')"
            :aria-label="t('app.newFolderTitle')"
            @click="emit('new-folder')"
          >
            <Icon name="folder" :size="12" />
          </button>
          <button
            :title="t('sidebar.addFeed')"
            :aria-label="t('sidebar.addFeed')"
            @click="emit('add-feed')"
          >
            <Icon name="plus" :size="12" />
          </button>
        </span>
      </div>

      <!-- New-library funnel: don't just describe the emptiness, hand over
           the way out — the Source Hub. -->
      <div v-if="allFeeds.length === 0" class="sb-empty">
        <p>{{ t("sidebar.emptyHint") }}</p>
        <button class="s-btn primary" @click="emit('explore')">
          <Icon name="globe" :size="12" />
          {{ t("sidebar.exploreCta") }}
        </button>
      </div>

      <div
        v-if="allFeeds.length > 0 && unreadOnly && visibleFeeds.length === 0"
        :style="{
          padding: '10px 12px',
          fontSize: '12px',
          color: 'var(--muted)',
          lineHeight: 1.5,
        }"
      >
        {{ t("sidebar.allReadHint") }}
      </div>

      <!-- Ungrouped feeds + drop zone for "move out of folder" -->
      <div
        v-if="showUngroupedZone"
        :class="{ 'sb-drop-active': dropFolder === 'none' }"
        @dragover="
          dragId != null && ($event.preventDefault(), (dropFolder = 'none'))
        "
        @dragleave="leftZone($event) && (dropFolder = null)"
        @drop="handleDrop(null)"
      >
        <template v-if="ungrouped.length > 0">
          <div
            v-for="f in ungrouped"
            :key="f.id"
            class="sb-item"
            :class="{
              active: isActive({ kind: 'feed', value: f.id }),
              dragging: dragId === f.id,
            }"
            role="button"
            :tabindex="0"
            :aria-current="isActive({ kind: 'feed', value: f.id }) || undefined"
            :draggable="true"
            :title="f.fetchError ?? f.title"
            @dragstart="dragId = f.id"
            @dragend="((dragId = null), (dropFolder = null))"
            @click="selectQuery({ kind: 'feed', value: f.id }, f.title)"
            @keydown="onActivate(() => selectQuery({ kind: 'feed', value: f.id }, f.title))($event)"
            @contextmenu="openMenu($event, { kind: 'feed', feed: f })"
          >
            <FeedAvatar :feed="f" />
            <span class="sb-label">{{ f.title }}</span>
            <span
              v-if="f.fetchError"
              class="sb-warn"
              role="img"
              :aria-label="t('sidebar.feedError')"
              >!</span
            >
            <span
              v-if="showCounts && f.unreadCount > 0"
              class="sb-count"
              >{{ f.unreadCount }}</span
            >
          </div>
        </template>
        <div v-else class="sb-drop-hint">{{ t("sidebar.dropUngroup") }}</div>
      </div>

      <!-- Folders -->
      <div
        v-for="folder in visibleFolders"
        :key="folder.id"
        :class="{ 'sb-drop-active': dropFolder === folder.id }"
        @dragover="onFolderOver($event, folder.id)"
        @dragleave="leftZone($event) && (dropFolder = null)"
        @drop="handleDrop(folder.id)"
      >
        <div
          class="sb-folder"
          :class="{
            collapsed: collapsed[folder.id],
            active: isActive({ kind: 'folder', value: folder.id }),
          }"
          role="button"
          :tabindex="0"
          :aria-current="isActive({ kind: 'folder', value: folder.id }) || undefined"
          @click="selectQuery({ kind: 'folder', value: folder.id }, folder.name)"
          @keydown="
            onActivate(() => selectQuery({ kind: 'folder', value: folder.id }, folder.name))($event)
          "
          @contextmenu="openMenu($event, { kind: 'folder', folder })"
        >
          <!-- The chevron is the expand/collapse affordance; clicking the folder
               name selects the folder's combined article view. -->
          <button
            type="button"
            class="sb-folder-toggle"
            :aria-label="t(collapsed[folder.id] ? 'sidebar.expandFolder' : 'sidebar.collapseFolder')"
            :aria-expanded="!collapsed[folder.id]"
            @click.stop="toggleFolder(folder.id)"
          >
            <Icon name="chevron-down" :size="11" />
          </button>
          <span class="sb-folder-name">{{ folder.name }}</span>
          <span
            v-if="
              showCounts &&
              (collapsed[folder.id] || isActive({ kind: 'folder', value: folder.id })) &&
              folderUnread(folder) > 0
            "
            class="sb-count"
            >{{ folderUnread(folder) }}</span
          >
        </div>

        <template v-if="!collapsed[folder.id]">
          <div
            v-for="f in feedsInFolder(folder)"
            :key="f.id"
            class="sb-item"
            :class="{
              active: isActive({ kind: 'feed', value: f.id }),
              dragging: dragId === f.id,
            }"
            role="button"
            :tabindex="0"
            :aria-current="isActive({ kind: 'feed', value: f.id }) || undefined"
            :draggable="true"
            :title="f.fetchError ?? f.title"
            @dragstart="dragId = f.id"
            @dragend="((dragId = null), (dropFolder = null))"
            @click="selectQuery({ kind: 'feed', value: f.id }, f.title)"
            @keydown="onActivate(() => selectQuery({ kind: 'feed', value: f.id }, f.title))($event)"
            @contextmenu="openMenu($event, { kind: 'feed', feed: f })"
          >
            <FeedAvatar :feed="f" />
            <span class="sb-label">{{ f.title }}</span>
            <span
              v-if="f.fetchError"
              class="sb-warn"
              role="img"
              :aria-label="t('sidebar.feedError')"
              >!</span
            >
            <span
              v-if="showCounts && f.unreadCount > 0"
              class="sb-count"
              >{{ f.unreadCount }}</span
            >
          </div>
        </template>
      </div>

      <!-- Tags section -->
      <div class="sb-section-title">
        <span>{{ t("sidebar.tags") }}</span>
        <button
          :title="t('sidebar.newTagTitle')"
          :aria-label="t('sidebar.newTagTitle')"
          @click="createTag"
        >
          <Icon name="plus" :size="12" />
        </button>
      </div>

      <div
        v-if="allTags.length === 0"
        :style="{
          padding: '4px 12px 2px',
          fontSize: '11.5px',
          color: 'var(--muted)',
          lineHeight: 1.5,
        }"
      >
        {{ t("sidebar.tagsEmptyHint") }}
      </div>

      <div
        v-for="tag in allTags"
        :key="tag.id"
        class="sb-item"
        :class="{
          active: isActive({ kind: 'tag', value: tag.id }),
          dragging: tagDragId === tag.id,
          'drop-above': tagOverId === tag.id,
        }"
        role="button"
        :tabindex="0"
        :aria-current="isActive({ kind: 'tag', value: tag.id }) || undefined"
        :draggable="true"
        @dragstart="tagDragId = tag.id"
        @dragend="((tagDragId = null), (tagOverId = null))"
        @dragover="
          tagDragId != null &&
            tagDragId !== tag.id &&
            ($event.preventDefault(), (tagOverId = tag.id))
        "
        @dragleave="leftZone($event) && (tagOverId = null)"
        @drop="dropTag(tag.id)"
        @click="selectQuery({ kind: 'tag', value: tag.id }, tag.name)"
        @keydown="onActivate(() => selectQuery({ kind: 'tag', value: tag.id }, tag.name))($event)"
        @contextmenu="openMenu($event, { kind: 'tag', tag })"
      >
        <span class="sb-ico">
          <span class="tag-dot" :style="{ background: tagColor(tag.color) }" />
        </span>
        <span class="sb-label">{{ tag.name }}</span>
        <span
          v-if="showCounts && tag.articleCount > 0"
          class="sb-count"
          >{{ tag.articleCount }}</span
        >
      </div>

      <div :style="{ height: '30px' }" />
      </div>
    </div>

    <!-- Context menu -->
    <ContextMenu
      v-if="menu"
      :x="menu.x"
      :y="menu.y"
      :items="menuItems"
      :on-close="closeMenu"
    />

    <!-- Single-field prompt (rename feed/folder/tag, new tag) -->
    <PromptDialog
      v-if="prompt"
      :title="prompt.title"
      :initial="prompt.initial"
      :placeholder="prompt.placeholder"
      @submit="submitPrompt"
      @cancel="closePrompt"
    />
  </div>
</template>
