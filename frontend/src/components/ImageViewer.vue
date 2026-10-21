<script setup lang="ts">
// Full-screen image viewer. Opens from a click on an article-body image and
// cycles through every image in the article via the on-screen arrows or the
// ← / → keys; Escape or a backdrop click closes it, and a counter shows the
// position in the set. Videos are intentionally excluded — sanitize forces
// `controls` on every <video>, so they already have native fullscreen.
//
// The wheel zooms (anchored to the cursor) and the image can be dragged to
// pan at any zoom level (ported from papr #132). Videos are intentionally
// excluded — sanitize forces `controls` on every <video>, so they already
// have native fullscreen.
//
// The `.image-viewer` root class doubles as the "a modal is open" marker the
// global shortcut handlers bail on (App.vue onKey, Reader's space/`t` keys),
// so j/k article flips can't yank the article out from under the gallery.

import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import Icon from "./Icon.vue";

const props = defineProps<{
  /** The working (already-loaded, possibly proxied) src of every image in the
   *  article, in reading order. */
  srcs: string[];
  /** Index of the image the user clicked. */
  index: number;
}>();

const emit = defineEmits<{ (e: "close"): void }>();
const { t } = useI18n();

// Zoom bounds. Min < 1 lets the user zoom out below the fit-to-viewport size;
// max caps how far in before the pixels blur into mush.
const MIN_SCALE = 0.5;
const MAX_SCALE = 4;

const clamp = (n: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, n));

const rootEl = ref<HTMLElement | null>(null);
const imgEl = ref<HTMLImageElement | null>(null);

const i = ref(props.index);
const many = props.srcs.length > 1;

// The current zoom/pan transform. Kept outside reactivity and applied straight
// to the <img> style — wheel events fire far faster than Vue can re-render, so
// reactive state would lag the pointer. Only a change of shown image resets it.
const view = { scale: 1, tx: 0, ty: 0 };
const drag = { startX: 0, startY: 0, tx0: 0, ty0: 0, active: false };
// Set on a drag's pointerup so the click that immediately follows (retargeted
// to the image by pointer capture) can't bubble up and close the viewer.
let justDragged = false;

function applyView(next: { scale: number; tx: number; ty: number }): void {
  view.scale = next.scale;
  view.tx = next.tx;
  view.ty = next.ty;
  const el = imgEl.value;
  if (el) {
    el.style.transform = `translate3d(${next.tx}px, ${next.ty}px, 0) scale(${next.scale})`;
    el.style.cursor = "grab";
  }
}

// Return to the fit view. Called when the shown image changes, so the next
// image never renders (even for a frame) at the previous one's zoom level.
function resetView(): void {
  view.scale = 1;
  view.tx = 0;
  view.ty = 0;
  drag.active = false;
  const el = imgEl.value;
  if (el) {
    el.style.transform = "";
    el.style.cursor = "";
  }
}

const prev = () => {
  resetView();
  i.value = (i.value - 1 + props.srcs.length) % props.srcs.length;
};
const next = () => {
  resetView();
  i.value = (i.value + 1) % props.srcs.length;
};

// Zoom to `s1` while keeping the point under (x, y) stationary. With
// `transform-origin: 0 0`, an element point at screen offset
// (x - rect.left, y - rect.top) from the image's box must keep that offset
// under the cursor, so the translation is moved by (1 - ratio) × it.
function zoomAt(s1: number, x: number, y: number): void {
  const img = imgEl.value;
  if (!img) return;
  const target = clamp(s1, MIN_SCALE, MAX_SCALE);
  const ratio = target / view.scale;
  if (ratio === 1) return;
  const rect = img.getBoundingClientRect();
  applyView({
    scale: target,
    tx: view.tx + (1 - ratio) * (x - rect.left),
    ty: view.ty + (1 - ratio) * (y - rect.top),
  });
}

