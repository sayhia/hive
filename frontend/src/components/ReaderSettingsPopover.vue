<script setup lang="ts">
// Compact reading settings over an open article — font, size, leading, width,
// theme. Same values as Settings → Reading; this is the in-place control.
// Pattern follows hiread's Aa panel, scoped to hive's existing reader prefs.

import { computed, nextTick, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useUi, READER_BOUNDS, READER_DEFAULTS, type Theme } from "../stores/ui";
import { resolveReaderFont } from "../lib/fonts";
import { useFocusTrap } from "../composables/useFocusTrap";
import Icon from "./Icon.vue";

const emit = defineEmits<{
  (e: "close"): void;
  (e: "open-settings"): void;
}>();

const { t } = useI18n();
const ui = useUi();
const panel = ref<HTMLDivElement>();
useFocusTrap(panel);

const SYSTEM_FONTS = [
  "PingFang SC", "Songti SC", "Heiti SC", "STSong",
  "Microsoft YaHei", "Segoe UI", "Helvetica Neue", "system-ui",
];

const bundled = [
  { value: "serif", labelKey: "settings.reading.serif" },
  { value: "sans", labelKey: "settings.reading.sans" },
  { value: "hyperlegible", labelKey: "settings.reading.hyperlegible" },
] as const;
const isBundled = computed(() => bundled.some((f) => f.value === ui.readerFont));
const installed = computed(() =>
  ui.installedFonts.filter((f) => f.axis === "reader" || f.axis === "both"),
);

const THEMES: { value: Theme; key: "light" | "dark" | "auto" }[] = [
  { value: "light", key: "light" },
  { value: "dark", key: "dark" },
  { value: "auto", key: "auto" },
];

type SliderKey = "size" | "leading" | "width";
const sliders: {
  key: SliderKey;
  label: () => string;
  min: number;
  max: number;
  step: number;
  unit: string;
  get: () => number;
  set: (n: number) => void;
  reset: number;
}[] = [
  {
    key: "size",
    label: () => t("settings.reading.fontSize"),
    min: READER_BOUNDS.size.min,
    max: READER_BOUNDS.size.max,
    step: 1,
    unit: "px",
    get: () => ui.readerSize,
    set: (n) => ui.setReader({ readerSize: n }),
    reset: READER_DEFAULTS.size,
  },
  {
    key: "leading",
    label: () => t("settings.reading.lineHeight"),
    min: READER_BOUNDS.leading.min,
    max: READER_BOUNDS.leading.max,
    step: 5,
    unit: "%",
    get: () => ui.readerLeading,
    set: (n) => ui.setReader({ readerLeading: n }),
    reset: READER_DEFAULTS.leading,
  },
  {
    key: "width",
    label: () => t("reader.type.width"),
    min: READER_BOUNDS.width.min,
    max: READER_BOUNDS.width.max,
    step: 20,
    unit: "px",
    get: () => ui.readerWidth,
    set: (n) => ui.setReader({ readerWidth: n }),
    reset: READER_DEFAULTS.width,
  },
];

function nudge(s: (typeof sliders)[number], dir: -1 | 1) {
  s.set(s.get() + dir * s.step);
}

function onSliderWheel(s: (typeof sliders)[number], e: WheelEvent) {
  if (document.activeElement !== e.target) return;
  e.preventDefault();
  nudge(s, e.deltaY > 0 ? -1 : 1);
}

function stackOf(choice: string) {
  return resolveReaderFont(choice, ui.installedFonts).stack;
}

function openMore() {
  emit("close");
  emit("open-settings");
}

onMounted(() => {
  void nextTick(() => panel.value?.focus());
});
</script>

