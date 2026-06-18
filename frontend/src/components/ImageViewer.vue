<script setup lang="ts">
// Full-screen image viewer. Opens from a click on an article-body image and
// cycles through every image in the article via the on-screen arrows or the
// ← / → keys; Escape or a backdrop click closes it, and a counter shows the
// position in the set. Videos are intentionally excluded — sanitize forces
// `controls` on every <video>, so they already have native fullscreen.
//
// The `.image-viewer` root class doubles as the "a modal is open" marker the
// global shortcut handlers bail on (App.vue onKey, Reader's space/`t` keys),
// so j/k article flips can't yank the article out from under the gallery.

import { onBeforeUnmount, onMounted, ref } from "vue";
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

const i = ref(props.index);
const many = props.srcs.length > 1;

const prev = () => (i.value = (i.value - 1 + props.srcs.length) % props.srcs.length);
const next = () => (i.value = (i.value + 1) % props.srcs.length);

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
</script>

<template>
  <div class="image-viewer" role="dialog" aria-modal="true" @click="emit('close')">
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

    <img class="iv-img" :src="srcs[i]" alt="" draggable="false" @click.stop />

    <button
      v-if="many"
      class="iv-btn iv-next"
      :aria-label="t('reader.viewerNext')"
      @click.stop="next()"
    >
      <Icon name="chevron-right" :size="22" />
    </button>

    <div v-if="many" class="iv-counter" @click.stop>{{ i + 1 }} / {{ srcs.length }}</div>
  </div>
</template>
