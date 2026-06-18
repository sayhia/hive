<script setup lang="ts">
// Subscriptions: search, OPML import/export, the RSSHub instance, and a
// per-feed list with refresh-interval override and unsubscribe. Ported from
// papr's `SubscriptionsSection` (+ its `RsshubInstanceGroup`).
//
// papr threaded an `onToast` callback and a `useArticleActions().refreshAfterBulk()`
// hook; hive raises toasts through the global store and inlines the
// article-cache invalidation (the `articleActions` composable's equivalent),
// matching Sidebar.vue / AddFeedDialog.vue. "Add" is emitted up to the dialog,
// which closes Settings and opens the Add-feed modal (App.vue wiring).

import { computed, onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useQuery, useQueryClient } from "@tanstack/vue-query";
import * as api from "../../api";
import { reportError, toast, withUndo } from "../../stores/toasts";
import { pendingFeedDeletes } from "../../lib/pendingDeletes";
import { feedHost } from "./helpers";
import type { Feed } from "../../types";
import Icon from "../Icon.vue";
import FeedAvatar from "../FeedAvatar.vue";
import FeedEditFields from "./FeedEditFields.vue";

const props = defineProps<{
  feeds: Feed[];
}>();

const emit = defineEmits<{
  (e: "add-feed"): void;
}>();

const { t } = useI18n();
const qc = useQueryClient();

const search = ref("");
const fileRef = ref<HTMLInputElement | null>(null);

const filtered = computed(() =>
  props.feeds.filter(
    (f) => !search.value || f.title.toLowerCase().includes(search.value.toLowerCase()),
  ),
);

// ── Per-feed edit panel ──
// One row expands at a time (accordion). The form itself lives in FeedEditFields,
// shared with the home-page FeedEditDialog so the two entry points can't drift.
const editingId = ref<number | null>(null);
function toggleEdit(id: number): void {
  editingId.value = editingId.value === id ? null : id;
}

// Inlined equivalent of papr's `actions.refreshAfterBulk()` — invalidate only
// the article-bearing caches, not AI summaries / settings / storage stats.
function refreshAfterBulk(): void {
  for (const key of [
    ["smartCounts"], ["feeds"], ["folders"], ["tags"],
    ["articles"], ["article"], ["search"], ["cp-search"],
  ]) {
    qc.invalidateQueries({ queryKey: key });
  }
}

async function exportOpml(): Promise<void> {
  try {
    // The backend opens a native save panel and writes the file (the WKWebview
    // can't save a synthetic <a download>); it returns the chosen path, or an
    // empty string if the user cancelled.
    const savedPath = await api.exportOpml();
    if (!savedPath) return;
    toast.show(t("settings.subscriptions.opmlExported"));
  } catch (e) {
    reportError(e);
  }
}

async function importOpml(file: File): Promise<void> {
  try {
    const n = await api.importOpml(await file.text());
    await qc.invalidateQueries();
    toast.show(t("settings.subscriptions.opmlImported", { count: n }));
  } catch (e) {
    reportError(e);
  }
}

function onFileChange(e: Event): void {
  const input = e.target as HTMLInputElement;
  const f = input.files?.[0];
  if (f) importOpml(f);
  input.value = "";
}

function unsubscribe(f: Feed): void {
  // Match the sidebar's delete affordance: an optimistic removal behind a 6s
  // Undo window, so a mis-click doesn't irreversibly drop the feed and all its
  // articles with no recourse. The id parks in pendingFeedDeletes (which
  // api.listFeeds filters on every fetch) so a mid-window refetch can't
  // flicker the row back; undo / a failed delete just un-parks and refetches
  // backend truth — no stale snapshot to clobber concurrent cache updates.
  const resync = () => {
    pendingFeedDeletes.delete(f.id);
    qc.invalidateQueries({ queryKey: ["feeds"] });
  };
  withUndo({
    text: t("settings.subscriptions.unsubscribed", { title: f.title }),
    apply: () => {
      pendingFeedDeletes.add(f.id);
      qc.setQueryData<Feed[]>(["feeds"], (old) => old?.filter((x) => x.id !== f.id));
    },
    commit: () =>
      api
        .deleteFeed(f.id)
        .then(() => refreshAfterBulk())
        .catch(reportError)
        .finally(resync),
    revert: resync,
  });
}

