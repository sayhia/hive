<script setup lang="ts">
// ⌘K command palette: a search box over actions, feeds and articles plus a
// keyboard-driven runner. Ported 1:1 from papr's React CommandPalette.tsx —
// same CSS class names and behavior. React hooks mapped to Vue: useState→ref,
// useEffect→watch/onMounted, useRef(dom)→template ref, useMemo→computed.
//
// Lives behind a Teleport so its fixed overlay escapes any transformed/
// overflow-clipped ancestor. The parent keeps it mounted and toggles `open`.

import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { useQuery } from "@tanstack/vue-query";
import { useI18n } from "vue-i18n";
import * as api from "../api";
import { useUi } from "../stores/ui";
import { feedHost, relTime } from "../lib/feedMeta";
import { modCombo } from "../lib/platform";
import type { ArticleSummary, Feed } from "../types";
import Icon, { type IconName } from "./Icon.vue";
import FeedAvatar from "./FeedAvatar.vue";
import { useFocusTrap } from "../composables/useFocusTrap";

export type CommandAction =
  | "mark-all-read"
  | "toggle-theme"
  | "toggle-focus"
  | "toggle-ai"
  | "ai-ask"
  | "ai-digest"
  | "refresh"
  | "add-feed"
  | "new-folder"
  | "opml"
  | "open-settings";

interface Item {
  id: string;
  group: "action" | "feed" | "article";
  icon: IconName | null;
  feed?: Feed;
  label: string;
  hint?: string;
  run: () => void;
}

const props = defineProps<{ open: boolean }>();
const emit = defineEmits<{
  (e: "close"): void;
  (e: "action", action: CommandAction): void;
  (e: "navigateFeed", feed: Feed): void;
  (e: "navigateArticle", article: ArticleSummary): void;
}>();

const ACTIONS: { icon: IconName; labelKey: string; hint: string; action: CommandAction }[] = [
  { icon: "check-all", labelKey: "commandPalette.actionMarkAllRead", hint: "⇧A", action: "mark-all-read" },
  { icon: "globe", labelKey: "commandPalette.actionToggleTheme", hint: "⇧D", action: "toggle-theme" },
  { icon: "sparkle", labelKey: "commandPalette.actionToggleAi", hint: "I", action: "toggle-ai" },
  { icon: "search", labelKey: "commandPalette.actionAiAsk", hint: "?", action: "ai-ask" },
  { icon: "list", labelKey: "commandPalette.actionAiDigest", hint: "", action: "ai-digest" },
  { icon: "refresh", labelKey: "commandPalette.actionRefresh", hint: modCombo("R"), action: "refresh" },
  { icon: "plus", labelKey: "commandPalette.actionAddFeed", hint: "A", action: "add-feed" },
  { icon: "folder", labelKey: "commandPalette.actionNewFolder", hint: "", action: "new-folder" },
  { icon: "open", labelKey: "commandPalette.actionOpml", hint: "", action: "opml" },
  { icon: "settings", labelKey: "commandPalette.actionOpenSettings", hint: modCombo(","), action: "open-settings" },
];

const { t } = useI18n();
const ui = useUi();

// The "Toggle AI summary" action only does anything while an article is open
// (its handler in App no-ops otherwise). Listing it unconditionally would let
// the user pick a command that silently does nothing — so hide it when there is
// no article to summarise.
const hasArticle = computed(() => ui.selectedArticleId != null);

const query = ref("");
const debounced = ref("");
const active = ref(0);
const inputRef = ref<HTMLInputElement | null>(null);
const listRef = ref<HTMLDivElement | null>(null);
const cpRef = ref<HTMLDivElement | null>(null);
// True while the selection just moved by keyboard. Mouse hover also sets
// `active`, but scrolling the list to a hovered row yanks content under the
// cursor — and can land a different row under it, cascading more hover events.
// So the scroll-into-view watcher below only fires for keyboard nav.
let keyboardNav = false;

useFocusTrap(cpRef, computed(() => props.open));

let focusTimer: number | undefined;
let trigger: HTMLElement | null = null;
let debounceTimer: number | undefined;

