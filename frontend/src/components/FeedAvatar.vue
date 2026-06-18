<script setup lang="ts">
// A feed's logo. Shows the site's favicon when one is available, and falls
// back to the coloured letter avatar when there is no icon — or when the
// icon fails to load. Ported from papr's FeedAvatar.tsx.
//
// papr took { title, faviconUrl, seed, style }; hive passes either a whole
// `feed` or the discrete fields. Title / favicon / seed are resolved from the
// `feed` prop when present, with the explicit props overriding.

import { computed, ref, watch } from "vue";
import type { Feed } from "../types";

const props = withDefaults(
  defineProps<{
    /** A feed to render the avatar for — supplies title / favicon / seed. */
    feed?: Feed;
    /** Display title, overrides `feed.title`. Used for the letter fallback. */
    title?: string;
    /** The feed's favicon URL, if the backend resolved one. */
    faviconUrl?: string | null;
    /** Square pixel size; also drives the proportional border-radius. */
    size?: number;
  }>(),
  {},
);

// --- avatar / colour derivation (ported 1:1 from lib/feedMeta.ts) ---

const PALETTE = [
  "#7c5cff", "#2c8a3e", "#0a6bd4", "#d23a8b", "#a8501f", "#ff6600",
  "#c0392b", "#d05050", "#3a4cb8", "#4a4a4a", "#1d8a8a", "#b85c00",
  "#1c1c1c", "#5200ff", "#1a73e8", "#0f9d8c",
];

/** Stable accent color for a feed, hashed off its seed so it never shifts. */
function feedColor(seed: string | number): string {
  const s = String(seed);
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0;
  return PALETTE[h % PALETTE.length];
}

/** Scripts where a single character already reads as a whole word-unit:
 *  CJK ideographs (incl. Extension A) plus Japanese kana and Korean Hangul. */
const CJK_GLYPH = /[\u3040-\u30ff\u3400-\u9fff\uac00-\ud7af\uf900-\ufaff]/;

/** The first whole code point of a string — `s[0]` would split an
 *  astral-plane character into a lone surrogate, rendering as a broken `�`. */
function firstCodePoint(s: string): string {
  return Array.from(s)[0] ?? "";
}

/** 1–2 character avatar label: first CJK glyph, or latin initials. */
function feedAvatar(title: string): string {
  const t = (title || "").trim();
  if (!t) return "?";
  const chars = Array.from(t);
  const first = chars[0];
  if (CJK_GLYPH.test(first)) return first;
  const words = t.split(/[\s·|—-]+/).filter(Boolean);
  const firstInitial = words[0] ? firstCodePoint(words[0]) : "";
  if (words.length >= 2 && /[a-zA-Z]/.test(firstInitial))
    return (firstInitial + firstCodePoint(words[1])).toUpperCase();
  return chars.slice(0, 2).join("").toUpperCase();
}

// --- resolved inputs ---

const resolvedTitle = computed(() => props.title ?? props.feed?.title ?? "");
const resolvedFavicon = computed(
  () => props.faviconUrl ?? props.feed?.faviconUrl ?? null,
);
/** Hash seed for the fallback colour — the feed id when we have a feed. */
const resolvedSeed = computed<string | number>(
  () => props.feed?.id ?? resolvedTitle.value,
);

const broken = ref(false);

// The same component instance is reused across list rows as feeds change,
// so clear the error flag whenever the icon URL does.
watch(resolvedFavicon, () => {
  broken.value = false;
});

const showIcon = computed(() => !!resolvedFavicon.value && !broken.value);

const avatarStyle = computed(() => {
  const style: Record<string, string> = {};
  if (!showIcon.value) style.background = feedColor(resolvedSeed.value);
  if (props.size != null) {
    style.width = `${props.size}px`;
    style.height = `${props.size}px`;
    style.borderRadius = `${Math.round(props.size * 0.22)}px`;
  }
  return style;
});

const label = computed(() => feedAvatar(resolvedTitle.value));
</script>

<template>
  <span class="sb-feed-avatar" :class="{ 'has-icon': showIcon }" :style="avatarStyle">
    <img
      v-if="showIcon"
      :src="resolvedFavicon!"
      alt=""
      loading="lazy"
      :draggable="false"
      @error="broken = true"
    />
    <template v-else>{{ label }}</template>
  </span>
</template>