// ── Multi-select + batch unsubscribe ──
// Selected feed ids. A reactive Set tracks add/delete/has/size, so the checkboxes
// and the bulk bar update live. Selection persists across search (by id).
const selected = reactive(new Set<number>());
const allSelected = computed(
  () => filtered.value.length > 0 && filtered.value.every((f) => selected.has(f.id)),
);
function toggleOne(id: number): void {
  if (selected.has(id)) selected.delete(id);
  else selected.add(id);
}
function toggleAll(): void {
  if (allSelected.value) filtered.value.forEach((f) => selected.delete(f.id));
  else filtered.value.forEach((f) => selected.add(f.id));
}
function clearSelection(): void {
  selected.clear();
}
// Batch delete — same optimistic-remove + 6s Undo window + pendingFeedDeletes
// parking as the single unsubscribe, but for every selected id in one call.
function unsubscribeSelected(): void {
  const ids = [...selected];
  if (!ids.length) return;
  selected.clear();
  const resync = () => {
    for (const id of ids) pendingFeedDeletes.delete(id);
    qc.invalidateQueries({ queryKey: ["feeds"] });
  };
  withUndo({
    text: t("settings.subscriptions.unsubscribedN", { count: ids.length }),
    apply: () => {
      for (const id of ids) pendingFeedDeletes.add(id);
      qc.setQueryData<Feed[]>(["feeds"], (old) => old?.filter((x) => !ids.includes(x.id)));
    },
    commit: () =>
      api
        .deleteFeeds(ids)
        .then(() => refreshAfterBulk())
        .catch(reportError)
        .finally(resync),
    revert: resync,
  });
}

// ── RSSHub instance for expanding `rsshub://route` short links. ──
const rsshub = ref("");
onMounted(() => {
  api
    .getSetting("rsshub_instance")
    .then((v) => (rsshub.value = v ?? ""))
    .catch(() => {});
});
function commitRsshub(): void {
  api.setSetting("rsshub_instance", rsshub.value.trim()).catch(() => {});
}
function onRsshubKey(e: KeyboardEvent): void {
  if (e.key === "Enter") (e.currentTarget as HTMLInputElement).blur();
}

// ── curated directory: multi-source list + manual refresh. ──
// The Source Hub's directory merges the official list with any user-added
// directory URLs (dedup by feedUrl, official wins) and refreshes itself lazily
// (24h TTL). This group manages the source list — the fonts add-by-URL
// philosophy applied to whole directories — and offers the refresh escape
// hatch: see a stale list, hit refresh, done.
const dirSources = useQuery({
  queryKey: ["directory-sources"],
  queryFn: api.listDirectorySources,
});
const dirNewUrl = ref("");
const dirAdding = ref(false);
function addDirSource(): void {
  const url = dirNewUrl.value.trim();
  if (!url || dirAdding.value) return;
  dirAdding.value = true;
  api
    .addDirectorySource(url)
    .then((list) => {
      qc.setQueryData(["directory-sources"], list);
      qc.invalidateQueries({ queryKey: ["directory"] });
      qc.invalidateQueries({ queryKey: ["directory-status"] });
      dirNewUrl.value = "";
      const added = list.find((s) => s.url === url);
      toast.show(t("settings.subscriptions.dirSourceAdded", { count: added?.count ?? 0 }));
    })
    .catch(reportError)
    .finally(() => (dirAdding.value = false));
}
function onDirNewKey(e: KeyboardEvent): void {
  if (e.key === "Enter" && !e.isComposing) addDirSource();
}
function removeDirSource(url: string): void {
  api
    .removeDirectorySource(url)
    .then((list) => {
      qc.setQueryData(["directory-sources"], list);
      qc.invalidateQueries({ queryKey: ["directory"] });
      qc.invalidateQueries({ queryKey: ["directory-status"] });
      toast.show(t("settings.subscriptions.dirSourceRemoved"));
    })
    .catch(reportError);
}
/** Shorten a source URL for the row label: host + trailing file. */
function dirSourceLabel(url: string): string {
  try {
    const u = new URL(url);
    const tail = u.pathname.split("/").filter(Boolean).pop() ?? "";
    return tail ? `${u.hostname}/…/${tail}` : u.hostname;
  } catch {
    return url;
  }
}

const dirStatus = useQuery({ queryKey: ["directory-status"], queryFn: api.directoryStatus });
const dirRefreshing = ref(false);
function refreshDirectory(): void {
  if (dirRefreshing.value) return;
  dirRefreshing.value = true;
  api
    .refreshFeedDirectory()
    .then((st) => {
      qc.setQueryData(["directory-status"], st);
      qc.invalidateQueries({ queryKey: ["directory"] });
      // A 304 leaves the copy as-is; say so, or "nothing new" reads as a
      // failed click.
      toast.show(
        st.changed
          ? t("settings.subscriptions.dirRefreshed", { count: st.count })
          : t("settings.subscriptions.dirUpToDate"),
      );
    })
    .catch(reportError)
    .finally(() => (dirRefreshing.value = false));
}