watch(
  () => props.open,
  (open) => {
    window.clearTimeout(focusTimer);
    if (!open) {
      // Return focus to whatever was focused when the palette opened (the ⌘K
      // trigger lives nowhere in particular), so closing it doesn't drop the
      // user on <body>.
      trigger?.focus?.();
      trigger = null;
      return;
    }
    trigger = document.activeElement as HTMLElement | null;
    query.value = "";
    debounced.value = "";
    active.value = 0;
    focusTimer = window.setTimeout(() => inputRef.value?.focus(), 30);
  },
  { immediate: true },
);

watch(query, (q) => {
  window.clearTimeout(debounceTimer);
  debounceTimer = window.setTimeout(() => (debounced.value = q.trim()), 180);
});

onBeforeUnmount(() => {
  window.clearTimeout(focusTimer);
  window.clearTimeout(debounceTimer);
});

const feeds = useQuery({
  queryKey: ["feeds"],
  queryFn: api.listFeeds,
  enabled: computed(() => props.open),
});

const articleResults = useQuery({
  queryKey: ["cp-search", debounced],
  queryFn: () => api.listArticles({ kind: "all" }, false, debounced.value, false, 8, 0),
  enabled: computed(() => props.open && debounced.value.length > 0),
});

const items = computed<Item[]>(() => {
  const q = debounced.value.toLowerCase();
  const out: Item[] = [];

  for (const a of ACTIONS) {
    // Skip article-scoped actions when nothing is open — they would no-op.
    if (a.action === "toggle-ai" && !hasArticle.value) continue;
    const label = t(a.labelKey);
    if (q && !label.toLowerCase().includes(q)) continue;
    out.push({
      id: `act-${a.action}`,
      group: "action",
      icon: a.icon,
      label,
      hint: a.hint,
      run: () => emit("action", a.action),
    });
  }

  const matchedFeeds = (feeds.data.value ?? [])
    .filter(
      (f) =>
        !q ||
        f.title.toLowerCase().includes(q) ||
        feedHost(f).toLowerCase().includes(q),
    )
    .slice(0, 6);
  for (const f of matchedFeeds) {
    out.push({
      id: `feed-${f.id}`,
      group: "feed",
      icon: null,
      feed: f,
      label: f.title,
      hint: feedHost(f),
      run: () => emit("navigateFeed", f),
    });
  }

  if (q) {
    for (const a of articleResults.data.value ?? []) {
      out.push({
        id: `article-${a.id}`,
        group: "article",
        icon: a.isStarred ? "star-fill" : "rss",
        label: a.title,
        hint: relTime(a.publishedAt),
        run: () => emit("navigateArticle", a),
      });
    }
  }

  return out;
});

watch(
  () => items.value.length,
  (len) => {
    if (active.value >= len) active.value = 0;
  },
);

// A new query reselects the first row — scroll the list back to the top so that
// row is visible (the keyboardNav-gated watcher below deliberately ignores this
// non-keyboard `active` reset).
watch(debounced, () => {
  if (listRef.value) listRef.value.scrollTop = 0;
});

// Keep the keyboard-selected row visible when arrowing past the fold. Skipped
// for mouse-driven selection changes — see `keyboardNav`.
watch(active, (idx) => {
  if (!keyboardNav) return;
  keyboardNav = false;
  nextTick(() => {
    listRef.value
      ?.querySelector<HTMLElement>(`[data-cp-index="${idx}"]`)
      ?.scrollIntoView({ block: "nearest" });
  });
});

const run = (it: Item) => {
  it.run();
  emit("close");
};

const handleKey = (e: KeyboardEvent) => {
  if (e.key === "ArrowDown") {
    e.preventDefault();
    keyboardNav = true;
    active.value = (active.value + 1) % Math.max(items.value.length, 1);
  } else if (e.key === "ArrowUp") {
    e.preventDefault();
    keyboardNav = true;
    active.value =
      (active.value - 1 + items.value.length) % Math.max(items.value.length, 1);
  } else if (e.key === "Enter" && !e.isComposing) {
    // `isComposing` skips the Enter that only confirms an IME candidate (CJK
    // search input) — without it the first result fires mid-typing.
    e.preventDefault();
    const it = items.value[active.value];
    if (it) run(it);
  } else if (e.key === "Escape") {
    e.preventDefault();
    emit("close");
  }
};

