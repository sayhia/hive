<script setup lang="ts">
// Persistent audio mini-player. Mounted once at the app shell, it keeps a
// podcast episode playing while the user browses other articles. Renders
// nothing until a track is loaded. Ported 1:1 from papr's PlayerBar.tsx.

import { onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { usePlayer, PLAYBACK_RATES } from "../stores/player";
import { useUi } from "../stores/ui";
import Icon from "./Icon.vue";

const { t } = useI18n();
const player = usePlayer();
const ui = useUi();

const audioRef = ref<HTMLAudioElement>();
const time = ref(0);
const duration = ref(0);
const failed = ref(false);
// True between starting a new src `load()` and its metadata arriving. The
// element fires a spurious `pause` while it resets for the new src; that pause
// must not be mirrored into the store (it would cancel the new track). A plain
// flag, not reactive — it gates event handlers, it is never rendered.
let loading = false;

// m:ss for a seconds value, or h:mm:ss past an hour (podcasts run long);
// "–:––" while the duration is unknown.
function clock(s: number): string {
  if (!isFinite(s) || s < 0) return "–:––";
  const total = Math.floor(s);
  const sec = (total % 60).toString().padStart(2, "0");
  const min = Math.floor(total / 60) % 60;
  const hr = Math.floor(total / 3600);
  return hr > 0 ? `${hr}:${min.toString().padStart(2, "0")}:${sec}` : `${min}:${sec}`;
}

// The three effects below mirror papr's three useEffects. They run with
// `flush: "post"` so they fire *after* the DOM updates — the React equivalent
// of an effect running post-commit, which guarantees the <audio> template ref
// is populated when a track first appears (null → set renders the element in
// the same tick). `onMounted` runs them once so a track already present at
// mount is loaded, matching React effects firing on first render.

// Load a new src and reset the timeline when the track changes.
function syncSrc() {
  const el = audioRef.value;
  if (!el || !player.track) return;
  loading = true;
  el.src = player.track.src;
  el.load();
  time.value = 0;
  duration.value = 0;
  failed.value = false;
}
watch(() => player.track?.src, syncSrc, { flush: "post" });

// Reflect play/pause intent onto the element. (papr's effect depended on
// [playing, track.src, setPlaying, failed].)
function syncPlaying() {
  const el = audioRef.value;
  if (!el || !player.track) return;
  if (player.playing) {
    // Re-requesting playback of a track that previously failed (same src, so
    // the track-change watcher above does not re-run) should retry the load —
    // the failure may have been a transient network error. Without this the
    // player stays permanently stuck on "load error".
    if (failed.value) {
      failed.value = false;
      // `el.load()` resets the element, which fires a transient `pause`. Flag
      // the reload so `onPause` ignores that pause — otherwise it would mirror
      // `playing → false` into the store and cancel the very retry the user
      // just asked for (leaving the track stuck until a 2nd click).
      loading = true;
      el.load();
    }
    el.play().catch(() => player.setPlaying(false));
  } else {
    el.pause();
  }
}
// `failed` is intentionally NOT a dependency here. syncPlaying clears it
// (`failed.value = false`) on a retry; listing it would re-run the watch
// mid-retry and fire a second `el.play()` against the still-loading element —
// that rejects with AbortError and bounces playback back to paused, so the retry
// would need a second click. A retry is already driven by `playing` flipping to
// true, so reading `failed.value` inside the handler is enough.
watch([() => player.playing, () => player.track?.src], syncPlaying, { flush: "post" });

// Keep the element's speed in sync with the store. `defaultPlaybackRate` is set
// alongside `playbackRate` because loading a new src resets the live
// `playbackRate` back to `defaultPlaybackRate` once the media is ready — so
// without this a track switch would silently snap the user's chosen speed back
// to 1×. The `onLoadedMetadata` handler re-asserts it after the load actually
// completes, which is when the reset would otherwise land.
function syncRate() {
  const el = audioRef.value;
  if (el) {
    el.defaultPlaybackRate = player.rate;
    el.playbackRate = player.rate;
  }
}
watch([() => player.rate, () => player.track?.src], syncRate, { flush: "post" });

// First-mount pass, mirroring React effects running after the initial render.
onMounted(() => {
  syncSrc();
  syncPlaying();
  syncRate();
});

// A finite, seekable upper bound. The element's `duration` is `Infinity` for a
// live stream and `NaN` before metadata loads — feeding either into the
// `<input type="range">` `max` makes the browser reject the attribute and fall
// back to its default max of 100, which pins the thumb to the far right and
// turns a drag into a meaningless 0–100s seek. `clock(duration)` still gets the
// raw value so an unknown total shows as "–:––".
const seek = (to: number) => {
  const el = audioRef.value;
  if (el && isFinite(to)) {
    el.currentTime = to;
    time.value = to;
  }
};
const seekMax = () => (Number.isFinite(duration.value) && duration.value > 0 ? duration.value : 0);
const nudge = (delta: number) =>
  seek(Math.min(seekMax() || Infinity, Math.max(0, time.value + delta)));
const cycleRate = () => {
  // indexOf is -1 for an unknown rate, so this lands on the first entry.
  const i = PLAYBACK_RATES.indexOf(player.rate as (typeof PLAYBACK_RATES)[number]);
  player.setRate(PLAYBACK_RATES[(i + 1) % PLAYBACK_RATES.length]);
};

// ── audio element event handlers ──
const onTimeUpdate = (e: Event) => {
  time.value = (e.currentTarget as HTMLAudioElement).currentTime;
};
const onLoadedMetadata = (e: Event) => {
  // The new src is ready: any further `pause` is now a genuine one (user /
  // media key), so let `onPause` mirror it to the store.
  loading = false;
  const el = e.currentTarget as HTMLAudioElement;
  duration.value = el.duration;
  // The element resets playbackRate to its default once new media is loaded —
  // re-apply the chosen speed so a track switch keeps it.
  el.playbackRate = player.rate;
};
const onEnded = () => player.setPlaying(false);
// The element's play state can change without the store driving it — hardware
// media keys (F8 / AirPods) and the macOS Now Playing widget pause/resume the
// <audio> directly. Mirror those back into the store so the play/pause button
// never shows the wrong icon (and a click is not wasted just re-syncing the
// state). Re-asserting the same value is a no-op for both the store and the
// play/pause effect.
const onPlay = () => {
  // Do NOT clear `loading` here. The `play` event can fire before
  // `loadedmetadata` during a track switch; clearing it early would let the
  // transient `pause` (fired while the element resets for the new src) slip past
  // onPause's `!loading` guard and cancel the new track. Only loadedmetadata /
  // onError own clearing `loading`.
  player.setPlaying(true);
};
const onPause = () => {
  // `pause` also fires transiently while the element resets for a new src; that
  // pause is internal, not a user/media-key pause, so it must not flip the
  // store off (it would cancel the new track).
  if (!loading) player.setPlaying(false);
};
const onError = () => {
  // A bad enclosure URL or unsupported codec would otherwise leave the bar
  // stuck at 0:00 with no hint of what went wrong.
  loading = false;
  failed.value = true;
  player.setPlaying(false);
};
</script>

<template>
  <div v-if="player.track" class="player-bar">
    <audio
      ref="audioRef"
      @timeupdate="onTimeUpdate"
      @loadedmetadata="onLoadedMetadata"
      @ended="onEnded"
      @play="onPlay"
      @pause="onPause"
      @error="onError"
      preload="metadata"
    />

    <span class="player-ico">
      <Icon name="headphones" :size="15" />
    </span>

    <div class="player-meta">
      <button
        class="player-title"
        @click="ui.openArticle(player.track.articleId)"
        :title="player.track.title"
        :aria-label="t('player.openArticle', { title: player.track.title })"
      >
        {{ player.track.title }}
      </button>
      <span class="player-feed">{{ player.track.feedTitle }}</span>
    </div>

    <div class="player-controls">
      <button
        class="player-btn"
        @click="nudge(-15)"
        :title="t('player.back15')"
        :aria-label="t('player.back15')"
        :disabled="failed || !(seekMax() > 0)"
      >
        <Icon name="skip-back" :size="15" />
      </button>
      <button
        class="player-btn play"
        @click="player.toggle()"
        :title="failed ? t('player.retry') : player.playing ? t('player.pause') : t('player.play')"
        :aria-label="failed ? t('player.retry') : player.playing ? t('player.pause') : t('player.play')"
      >
        <Icon :name="player.playing ? 'pause' : 'play'" :size="15" />
      </button>
      <button
        class="player-btn"
        @click="nudge(30)"
        :title="t('player.fwd30')"
        :aria-label="t('player.fwd30')"
        :disabled="failed || !(seekMax() > 0)"
      >
        <Icon name="skip-fwd" :size="15" />
      </button>
    </div>

    <span v-if="failed" class="player-error">
      <Icon name="alert" :size="13" />
      {{ t("player.loadError") }}
    </span>
    <template v-else>
      <span class="player-time">{{ clock(time) }}</span>
      <input
        class="player-scrub"
        type="range"
        :min="0"
        :max="seekMax()"
        :step="1"
        :value="Math.min(time, seekMax())"
        :disabled="!(seekMax() > 0)"
        @input="seek(Number(($event.target as HTMLInputElement).value))"
        :aria-label="t('player.seek')"
        :aria-valuetext="clock(time)"
      />
      <span class="player-time">{{ clock(duration) }}</span>
    </template>

    <button
      class="player-btn rate"
      @click="cycleRate"
      :title="t('player.speed')"
      :aria-label="t('player.speedValue', { rate: player.rate })"
    >
      {{ player.rate }}×
    </button>
    <button
      class="player-btn"
      @click="player.close()"
      :title="t('player.close')"
      :aria-label="t('player.close')"
    >
      <Icon name="x" :size="14" />
    </button>
  </div>
</template>