// "137 个订阅源 · 目录日期 2026-07-09 · 检查于 …" status line.
const dirInfo = computed(() => {
  const st = dirStatus.data.value;
  if (!st) return t("settings.subscriptions.dirDesc");
  const parts = [t("settings.subscriptions.dirCount", { count: st.count })];
  if (st.updatedAt) parts.push(t("settings.subscriptions.dirAuthored", { date: st.updatedAt }));
  if (st.checkedAt) {
    const d = new Date(st.checkedAt);
    if (!Number.isNaN(d.getTime())) {
      parts.push(t("settings.subscriptions.dirChecked", { time: d.toLocaleString() }));
    }
  }
  return parts.join(" · ");
});
</script>

<template>
  <input
    ref="fileRef"
    type="file"
    accept=".opml,.xml"
    :style="{ display: 'none' }"
    @change="onFileChange"
  />

  <div class="settings-group" :style="{ marginBottom: '18px' }">
    <div :style="{ display: 'flex', gap: '8px', alignItems: 'center' }">
      <div
        :style="{
          flex: 1,
          display: 'flex',
          alignItems: 'center',
          gap: '8px',
          padding: '6px 10px',
          borderRadius: '7px',
          border: '1px solid var(--hair-strong)',
          background: 'var(--panel)',
        }"
      >
        <Icon name="search" :size="13" color="var(--muted)" />
        <input
          v-model="search"
          :placeholder="t('settings.subscriptions.searchPlaceholder')"
          :style="{
            flex: 1,
            border: 0,
            outline: 0,
            background: 'transparent',
            fontFamily: 'inherit',
            fontSize: '12.5px',
            color: 'var(--ink)',
          }"
        />
      </div>
      <button class="s-btn" @click="fileRef?.click()">
        <Icon name="arrow-down" :size="12" /> {{ t("settings.subscriptions.importOpml") }}
      </button>
      <button class="s-btn" @click="exportOpml">
        <Icon name="arrow-up" :size="12" /> {{ t("settings.subscriptions.export") }}
      </button>
      <button class="s-btn primary" @click="emit('add-feed')">
        <Icon name="plus" :size="12" /> {{ t("common.add") }}
      </button>
    </div>
  </div>

  <!-- RSSHub instance -->
  <div class="settings-group" :style="{ marginBottom: '18px' }">
    <h3 class="settings-group-title">{{ t("settings.subscriptions.rsshubTitle") }}</h3>
    <div class="settings-row">
      <div class="settings-row-text">
        <div class="settings-row-label">{{ t("settings.subscriptions.rsshubInstance") }}</div>
        <div class="settings-row-desc">{{ t("settings.subscriptions.rsshubInstanceDesc") }}</div>
      </div>
      <div class="settings-row-control">
        <input
          v-model="rsshub"
          @blur="commitRsshub"
          @keydown="onRsshubKey"
          placeholder="https://rsshub.app"
          :spellcheck="false"
          autocapitalize="off"
          autocorrect="off"
          :aria-label="t('settings.subscriptions.rsshubInstance')"
          :style="{
            width: '220px',
            padding: '5px 9px',
            borderRadius: '7px',
            border: '1px solid var(--hair-strong)',
            background: 'var(--panel)',
            fontFamily: 'inherit',
            fontSize: '12.5px',
            color: 'var(--ink)',
            outline: 0,
          }"
        />
      </div>
    </div>
  </div>

  <!-- Curated directory: status + manual refresh + custom source URL -->
  <div class="settings-group" :style="{ marginBottom: '18px' }">
    <h3 class="settings-group-title">{{ t("settings.subscriptions.dirTitle") }}</h3>
    <div class="settings-row">
      <div class="settings-row-text">
        <div class="settings-row-label">{{ t("settings.subscriptions.dirStatus") }}</div>
        <div class="settings-row-desc">{{ dirInfo }}</div>
      </div>
      <div class="settings-row-control">
        <button class="s-btn" :disabled="dirRefreshing" @click="refreshDirectory">
          <Icon name="refresh" :size="12" />
          {{ dirRefreshing
            ? t("settings.subscriptions.dirRefreshing")
            : t("settings.subscriptions.dirRefresh") }}
        </button>
      </div>
    </div>
    <!-- Source list: official first (fixed), then user-added directories. -->
    <div
      v-for="s in dirSources.data.value ?? []"
      :key="s.url || 'official'"
      class="settings-row"
    >
      <div class="settings-row-text">
        <div class="settings-row-label" :style="s.official ? {} : { fontFamily: 'var(--mono)', fontSize: '12px' }">
          {{ s.official ? t("settings.subscriptions.dirOfficial") : dirSourceLabel(s.url) }}
        </div>
        <div class="settings-row-desc">
          <template v-if="s.ok">
            {{ t("settings.subscriptions.dirCount", { count: s.count }) }}
            <template v-if="s.updatedAt"> · {{ t("settings.subscriptions.dirAuthored", { date: s.updatedAt }) }}</template>
          </template>
          <template v-else>{{ t("settings.subscriptions.dirSourcePending") }}</template>
        </div>
      </div>
      <div class="settings-row-control">
        <button
          v-if="!s.official"
          class="icon-btn"
          :title="t('settings.subscriptions.dirSourceRemove')"
          :aria-label="`${t('settings.subscriptions.dirSourceRemove')} — ${s.url}`"
          @click="removeDirSource(s.url)"
        >
          <Icon name="trash" :size="13" />
        </button>
      </div>
    </div>

    <!-- Add a directory by URL. -->
    <div class="settings-row">
      <div class="settings-row-text">
        <div class="settings-row-label">{{ t("settings.subscriptions.dirSourceAdd") }}</div>
        <div class="settings-row-desc">{{ t("settings.subscriptions.dirSourceAddDesc") }}</div>
      </div>
      <div class="settings-row-control" :style="{ display: 'flex', gap: '8px' }">
        <input
          v-model="dirNewUrl"
          @keydown="onDirNewKey"
          placeholder="https://…/directory.json"
          :spellcheck="false"
          autocapitalize="off"
          autocorrect="off"
          :aria-label="t('settings.subscriptions.dirSourceAdd')"
          :style="{
            width: '220px',
            padding: '5px 9px',
            borderRadius: '7px',
            border: '1px solid var(--hair-strong)',
            background: 'var(--panel)',
            fontFamily: 'inherit',
            fontSize: '12.5px',
            color: 'var(--ink)',
            outline: 0,
          }"
        />
        <button
          class="s-btn"
          :disabled="!dirNewUrl.trim() || dirAdding"
          @click="addDirSource"
        >
          {{ dirAdding ? t("settings.subscriptions.dirSourceAdding") : t("common.add") }}
        </button>
      </div>
    </div>
  </div>

  <div class="settings-group">
    <h3 class="settings-group-title">
      {{ t("settings.subscriptions.feedsCount", { count: filtered.length }) }}
    </h3>
    <div v-if="filtered.length" class="s-feed-bulk">
      <label class="s-feed-selall">
        <input type="checkbox" class="s-feed-check" :checked="allSelected" @change="toggleAll" />
        <span>{{
          selected.size
            ? t("settings.subscriptions.selectedCount", { count: selected.size })
            : t("settings.subscriptions.selectAll")
        }}</span>
      </label>
      <div v-if="selected.size" class="s-feed-bulk-actions">
        <button class="s-btn" @click="clearSelection">
          {{ t("settings.subscriptions.clearSelection") }}
        </button>
        <button class="s-btn danger" @click="unsubscribeSelected">
          <Icon name="trash" :size="12" />
          {{ t("settings.subscriptions.deleteSelected", { count: selected.size }) }}
        </button>
      </div>
    </div>
    <div>
      <div v-for="f in filtered" :key="f.id" class="s-feed-item">
        <div class="s-feed-row" :class="{ selected: selected.has(f.id) }">
          <input
            type="checkbox"
            class="s-feed-check"
            :checked="selected.has(f.id)"
            :aria-label="t('settings.subscriptions.selectFeed', { title: f.title })"
            @change="toggleOne(f.id)"
          />
          <FeedAvatar :feed="f" :size="22" />
          <span class="name">{{ f.title }}</span>
          <span class="url">{{ feedHost(f) }}</span>
          <div class="actions">
            <button
              class="icon-btn"
              :title="t('settings.subscriptions.edit')"
              :aria-label="`${t('settings.subscriptions.edit')} — ${f.title}`"
              @click="toggleEdit(f.id)"
            >
              <Icon :name="editingId === f.id ? 'chevron-down' : 'chevron-right'" :size="13" />
            </button>
            <button
              class="icon-btn"
              :title="t('settings.subscriptions.unsubscribe')"
              @click="unsubscribe(f)"
            >
              <Icon name="trash" :size="13" />
            </button>
          </div>
        </div>

        <FeedEditFields v-if="editingId === f.id" :feed="f" />
      </div>
      <div
        v-if="filtered.length === 0"
        :style="{ padding: '16px 4px', fontSize: '13px', color: 'var(--muted)' }"
      >
        {{ t("settings.subscriptions.noMatch") }}
      </div>
    </div>
  </div>
</template>
