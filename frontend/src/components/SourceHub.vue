<script setup lang="ts">
// The Source Hub — the single "发现订阅源" surface that replaced the separate
// Add-feed and Explore dialogs. One input, three states:
//
//   - empty      → browse: category chips + the curated gallery (old Explore)
//   - text       → merged results: directory hits, subscribed ones marked ✓
//   - URL pasted → live page sniff pinned on top, ALL candidate feeds listed
//                  for the user to pick (no silent first-pick), plus a direct
//                  Subscribe button for sources the sniffer can't fetch
//                  (rsshub:// short links, YouTube pages needing resolution)
//
// Every row can expand into a pre-subscribe preview (recent items + cadence)
// via the PreviewFeed service. Newsletters (IMAP) are a secondary panel behind
// a footer link rather than a first-level tab. Deep links prefill the input
// through `initialUrl`, same semantics the Add-feed dialog had.

import { useQuery, useQueryClient } from "@tanstack/vue-query";
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import * as api from "../api";
import { useFocusTrap } from "../composables/useFocusTrap";
import { useSubscribe } from "../composables/useSubscribe";
import { errorText } from "../lib/errors";
import { reportError, toast } from "../stores/toasts";
import type { DiscoveryResult } from "../types";
import ConfirmDialog from "./ConfirmDialog.vue";
import DiscoveryRow from "./DiscoveryRow.vue";
import FeedAvatar from "./FeedAvatar.vue";
import FolderPicker from "./FolderPicker.vue";
import Icon from "./Icon.vue";
import NewsletterForm from "./NewsletterForm.vue";

const props = defineProps<{
  /** Feed URL to prefill the input with — set by a `hive://` deep link. */
  initialUrl?: string;
}>();

const emit = defineEmits<{
  (e: "close"): void;
}>();

const { t, locale } = useI18n();
const qc = useQueryClient();

const dialogRef = ref<HTMLDivElement | null>(null);
useFocusTrap(dialogRef);

const panel = ref<"discover" | "newsletter">("discover");
const input = ref(props.initialUrl ?? "");
const folderId = ref<number | null>(null);
const category = ref<string | null>(null);

// A deep link can arrive while the hub is already open; surface it.
watch(
  () => props.initialUrl,
  (next) => {
    if (next) {
      input.value = next;
      panel.value = "discover";
    }
  },
);

// ── data: curated directory (dynamic — see backend DirectoryStore) ──
const directory = useQuery({
  // Keyed by language: switching the UI locale fetches that locale's slice.
  // No staleTime: Infinity anymore — the backend refreshes the directory from
  // its remote copy and App.vue invalidates this key on "directory-updated".
  queryKey: ["directory", computed(() => locale.value)],
  queryFn: () => api.searchFeedDirectory("", locale.value),
  staleTime: 60 * 60_000,
});
const feeds = useQuery({ queryKey: ["feeds"], queryFn: api.listFeeds });
const subscribedUrls = computed(
  () => new Set((feeds.data.value ?? []).map((f) => f.feedUrl)),
);

// ── input interpretation ──
const debounced = ref(input.value.trim());
let debounceHandle: number | undefined;
watch(input, (next) => {
  window.clearTimeout(debounceHandle);
  debounceHandle = window.setTimeout(() => {
    debounced.value = next.trim();
  }, 280);
});
onUnmounted(() => window.clearTimeout(debounceHandle));

