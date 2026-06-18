<script setup lang="ts">
// One discovery result row in the Source Hub — used for both curated-directory
// hits and live page-scrape candidates (the two renderings AddFeedDialog and
// ExploreDialog used to implement separately). Carries the subscribed ✓ state,
// the add button, and an expandable pre-subscribe preview (recent item titles
// plus an update-cadence hint) backed by the PreviewFeed service.

import { useQuery } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import * as api from "../api";
import { errorText } from "../lib/errors";
import type { DiscoveryResult } from "../types";
import FeedAvatar from "./FeedAvatar.vue";
import Icon from "./Icon.vue";

const props = defineProps<{
  result: DiscoveryResult;
  subscribed: boolean;
  /** An add for THIS row is in flight. */
  adding: boolean;
  /** Any add is in flight — disables the button without the spinner label. */
  busy: boolean;
  /** Keyboard-highlighted row (↑/↓ from the search input; Enter adds it). */
  active?: boolean;
}>();

const emit = defineEmits<{
  (e: "add", r: DiscoveryResult): void;
}>();

const { t, locale } = useI18n();

/**
 * A favicon URL derived from the entry's site URL via the same Google s2
 * service add_feed uses on the backend; FeedAvatar falls back to a coloured
 * letter when it fails to load.
 */
const favicon = computed(() => {
  if (!props.result.siteUrl) return null;
  try {
    const host = new URL(props.result.siteUrl).hostname;
    return `https://www.google.com/s2/favicons?domain=${host}&sz=64`;
  } catch {
    return null;
  }
});

// ── pre-subscribe preview, fetched lazily on expand ──
const expanded = ref(false);
const preview = useQuery({
  queryKey: ["feed-preview", computed(() => props.result.feedUrl)],
  queryFn: () => api.previewFeed(props.result.feedUrl),
  enabled: expanded,
  staleTime: 5 * 60_000,
  retry: false,
});

/** "3 天前" style relative date for a preview item. */
function relDate(iso: string | null): string {
  if (!iso) return "";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "";
  const days = Math.round((Date.now() - then) / 86_400_000);
  const rtf = new Intl.RelativeTimeFormat(locale.value, { numeric: "auto" });
  if (Math.abs(days) < 1) return rtf.format(0, "day");
  if (Math.abs(days) < 30) return rtf.format(-days, "day");
  return rtf.format(-Math.round(days / 30), "month");
}

// Update cadence estimated from the preview item dates: average gap between
// the newest and oldest of the sample.
const cadence = computed(() => {
  const items = preview.data.value?.items ?? [];
  const times = items
    .map((i) => (i.publishedAt ? new Date(i.publishedAt).getTime() : NaN))
    .filter((n) => !Number.isNaN(n))
    .sort((a, b) => b - a);
  if (times.length < 2) return null;
  const spanDays = (times[0] - times[times.length - 1]) / 86_400_000;
  const gap = spanDays / (times.length - 1);
  if (gap < 1) return t("hub.cadenceDaily");
  if (gap < 10) return t("hub.cadenceEveryDays", { n: Math.round(gap) });
  return t("hub.cadenceMonthly");
});
</script>

<template>
  <div class="discover-row" :class="{ open: expanded, 'kbd-active': active }">
    <div class="discover-main">
      <FeedAvatar :title="result.title" :favicon-url="favicon" :size="26" />
      <button
        class="discover-text"
        type="button"
        :aria-expanded="expanded"
        :title="t('hub.previewToggle')"
        @click="expanded = !expanded"
      >
        <span class="discover-title">{{ result.title }}</span>
        <span v-if="result.description" class="discover-desc">{{ result.description }}</span>
        <span v-else class="discover-desc discover-url">{{ result.feedUrl }}</span>
      </button>
      <span v-if="result.category" class="discover-cat">{{ result.category }}</span>
      <!-- Same pill vocabulary as the gallery cards: quiet ✓ badge when
           subscribed, tinted add pill otherwise. -->
      <span v-if="subscribed" class="explore-card-act done">
        <Icon name="check" :size="11" /> {{ t("hub.subscribedState") }}
      </span>
      <button
        v-else
        class="explore-card-act"
        :disabled="busy"
        :aria-label="`${t('hub.add')} — ${result.title}`"
        @click="emit('add', result)"
      >
        <Icon name="plus" :size="11" />
        {{ adding ? t("hub.adding") : t("hub.add") }}
      </button>
    </div>

    <div v-if="expanded" class="discover-preview">
      <div v-if="preview.isLoading.value" class="discover-preview-note">
        {{ t("hub.previewLoading") }}
      </div>
      <div v-else-if="preview.isError.value" class="discover-preview-note">
        {{ errorText(preview.error.value) }}
      </div>
      <template v-else-if="preview.data.value">
        <div v-if="cadence" class="discover-preview-cadence">{{ cadence }}</div>
        <ul class="discover-preview-items">
          <li v-for="(it, i) in preview.data.value.items ?? []" :key="i">
            <span class="pv-title">{{ it.title }}</span>
            <span class="pv-date">{{ relDate(it.publishedAt) }}</span>
          </li>
        </ul>
        <div
          v-if="(preview.data.value.items ?? []).length === 0"
          class="discover-preview-note"
        >
          {{ t("hub.previewEmpty") }}
        </div>
      </template>
    </div>
  </div>
</template>