<template>
  <div
    ref="panel"
    class="rs-pop"
    role="dialog"
    aria-modal="true"
    tabindex="-1"
    :aria-label="t('reader.type.title')"
  >
    <header class="rs-pop-head">
      <strong>{{ t("reader.type.title") }}</strong>
      <button
        type="button"
        class="rs-pop-close"
        :aria-label="t('common.close')"
        @click="emit('close')"
      >
        <Icon name="x" :size="14" />
      </button>
    </header>

    <div class="rs-pop-body">
      <div class="rs-pop-faces" role="group" :aria-label="t('settings.reading.font')">
        <button
          v-for="f in bundled"
          :key="f.value"
          type="button"
          class="rs-pop-face"
          :class="{ on: ui.readerFont === f.value }"
          :aria-pressed="ui.readerFont === f.value"
          :style="{ fontFamily: stackOf(f.value) }"
          @click="ui.setReaderFont(f.value)"
        >
          <span class="rs-pop-face-aa">Aa</span>
          <span class="rs-pop-face-name">{{ t(f.labelKey) }}</span>
        </button>
      </div>
      <div class="rs-pop-row">
        <span class="rs-pop-label">{{ t("reader.type.moreFonts") }}</span>
        <div class="rs-pop-control">
          <select
            class="s-select"
            :value="isBundled ? '' : ui.readerFont"
            :aria-label="t('settings.fonts.readerFont')"
            :style="{ fontFamily: isBundled ? undefined : stackOf(ui.readerFont) }"
            @change="ui.setReaderFont(($event.target as HTMLSelectElement).value)"
          >
            <option value="" disabled>{{ t("reader.type.moreFonts") }}</option>
            <optgroup :label="t('settings.fonts.system')">
              <option
                v-for="f in SYSTEM_FONTS"
                :key="f"
                :value="`system:${f}`"
                :style="{ fontFamily: stackOf(`system:${f}`) }"
              >
                {{ f }}
              </option>
            </optgroup>
            <optgroup v-if="installed.length" :label="t('settings.fonts.downloaded')">
              <option
                v-for="f in installed"
                :key="f.id"
                :value="`downloaded:${f.id}`"
                :style="{ fontFamily: stackOf(`downloaded:${f.id}`) }"
              >
                {{ f.label }}
              </option>
            </optgroup>
          </select>
        </div>
      </div>

      <div v-for="s in sliders" :key="s.key" class="rs-pop-row rs-pop-stepped">
        <span class="rs-pop-label">{{ s.label() }}</span>
        <div class="rs-pop-control">
          <button
            type="button"
            class="rs-pop-step"
            :disabled="s.get() <= s.min"
            :aria-label="t('reader.type.stepDown', { name: s.label() })"
            @click="nudge(s, -1)"
          >−</button>
          <input
            type="range"
            class="rs-pop-slider"
            :min="s.min"
            :max="s.max"
            :step="s.step"
            :value="s.get()"
            :aria-label="s.label()"
            :aria-valuetext="`${s.get()}${s.unit}`"
            @input="s.set(Number(($event.target as HTMLInputElement).value))"
            @dblclick="s.set(s.reset)"
            @wheel="onSliderWheel(s, $event)"
          />
          <button
            type="button"
            class="rs-pop-step"
            :disabled="s.get() >= s.max"
            :aria-label="t('reader.type.stepUp', { name: s.label() })"
            @click="nudge(s, 1)"
          >+</button>
          <span
            class="rs-pop-value"
            :title="t('reader.type.reset')"
            @dblclick="s.set(s.reset)"
          >{{ s.get() }}{{ s.unit }}</span>
        </div>
      </div>

      <div class="rs-pop-row">
        <span class="rs-pop-label">{{ t("reader.type.theme") }}</span>
        <div class="rs-pop-control">
          <div class="rs-pop-seg" role="group" :aria-label="t('reader.type.theme')">
            <button
              v-for="th in THEMES"
              :key="th.value"
              type="button"
              :class="{ on: ui.theme === th.value }"
              :aria-pressed="ui.theme === th.value"
              @click="ui.setTheme(th.value)"
            >
              {{ t(`reader.type.theme_${th.key}`) }}
            </button>
          </div>
        </div>
      </div>

      <div class="rs-pop-row">
        <span class="rs-pop-label">{{ t("reader.type.readingTime") }}</span>
        <div class="rs-pop-control rs-pop-control-end">
          <input
            type="checkbox"
            class="s-toggle"
            :checked="ui.prefs.showReadingTime"
            :aria-label="t('reader.type.readingTime')"
            @change="ui.setPref({ showReadingTime: ($event.target as HTMLInputElement).checked })"
          />
        </div>
      </div>
    </div>

    <footer class="rs-pop-foot">
      <button
        type="button"
        class="rs-pop-reset"
        :disabled="ui.isReaderDefault"
        @click="ui.resetReader()"
      >
        {{ t("reader.type.reset") }}
      </button>
      <button type="button" class="rs-pop-more" @click="openMore">
        <Icon name="settings" :size="13" />
        {{ t("reader.type.more") }}
      </button>
    </footer>
  </div>
</template>
