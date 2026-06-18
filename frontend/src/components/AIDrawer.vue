<script setup lang="ts">
// AI summary drawer for the Reader. Streams a summary from the backend the first
// time it opens for an article, accumulating tokens into `text`, and renders the
// sanitized markdown. Ported from papr's Reader.tsx `AIDrawer` (React → Vue 3).
//
// The parent keys this by article id, so switching articles remounts it and the
// `text` state re-initialises from the new article's stored summary.

import { useQueryClient } from "@tanstack/vue-query";
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import * as api from "../api";
import { reportError, toast } from "../stores/toasts";
import { renderMarkdown } from "../lib/markdown";
import { makeLinkClickHandler } from "../lib/readerLinks";
import type { ArticleDetail } from "../types";
import Icon from "./Icon.vue";

const props = defineProps<{
  article: ArticleDetail;
  onClose: () => void;
  // Opens app settings at the AI section — used by the "missing API key" state
  // so the user can fix the cause without hunting through menus.
  onOpenSettings: () => void;
}>();

const { t } = useI18n();
const qc = useQueryClient();

// Initialised from the article's stored summary (if any). The parent keys this
// component by article id, so a switch remounts it and re-runs this initialiser
// — no separate "reset on article change" effect is needed.
const text = ref<string | null>(props.article.aiSummary);
const busy = ref(false);
const failed = ref(false);
const retry = ref(0);

// Why a failure happened, so the panel can guide instead of just saying "failed":
// a missing key links to settings, a body-less article explains itself, anything
// else offers a retry.
type FailReason = "noKey" | "noBody" | "generic";
const failReason = ref<FailReason>("generic");

// Identifies the latest summarize run. A newer run (Retry) supersedes an older
// one, so only the run whose generation still matches may touch `busy` on settle.
let runSeq = 0;

// Generate on mount — the card is mounted only while open (v-if on the toolbar
// toggle) — and again on Retry. `text`/`failed` are in the guard so a cached
// summary shows as-is and a failed run isn't silently re-attempted.
watch(
  retry,
  (_v, _old, onCleanup) => {
    if (busy.value || text.value || failed.value) return;
    const run = ++runSeq;
    let cancelled = false;
    // Whether the stream settled (resolved or rejected) on its own. If cleanup
    // runs while this is still false, the drawer was closed mid-stream — the
    // accumulated `text` is then a truncated fragment.
    let settled = false;
    // An error raised inside the stream surfaces twice: once as an `error`
    // channel event (carrying the precise provider message) and again as the
    // command's rejected promise. Toast only the first so the user does not see
    // the same failure reported twice; the `.catch` still toasts for failures
    // that abort before streaming starts (no key, bad config) and so never emit
    // an `error` event.
    let sawErrorEvent = false;
    busy.value = true;
    text.value = "";
    api
      .aiSummarize(props.article.id, (ev) => {
        if (cancelled) return;
        if (ev.type === "delta") text.value = (text.value ?? "") + ev.data;
        else if (ev.type === "error") {
          sawErrorEvent = true;
          failReason.value = "generic";
          failed.value = true;
          toast.error(ev.data);
        }
      })
      .then(() => {
        if (cancelled) return;
        // A resolved-but-empty stream (no delta tokens and no error event) would
        // otherwise leave the drawer blank with no retry affordance — surface it
        // as a failure so the user gets the error + retry instead of nothing.
        if (!text.value && !sawErrorEvent) {
          failReason.value = "generic";
          failed.value = true;
        } else {
          qc.invalidateQueries({ queryKey: ["article", props.article.id] });
        }
      })
      .catch((e) => {
        if (!cancelled && !sawErrorEvent) {
          failReason.value = failReasonFromError(e);
          failed.value = true;
          // noKey / noBody render their own guidance, so a toast would just
          // double up; only the generic failure path needs one.
          if (failReason.value === "generic") reportError(e);
        }
      })
      .finally(() => {
        settled = true;
        // Clear `busy` only for the current run — a newer run, or an unmount
        // (which bumps runSeq), supersedes us, and touching `busy` then would
        // either wedge the loading state or write to a torn-down ref.
        if (runSeq === run) busy.value = false;
      });

    onCleanup(() => {
      cancelled = true;
      // Closed mid-stream: the backend discards an interrupted generation (it is
      // never persisted), so drop the partial fragment held here too. Reopening
      // then re-generates from scratch instead of freezing on a half-summary.
      if (!settled) text.value = props.article.aiSummary;
    });
  },
  { immediate: true },
);