// The flat index of an item in render order, so hover/aria-activedescendant
// line up across the three groups. Recomputed as a map keyed by item id.
const flatIndex = computed(() => {
  const map: Record<string, number> = {};
  let i = -1;
  for (const group of ["action", "feed", "article"] as const) {
    for (const it of items.value) {
      if (it.group !== group) continue;
      i++;
      map[it.id] = i;
    }
  }
  return map;
});

const groupItems = (key: Item["group"]) => items.value.filter((i) => i.group === key);
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="cp-backdrop" @click="emit('close')">
      <div class="cp" ref="cpRef" @click.stop>
        <div class="cp-input">
          <Icon name="search" :size="16" />
          <input
            ref="inputRef"
            :value="query"
            @input="query = ($event.target as HTMLInputElement).value"
            @keydown="handleKey"
            :placeholder="t('commandPalette.searchPlaceholder')"
            :aria-label="t('commandPalette.searchPlaceholder')"
            role="combobox"
            :aria-expanded="items.length > 0"
            aria-controls="cp-listbox"
            :aria-activedescendant="items.length > 0 ? `cp-option-${active}` : undefined"
            aria-autocomplete="list"
          />
          <span class="cp-esc">ESC</span>
        </div>
        <div class="cp-list" id="cp-listbox" role="listbox" ref="listRef">
          <div v-if="items.length === 0" class="cp-empty">
            {{
              articleResults.isFetching.value
                ? t("commandPalette.searching")
                : articleResults.isError.value && debounced.length > 0
                  ? t("commandPalette.searchError")
                  : t("commandPalette.noResults")
            }}
          </div>
          <template v-else>
            <template
              v-for="grp in [
                { key: 'action', title: t('commandPalette.groupActions') },
                { key: 'feed', title: t('commandPalette.groupFeeds') },
                { key: 'article', title: t('commandPalette.groupArticles') },
              ]"
              :key="grp.key"
            >
              <div
                v-if="groupItems(grp.key as Item['group']).length > 0"
                role="group"
                :aria-label="grp.title"
              >
                <div class="cp-group-title" aria-hidden="true">{{ grp.title }}</div>
                <div
                  v-for="it in groupItems(grp.key as Item['group'])"
                  :key="it.id"
                  :data-cp-index="flatIndex[it.id]"
                  :id="`cp-option-${flatIndex[it.id]}`"
                  role="option"
                  :aria-selected="flatIndex[it.id] === active"
                  :class="`cp-item ${flatIndex[it.id] === active ? 'active' : ''}`"
                  @mouseenter="active = flatIndex[it.id]"
                  @click="run(it)"
                >
                  <span class="cp-ico">
                    <FeedAvatar
                      v-if="it.feed"
                      :title="it.feed.title"
                      :faviconUrl="it.feed.faviconUrl"
                      :seed="it.feed.id"
                    />
                    <Icon v-else :name="it.icon ?? 'rss'" :size="15" />
                  </span>
                  <span class="cp-label">{{ it.label }}</span>
                  <span v-if="it.hint" class="cp-hint">{{ it.hint }}</span>
                </div>
              </div>
            </template>
            <!--
              The article search failing must not be masked just because an
              action or feed still matched the query — items.length would then be
              non-zero and the empty-state error branch above never shows. Surface
              the failure as a trailing notice instead, so a partial result list
              still tells the user article search broke rather than silently
              omitting matches.
            -->
            <div
              v-if="articleResults.isError.value && debounced.length > 0"
              class="cp-notice"
              role="status"
            >
              <Icon name="alert" :size="13" />
              {{ t("commandPalette.searchError") }}
            </div>
          </template>
        </div>
        <div class="cp-footer">
          <span>
            <kbd>↑</kbd>
            <kbd>↓</kbd> {{ t("commandPalette.footerSelect") }}
          </span>
          <span> <kbd>⏎</kbd> {{ t("commandPalette.footerOpen") }} </span>
          <span> <kbd>esc</kbd> {{ t("commandPalette.footerClose") }} </span>
          <div style="flex: 1" />
          <span>{{ t("commandPalette.footerHint") }}</span>
        </div>
      </div>
    </div>
  </Teleport>
</template>