/** Mirrors the backend's LooksLikeURL heuristic (discovery.go). */
function looksLikeUrl(q: string): boolean {
  if (!q || /\s/.test(q)) return false;
  if (q.includes("://")) return true;
  const host = q.split(/[/?#]/)[0];
  const labels = host.split(".").filter(Boolean);
  if (labels.length < 2) return false;
  return /^[a-z]{2,}$/i.test(labels[labels.length - 1]);
}

const browsing = computed(() => debounced.value.length < 2);
const urlish = computed(() => looksLikeUrl(debounced.value));

// ── live page sniff, only for URL-ish input (directory text search is done
// client-side below, so no backend round-trip per keystroke) ──
const sniffEnabled = computed(() => panel.value === "discover" && urlish.value);
const sniff = useQuery({
  queryKey: ["discover-scrape", debounced, computed(() => locale.value)],
  queryFn: () => api.searchFeedDirectory(debounced.value, locale.value),
  enabled: sniffEnabled,
  staleTime: 60_000,
  retry: false,
});
const sniffed = computed(() =>
  (sniff.data.value ?? []).filter((r) => !r.fromDirectory),
);

// ── directory views ──
const entries = computed(() => directory.data.value ?? []);

// Browse mode is organised language-first: a segmented control picks one
// language (defaulting to the UI locale) or everything. Category chips derive
// from the selected language only — three locales' worth of chips in one row
// read as noise ("Tech" next to 科技 next to テック). Results carry their
// entry's lang subtag, so the segmentation is a plain client-side filter.
const LANG_NAMES: Record<string, string> = { zh: "中文", en: "English", ja: "日本語" };
const uiLang = computed(() => (locale.value || "en").split(/[-_]/)[0].toLowerCase());
const langFilter = ref<string>("auto"); // "auto" = the UI locale, "all" = everything
// Languages actually present in the directory, UI locale first, then by
// first appearance in the (locale-leading) backend order.
const langs = computed(() => {
  const seen: string[] = [];
  for (const e of entries.value) {
    if (e.lang && !seen.includes(e.lang)) seen.push(e.lang);
  }
  return seen;
});
// null = browse everything. An "auto" pick for a locale the directory doesn't
// curate (say a ko UI) degrades to everything instead of an empty gallery.
const activeLang = computed<string | null>(() => {
  if (langFilter.value === "all") return null;
  const want = langFilter.value === "auto" ? uiLang.value : langFilter.value;
  return langs.value.includes(want) ? want : null;
});
function langLabel(l: string): string {
  return LANG_NAMES[l] ?? l.toUpperCase();
}
// The category pick is per-language; switching tabs resets it.
watch(activeLang, () => {
  category.value = null;
});

const langEntries = computed(() =>
  activeLang.value == null
    ? entries.value
    : entries.value.filter((e) => e.lang === activeLang.value),
);

// Categories in the directory's authored order, de-duplicated, scoped to the
// selected language.
const categories = computed(() => {
  const seen: string[] = [];
  for (const e of langEntries.value) {
    if (e.category && !seen.includes(e.category)) seen.push(e.category);
  }
  return seen;
});

// Browse gallery: the category-chip filter over the language slice.
const galleryEntries = computed(() =>
  category.value == null
    ? langEntries.value
    : langEntries.value.filter((e) => e.category === category.value),
);

// Search results: free-text filter over title/description/category, grouped by
// category in authored order.
const matchGroups = computed(() => {
  const needle = debounced.value.toLowerCase();
  const groups = new Map<string, DiscoveryResult[]>();
  for (const e of entries.value) {
    if (
      !e.title.toLowerCase().includes(needle) &&
      !(e.description ?? "").toLowerCase().includes(needle) &&
      !(e.category ?? "").toLowerCase().includes(needle)
    )
      continue;
    const cat = e.category ?? "";
    const list = groups.get(cat) ?? [];
    list.push(e);
    groups.set(cat, list);
  }
  return [...groups.entries()];
});
const matchCount = computed(() =>
  matchGroups.value.reduce((n, [, rows]) => n + rows.length, 0),
);

// ── subscribing ──
const add = useSubscribe();
const addingUrl = computed(() =>
  add.isPending.value ? add.variables.value?.url ?? null : null,
);

// ── bulk: one-click subscribe of the current (filtered) gallery view ──
// Scoped to what the user is looking at — the language tab and category chip
// narrow it — and skips already-subscribed entries. Uses the fetch-free
// QuickSubscribe (directory entries already carry title/site/description), so
// dozens of rows land in a blink; one background refresh afterwards streams
// the articles in. AddFeed's fetch-per-feed here meant minutes of spinner.
const bulkRunning = ref(false);
const bulkDone = ref(0);
const bulkTotal = ref(0);
const bulkConfirm = ref(false);
// Successes land here immediately so cards flip to ✓ while the run is still
// going (the feeds cache only refreshes once at the end).
const bulkAdded = ref(new Set<string>());
const isSubscribed = (url: string): boolean =>
  subscribedUrls.value.has(url) || bulkAdded.value.has(url);
const bulkTargets = computed(() =>
  galleryEntries.value.filter((e) => !isSubscribed(e.feedUrl)),
);

async function runBulk(): Promise<void> {
  bulkConfirm.value = false;
  const queue = [...bulkTargets.value];
  if (queue.length === 0 || bulkRunning.value) return;
  bulkRunning.value = true;
  bulkTotal.value = queue.length;
  bulkDone.value = 0;
  let ok = 0;
  let fail = 0;
  const worker = async (): Promise<void> => {
    for (;;) {
      const next = queue.shift();
      if (!next) return;
      try {
        await api.quickSubscribe(next, folderId.value);
        ok++;
        bulkAdded.value = new Set(bulkAdded.value).add(next.feedUrl);
      } catch {
        fail++;
      } finally {
        bulkDone.value++;
      }
    }
  };
  await Promise.all(Array.from({ length: Math.min(4, queue.length) }, () => worker()));
  bulkRunning.value = false;
  for (const key of [["feeds"], ["folders"], ["articles"], ["counts"], ["tags"]]) {
    qc.invalidateQueries({ queryKey: key });
  }
  toast.show(
    fail > 0
      ? t("hub.bulkDoneFail", { ok, fail })
      : t("hub.bulkDone", { ok }),
  );
  // The rows are live but article-less (QuickSubscribe skips the fetch) —
  // kick one background refresh to backfill; its feeds-updated event
  // re-renders counts and lists as articles land. Fire-and-forget: the hub
  // may close before it finishes.
  if (ok > 0) api.refreshFeeds(undefined, undefined).catch(() => {});
}
// Whether the inline error under the input applies (main-button subscribe);
// row adds report through the toast instead.
const mainErrored = ref(false);

function subscribeRow(r: DiscoveryResult): void {
  if (add.isPending.value) return;
  mainErrored.value = false;
  add.mutate(
    { url: r.feedUrl, folderId: folderId.value },
    { onError: (e) => reportError(e) },
  );
}

const canSubmit = computed(() => urlish.value && !add.isPending.value);

function submitMain(): void {
  if (!canSubmit.value) return;
  mainErrored.value = true;
  // The main button is the "I have this exact source" path (raw feed URLs,
  // rsshub:// short links, YouTube pages) — closing on success matches the old
  // Add-feed dialog. Row/card adds keep the hub open for another pick.
  add.mutate(
    { url: debounced.value, folderId: folderId.value },
    { onSuccess: () => emit("close") },
  );
}

function clearInput(): void {
  input.value = "";
  debounced.value = "";
  dialogRef.value?.querySelector("input")?.focus();
}

// ── keyboard: ↑/↓ walk the result rows, Enter adds the highlighted one ──
// The visible rows in reading order — the pinned page-sniff group, then the
// directory matches. Highlight is matched by feedUrl in the template.
const flatResults = computed<DiscoveryResult[]>(() =>
  browsing.value
    ? []
    : [...sniffed.value, ...matchGroups.value.flatMap(([, rows]) => rows)],
);
const activeIdx = ref(-1);
watch(flatResults, (list) => {
  if (activeIdx.value >= list.length) activeIdx.value = -1;
});
const activeUrl = computed(() => flatResults.value[activeIdx.value]?.feedUrl ?? null);

function moveActive(delta: number): void {
  const n = flatResults.value.length;
  if (n === 0) return;
  activeIdx.value =
    activeIdx.value < 0
      ? delta > 0
        ? 0
        : n - 1
      : Math.min(n - 1, Math.max(0, activeIdx.value + delta));
  void nextTick(() => {
    dialogRef.value
      ?.querySelector(".discover-row.kbd-active")
      ?.scrollIntoView({ block: "nearest" });
  });
}

function onInputKey(e: KeyboardEvent): void {
  if (e.key === "ArrowDown") {
    e.preventDefault();
    moveActive(1);
    return;
  }
  if (e.key === "ArrowUp") {
    e.preventDefault();
    moveActive(-1);
    return;
  }
  // Ignore the Enter that only confirms an IME candidate.
  if (e.key === "Enter" && !e.isComposing) {
    const r = flatResults.value[activeIdx.value];
    if (r) {
      if (!subscribedUrls.value.has(r.feedUrl)) subscribeRow(r);
      return;
    }
    submitMain();
  }
}

// ── scroll shadow: the browse toolbar casts one once the gallery scrolls ──
const bodyScrolled = ref(false);
function onBodyScroll(e: Event): void {
  bodyScrolled.value = (e.target as HTMLElement).scrollTop > 4;
}
watch([browsing, panel], () => {
  bodyScrolled.value = false;
});

// Escape closes the hub from anywhere inside it — but a dialog stacked on
// top of the hub (bulk confirm, the folder picker's new-folder prompt) owns
// the first Escape: dismiss it, keep the hub. Nested dialogs also render a
// .modal-backdrop, so more than one backdrop means "not ours to close".
function onWindowKey(e: KeyboardEvent): void {
  if (e.key === "Escape") {
    if (document.querySelectorAll(".modal-backdrop").length > 1) return;
    e.stopPropagation();
    if (bulkConfirm.value) {
      bulkConfirm.value = false;
      return;
    }
    emit("close");
  }
}
onMounted(() => window.addEventListener("keydown", onWindowKey, true));
onUnmounted(() => window.removeEventListener("keydown", onWindowKey, true));

/** Favicon for a gallery card (FeedAvatar falls back to a letter). */
function faviconFor(siteUrl: string | null): string | null {
  if (!siteUrl) return null;
  try {
    const host = new URL(siteUrl).hostname;
    return `https://www.google.com/s2/favicons?domain=${host}&sz=64`;
  } catch {
    return null;
  }
}
</script>

<template>
  <Teleport to="body">
    <div class="modal-backdrop" @click="emit('close')">
      <div
        class="modal modal-explore"
        ref="dialogRef"
        role="dialog"
        aria-modal="true"
        aria-labelledby="hub-title"
        @click.stop
      >
        <div class="explore-header">
          <div class="explore-header-text">
            <h2 id="hub-title">{{ t("hub.title") }}</h2>
            <p class="explore-sub">
              {{
                directory.isSuccess.value
                  ? t("hub.feedCount", { n: entries.length })
                  : t("hub.subtitle")
              }}
            </p>
          </div>
          <button
            class="explore-close"
            :aria-label="t('common.close')"
            @click="emit('close')"
          >
            <Icon name="x" :size="16" />
          </button>
        </div>

        <template v-if="panel === 'discover'">
          <div class="hub-input-row">
            <div class="explore-search hub-search">
              <Icon name="search" :size="14" />
              <input
                type="text"
                autofocus
                :placeholder="t('hub.inputPlaceholder')"
                :aria-label="t('hub.inputLabel')"
                v-model="input"
                @keydown="onInputKey"
              />
              <button
                v-if="input"
                class="hub-clear"
                :aria-label="t('hub.clearSearch')"
                :title="t('hub.clearSearch')"
                @click="clearInput"
              >
                <Icon name="x" :size="12" />
              </button>
            </div>
            <button
              class="s-btn primary"
              :disabled="!canSubmit"
              @click="submitMain"
            >
              <Icon name="plus" :size="12" />
              {{ addingUrl === debounced ? t("hub.adding") : t("hub.subscribe") }}
            </button>
          </div>
          <p class="hub-hint">{{ t("hub.hint") }}</p>
          <div v-if="add.isError.value && mainErrored" class="modal-error">
            {{ errorText(add.error.value) }}
          </div>

          <!-- ── browse: language tabs + per-language chips + gallery ── -->
          <template v-if="browsing">
            <div class="explore-toolbar hub-browse-bar" :class="{ 'hub-scrolled': bodyScrolled }">
              <div class="hub-langs-row">
                <div class="hub-langs" role="tablist" :aria-label="t('hub.languages')">
                  <button
                    v-for="l in langs"
                    :key="l"
                    role="tab"
                    :aria-selected="activeLang === l"
                    :class="['hub-lang', { active: activeLang === l }]"
                    @click="langFilter = l"
                  >
                    {{ langLabel(l) }}
                  </button>
                  <button
                    role="tab"
                    :aria-selected="activeLang === null"
                    :class="['hub-lang', { active: activeLang === null }]"
                    @click="langFilter = 'all'"
                  >
                    {{ t("hub.langAll") }}
                  </button>
                </div>
                <div class="hub-langs-right">
                  <span class="hub-lang-count">{{ t("hub.sliceCount", { n: galleryEntries.length }) }}</span>
                  <button
                    v-if="bulkRunning || bulkTargets.length > 0"
                    class="s-btn hub-bulk"
                    :disabled="bulkRunning"
                    @click="bulkConfirm = true"
                  >
                    <Icon name="check-all" :size="12" />
                    {{ bulkRunning
                      ? t("hub.bulkProgress", { done: bulkDone, total: bulkTotal })
                      : t("hub.bulkSubscribe", { n: bulkTargets.length }) }}
                  </button>
                </div>
              </div>
              <div class="explore-chips" role="tablist" :aria-label="t('hub.categories')">
                <button
                  role="tab"
                  :aria-selected="category === null"
                  :class="['explore-chip', { active: category === null }]"
                  @click="category = null"
                >
                  {{ t("hub.all") }}
                </button>
                <button
                  v-for="c in categories"
                  :key="c"
                  role="tab"
                  :aria-selected="category === c"
                  :class="['explore-chip', { active: category === c }]"
                  @click="category = c"
                >
                  {{ c }}
                </button>
              </div>
            </div>

            <div class="explore-grid" @scroll.passive="onBodyScroll">
              <template v-if="directory.isLoading.value">
                <div v-for="i in 6" :key="i" class="explore-card explore-skel" aria-hidden="true">
                  <div class="skel-head">
                    <div class="skel-avatar"></div>
                    <div class="skel-lines"><div class="skel-line"></div><div class="skel-line short"></div></div>
                  </div>
                  <div class="skel-line"></div>
                  <div class="skel-line short"></div>
                </div>
              </template>
              <div v-if="directory.isError.value" class="discover-empty">
                {{ t("hub.error") }}
              </div>
              <div
                v-for="r in galleryEntries"
                :key="r.feedUrl"
                class="explore-card"
                :class="{ subscribed: isSubscribed(r.feedUrl) }"
              >
                <div class="explore-card-head">
                  <FeedAvatar
                    :title="r.title"
                    :favicon-url="faviconFor(r.siteUrl)"
                    :size="34"
                  />
                  <div class="explore-card-meta">
                    <span class="explore-card-title">{{ r.title }}</span>
                    <!-- The category only earns its line when the gallery
                         mixes categories; under an active chip it repeats the
                         chip on every card. -->
                    <span v-if="r.category && category === null" class="explore-card-cat">{{ r.category }}</span>
                  </div>
                  <span v-if="isSubscribed(r.feedUrl)" class="explore-card-act done">
                    <Icon name="check" :size="11" />
                    {{ t("hub.subscribedState") }}
                  </span>
                  <button
                    v-else
                    class="explore-card-act"
                    :disabled="add.isPending.value || bulkRunning"
                    :aria-label="`${t('hub.add')} — ${r.title}`"
                    @click="subscribeRow(r)"
                  >
                    <Icon name="plus" :size="11" />
                    {{ addingUrl === r.feedUrl ? t("hub.adding") : t("hub.add") }}
                  </button>
                </div>
                <p class="explore-card-desc">{{ r.description }}</p>
              </div>
            </div>
          </template>

          <!-- ── results: page sniff pinned on top + directory matches ── -->
          <div v-else class="hub-results" @scroll.passive="onBodyScroll">
            <div v-if="urlish" class="discover-group">
              <div class="discover-group-label">
                {{ t("hub.fromPage") }}
                <span v-if="!sniff.isLoading.value" class="discover-group-count">{{ sniffed.length }}</span>
              </div>
              <div v-if="sniff.isLoading.value" class="discover-empty">
                {{ t("hub.sniffing") }}
              </div>
              <div
                v-else-if="sniffed.length === 0"
                class="discover-empty"
              >
                {{ t("hub.sniffNone") }}
              </div>
              <DiscoveryRow
                v-for="r in sniffed"
                :key="r.feedUrl"
                :result="r"
                :subscribed="subscribedUrls.has(r.feedUrl)"
                :adding="addingUrl === r.feedUrl"
                :busy="add.isPending.value"
                :active="activeUrl === r.feedUrl"
                @add="subscribeRow"
              />
            </div>

            <div
              v-for="[cat, rows] in matchGroups"
              :key="cat"
              class="discover-group"
            >
              <div class="discover-group-label">
                {{ cat }}
                <span class="discover-group-count">{{ rows.length }}</span>
              </div>
              <DiscoveryRow
                v-for="r in rows"
                :key="r.feedUrl"
                :result="r"
                :subscribed="subscribedUrls.has(r.feedUrl)"
                :adding="addingUrl === r.feedUrl"
                :busy="add.isPending.value"
                :active="activeUrl === r.feedUrl"
                @add="subscribeRow"
              />
            </div>

            <div v-if="!urlish && matchCount === 0" class="hub-empty">
              <Icon name="search" :size="22" />
              <p>{{ t("hub.noResults") }}</p>
            </div>
          </div>

          <div class="explore-footer">
            <button class="hub-nl-link" @click="panel = 'newsletter'">
              <Icon name="inbox" :size="12" />
              {{ t("hub.newsletterEntry") }}
            </button>
            <div class="hub-footer-right">
              <FolderPicker v-model="folderId" />
              <button class="s-btn primary" @click="emit('close')">
                {{ t("common.done") }}
              </button>
            </div>
          </div>
        </template>

        <!-- ── secondary panel: IMAP newsletter ── -->
        <template v-else>
          <NewsletterForm
            @back="panel = 'discover'"
            @added="emit('close')"
          />
        </template>

        <!-- Bulk subscribe is N network subscriptions in one go — confirm
             with the count spelled out before firing. -->
        <ConfirmDialog
          v-if="bulkConfirm"
          :title="t('hub.bulkConfirmTitle')"
          :message="t('hub.bulkConfirmText', { n: bulkTargets.length })"
          :confirm-label="t('hub.bulkConfirm')"
          :danger="false"
          @confirm="runBulk"
          @cancel="bulkConfirm = false"
        />
      </div>
    </div>
  </Teleport>
</template>