const loading = computed(() => busy.value && !text.value);

// The single "redo" path: drop whatever is shown — a cached result, a finished
// summary, or a failed attempt — and re-stream. The backend always regenerates
// and re-persists, so this serves both the header refresh and the error retry
// (and is how a stale cached summary, e.g. one in the wrong language, gets
// replaced). Bumping `retry` re-runs the watcher, which regenerates because
// `text`/`failed` are now cleared.
const regenerate = () => {
  if (busy.value) return;
  text.value = "";
  failed.value = false;
  retry.value += 1;
};

const copy = () => {
  if (!text.value) return;
  navigator.clipboard.writeText(text.value).then(
    () => toast.show(t("reader.textCopied")),
    () => {},
  );
};

function failReasonFromError(e: unknown): FailReason {
  const code = e && typeof e === "object" && "code" in e ? (e as { code?: string }).code : undefined;
  if (code === "noAiKey") return "noKey";
  if (code === "noArticleBody") return "noBody";
  return "generic";
}

// Parse + sanitize the summary only when the text changes.
const html = computed(() => (text.value ? renderMarkdown(text.value) : ""));

const onProseClick = makeLinkClickHandler(props.article.url);

const rootRef = ref<HTMLElement | null>(null);
// Opened while scrolled down the article? Bring the card into view so the click
// doesn't feel like nothing happened (`nearest` scrolls the minimum needed).
onMounted(() => {
  const reduce =
    document.documentElement.dataset.reduceMotion === "true" ||
    window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  rootRef.value?.scrollIntoView({ behavior: reduce ? "auto" : "smooth", block: "nearest" });
});

onBeforeUnmount(() => {
  // Supersede any in-flight run so its `finally` can't touch a torn-down ref.
  runSeq++;
});
</script>

<template>
  <div
    ref="rootRef"
    class="ai-drawer"
    role="complementary"
    :aria-label="t('reader.aiSummaryTitle')"
  >
    <div class="ai-head">
      <span class="accent-ico">
        <Icon name="sparkle-fill" :size="15" />
      </span>
      <h3>{{ t("reader.aiSummaryTitle") }}</h3>
      <div class="ai-head-actions">
        <template v-if="text && !failed">
          <button
            class="tb-btn"
            @click="regenerate"
            :disabled="busy"
            :title="t('reader.aiRegenerate')"
            :aria-label="t('reader.aiRegenerate')"
          >
            <Icon name="refresh" :size="14" />
          </button>
          <button
            class="tb-btn"
            @click="copy"
            :disabled="busy"
            :title="t('common.copy')"
            :aria-label="t('common.copy')"
          >
            <Icon name="copy" :size="14" />
          </button>
        </template>
        <button
          class="tb-btn"
          @click="onClose"
          :title="t('common.close')"
          :aria-label="t('common.close')"
        >
          <Icon name="x" :size="14" />
        </button>
      </div>
    </div>
    <div class="ai-body" aria-live="polite" :aria-busy="busy">
      <div v-if="loading" class="ai-loading">
        <span class="ai-dot" />
        <span class="ai-dot" />
        <span class="ai-dot" />
        <span :style="{ marginLeft: '4px' }">{{ t("reader.aiReadingFullText") }}</span>
      </div>
      <div v-else-if="failed" class="ai-error">
        <Icon name="alert" :size="18" />
        <span v-if="failReason === 'noKey'">{{ t("error.noAiKey") }}</span>
        <span v-else-if="failReason === 'noBody'">{{ t("error.noArticleBody") }}</span>
        <span v-else>{{ t("reader.aiError") }}</span>
        <button v-if="failReason === 'noKey'" class="empty-retry" @click="onOpenSettings">
          <Icon name="settings" :size="12" />
          {{ t("reader.aiOpenSettings") }}
        </button>
        <button v-else-if="failReason === 'generic'" class="empty-retry" @click="regenerate">
          <Icon name="refresh" :size="12" />
          {{ t("common.retry") }}
        </button>
      </div>
      <template v-else-if="text">
        <div class="ai-prose" @click="onProseClick" v-html="html" />
        <div v-if="busy" class="ai-generating">
          <span class="ai-dot" />
          {{ t("reader.aiGenerating") }}
        </div>
        <div v-else class="ai-disclaimer">{{ t("reader.aiDisclaimer") }}</div>
      </template>
    </div>
  </div>
</template>