// The wheel handler needs `passive: false` so it can stop the reader behind
// the viewer scrolling (and ctrl+wheel page-zoom); Vue attaches wheel
// listeners passively, so this one is bound natively.
function onWheel(e: WheelEvent): void {
  e.preventDefault();
  // Line-mode wheel events carry tiny deltas (e.g. 3); scale them up.
  const dy = e.deltaMode === 1 ? e.deltaY * 16 : e.deltaY;
  zoomAt(view.scale * Math.exp(-dy * 0.0015), e.clientX, e.clientY);
}
onMounted(() => rootEl.value?.addEventListener("wheel", onWheel, { passive: false }));
onBeforeUnmount(() => rootEl.value?.removeEventListener("wheel", onWheel));

// Reset zoom/pan whenever the shown image changes (arrows, keys).
watch(i, resetView);

// Dragging pans the image — freely, to any position, at any zoom level.
function onPointerDown(e: PointerEvent): void {
  (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  drag.startX = e.clientX;
  drag.startY = e.clientY;
  drag.tx0 = view.tx;
  drag.ty0 = view.ty;
  drag.active = true;
  (e.currentTarget as HTMLElement).style.cursor = "grabbing";
  e.stopPropagation();
}
function onPointerMove(e: PointerEvent): void {
  if (!drag.active) return;
  applyView({
    scale: view.scale,
    tx: drag.tx0 + (e.clientX - drag.startX),
    ty: drag.ty0 + (e.clientY - drag.startY),
  });
  (e.currentTarget as HTMLElement).style.cursor = "grabbing";
  e.stopPropagation();
}
function onPointerUp(e: PointerEvent): void {
  const wasDrag = drag.active;
  drag.active = false;
  if (!wasDrag) return;
  // Only swallow the click that trails a *real* drag; a plain click's own
  // click event is already stopped by the image handler.
  if (Math.hypot(e.clientX - drag.startX, e.clientY - drag.startY) > 4) {
    justDragged = true;
  }
  applyView({ ...view });
  try {
    (e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId);
  } catch {
    /* capture may already be gone */
  }
  e.stopPropagation();
}

// Capture-phase so the viewer wins over App.vue's reader shortcuts (← / →
// flip articles, Escape closes the reader overlay) while it is open.
function onKey(e: KeyboardEvent): void {
  if (e.key === "Escape") emit("close");
  else if (e.key === "ArrowLeft" || e.key === "[") prev();
  else if (e.key === "ArrowRight" || e.key === "]") next();
  else return;
  e.preventDefault();
  e.stopPropagation();
}
onMounted(() => window.addEventListener("keydown", onKey, true));
onBeforeUnmount(() => window.removeEventListener("keydown", onKey, true));

function onBackdropClick(): void {
  // A click trailing a drag (pointer capture retargets it) would land on
  // the backdrop only when capture wasn't honoured — swallow it rather
  // than closing the viewer the moment a pan ends.
  if (justDragged) {
    justDragged = false;
    return;
  }
  emit("close");
}
</script>

<template>
  <div ref="rootEl" class="image-viewer" role="dialog" aria-modal="true" @click="onBackdropClick">
    <button
      class="iv-btn iv-close"
      :aria-label="t('reader.viewerClose')"
      :title="t('reader.viewerClose')"
      @click.stop="emit('close')"
    >
      <Icon name="x" :size="18" />
    </button>

    <button
      v-if="many"
      class="iv-btn iv-prev"
      :aria-label="t('reader.viewerPrev')"
      @click.stop="prev()"
    >
      <Icon name="chevron-right" :size="22" />
    </button>

    <img
      ref="imgEl"
      class="iv-img"
      :src="srcs[i]"
      alt=""
      draggable="false"
      @click.stop="justDragged = false"
      @dblclick.stop="
        zoomAt(view.scale > 1.001 ? 1 : 2, $event.clientX, $event.clientY)
      "
      @pointerdown="onPointerDown"
      @pointermove="onPointerMove"
      @pointerup="onPointerUp"
      @pointercancel="onPointerUp"
    />

    <button
      v-if="many"
      class="iv-btn iv-next"
      :aria-label="t('reader.viewerNext')"
      @click.stop="next()"
    >
      <Icon name="chevron-right" :size="22" />
    </button>

    <div v-if="many" class="iv-counter" @click.stop>{{ i + 1 }} / {{ srcs.length }}</div>

    <div class="iv-hint">{{ t("reader.viewerHint") }}</div>
  </div>
</template>
