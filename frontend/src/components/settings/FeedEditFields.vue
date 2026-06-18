<script setup lang="ts">
// The per-feed settings form, shared by the two places a feed is edited: the
// Settings → Subscriptions inline panel and the home-page FeedEditDialog. One
// implementation, so the two entry points can't drift apart.
//
// Edits go into a local draft and are only sent when Save is pressed. An earlier
// version committed on blur/change, which read wrong inside a dialog (a dialog
// implies confirm/cancel) and silently dropped an edit when the panel was closed
// with Escape — removing a focused input does not reliably fire `blur`, so the
// typed value was lost. Save issues only the calls whose field actually changed.

import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useQuery, useQueryClient } from "@tanstack/vue-query";
import * as api from "../../api";
import { reportError, toast } from "../../stores/toasts";
import type { Feed } from "../../types";
import SettingsSelect from "./SettingsSelect.vue";

const props = defineProps<{ feed: Feed }>();
const emit = defineEmits<{ (e: "saved"): void }>();

const { t } = useI18n();
const qc = useQueryClient();

// Per-feed refresh interval. "default" ⇒ null (follow the global setting),
// "off" ⇒ the 525600-minute sentinel, otherwise the literal minute count.
const REFRESH_OFF = 525600;

interface Draft {
  title: string;
  feedUrl: string;
  folder: string;
  openMode: string;
  interval: string;
  autoTranslate: string;
}

/** The feed's stored values in draft (string-keyed) form — the diff baseline. */
function draftFrom(f: Feed): Draft {
  return {
    title: f.title,
    feedUrl: f.feedUrl,
    folder: f.folderId == null ? "none" : String(f.folderId),
    openMode: f.openMode ?? "default",
    interval:
      f.refreshIntervalMin == null
        ? "default"
        : f.refreshIntervalMin >= REFRESH_OFF
          ? "off"
          : String(f.refreshIntervalMin),
    autoTranslate:
      f.autoTranslate == null ? "default" : f.autoTranslate === 1 ? "on" : "off",
  };
}

const draft = reactive<Draft>(draftFrom(props.feed));

const saving = ref(false);
const dirty = computed(() => {
  const base = draftFrom(props.feed);
  return (Object.keys(base) as (keyof Draft)[]).some((k) => draft[k] !== base[k]);
});
// Empty name / URL are rejected backend-side; block them here so Save isn't a
// button that only ever errors.
const canSave = computed(
  () => dirty.value && !saving.value && !!draft.title.trim() && !!draft.feedUrl.trim(),
);

// Re-seed the draft when the stored feed changes — but never over unsaved edits.
// The feeds query hands us a fresh object on every refetch (invalidation, window
// focus), so an unguarded re-seed would wipe what the user is typing. After a
// successful save the draft already equals the new values, so this is a no-op.
watch(
  () => props.feed,
  (f) => {
    if (!dirty.value) Object.assign(draft, draftFrom(f));
  },
);

const intervalOptions = [
  { value: "default", label: t("settings.subscriptions.refreshDefault") },
  { value: "15", label: t("settings.subscriptions.refresh15m") },
  { value: "30", label: t("settings.subscriptions.refresh30m") },
  { value: "60", label: t("settings.subscriptions.refresh1h") },
  { value: "360", label: t("settings.subscriptions.refresh6h") },
  { value: "720", label: t("settings.subscriptions.refresh12h") },
  { value: "1440", label: t("settings.subscriptions.refresh1d") },
  { value: "off", label: t("settings.subscriptions.refreshOff") },
];
const openModeOptions = [
  { value: "default", label: t("settings.subscriptions.openDefault") },
  { value: "reader", label: t("settings.subscriptions.openReader") },
  { value: "extracted", label: t("settings.subscriptions.openExtracted") },
  { value: "web", label: t("settings.subscriptions.openWeb") },
];
// null follows the global translate_auto setting; 1 forces on, 0 forces off.
const autoTranslateOptions = [
  { value: "default", label: t("reader.autoDefault") },
  { value: "on", label: t("reader.autoOn") },
  { value: "off", label: t("reader.autoOff") },
];

const folders = useQuery({ queryKey: ["folders"], queryFn: api.listFolders });
const folderOptions = computed(() => [
  { value: "none", label: t("settings.subscriptions.folderNone") },
  ...(folders.data.value ?? []).map((fo) => ({ value: String(fo.id), label: fo.name })),
]);

async function save(): Promise<void> {
  if (!canSave.value) return;
  const base = draftFrom(props.feed);
  const id = props.feed.id;
  saving.value = true;
  try {
    // URL first: it is the one field that can be rejected (empty, or already
    // subscribed), so failing here stops before the other edits are applied.
    if (draft.feedUrl !== base.feedUrl) await api.setFeedUrl(id, draft.feedUrl.trim());
    if (draft.title !== base.title) await api.renameFeed(id, draft.title.trim());
    if (draft.folder !== base.folder) {
      await api.moveFeed(id, draft.folder === "none" ? null : Number(draft.folder));
      qc.invalidateQueries({ queryKey: ["folders"] });
    }
    if (draft.openMode !== base.openMode) {
      await api.setFeedOpenMode(
        id,
        draft.openMode === "default" ? null : (draft.openMode as "reader" | "extracted" | "web"),
      );
    }
    if (draft.interval !== base.interval) {
      await api.setFeedRefreshInterval(
        id,
        draft.interval === "default" ? null : draft.interval === "off" ? REFRESH_OFF : Number(draft.interval),
      );
    }
    if (draft.autoTranslate !== base.autoTranslate) {
      await api.setFeedAutoTranslate(
        id,
        draft.autoTranslate === "default" ? null : draft.autoTranslate === "on" ? 1 : 0,
      );
    }
    await qc.invalidateQueries({ queryKey: ["feeds"] });
    toast.show(t("settings.subscriptions.saved"));
    emit("saved");
  } catch (e) {
    reportError(e);
  } finally {
    saving.value = false;
  }
}

function revert(): void {
  Object.assign(draft, draftFrom(props.feed));
}
// Enter in a text field commits the form rather than doing nothing.
function onEnter(): void {
  if (canSave.value) save();
}
</script>

<template>
  <div class="s-feed-edit">
    <label class="s-feed-field">
      <span>{{ t("settings.subscriptions.feedName") }}</span>
      <input class="s-text-input" type="text" v-model="draft.title" @keydown.enter="onEnter" />
    </label>
    <label class="s-feed-field">
      <span>{{ t("settings.subscriptions.feedUrl") }}</span>
      <input
        class="s-text-input"
        type="text"
        v-model="draft.feedUrl"
        :spellcheck="false"
        autocapitalize="off"
        autocorrect="off"
        @keydown.enter="onEnter"
      />
      <small class="s-feed-hint">{{ t("settings.subscriptions.feedUrlDesc") }}</small>
    </label>
    <div class="s-feed-field-row">
      <label class="s-feed-field">
        <span>{{ t("settings.subscriptions.folder") }}</span>
        <SettingsSelect
          :value="draft.folder"
          :options="folderOptions"
          @change="draft.folder = $event"
        />
      </label>
      <label class="s-feed-field">
        <span>{{ t("settings.subscriptions.openMode") }}</span>
        <SettingsSelect
          :value="draft.openMode"
          :options="openModeOptions"
          @change="draft.openMode = $event"
        />
      </label>
    </div>
    <div class="s-feed-field-row">
      <label class="s-feed-field">
        <span>{{ t("settings.subscriptions.refreshInterval") }}</span>
        <SettingsSelect
          :value="draft.interval"
          :options="intervalOptions"
          @change="draft.interval = $event"
        />
      </label>
      <label class="s-feed-field">
        <span>{{ t("reader.autoTranslateFeed") }}</span>
        <SettingsSelect
          :value="draft.autoTranslate"
          :options="autoTranslateOptions"
          @change="draft.autoTranslate = $event"
        />
      </label>
    </div>
    <!-- Revert stays mounted (disabled when clean) so the row doesn't shift the
         Save button sideways the moment you start typing. -->
    <div class="s-feed-actions">
      <button class="s-btn" :disabled="!dirty || saving" @click="revert">
        {{ t("settings.subscriptions.revert") }}
      </button>
      <button class="s-btn primary" :disabled="!canSave" @click="save">
        {{ saving ? t("settings.subscriptions.saving") : t("common.save") }}
      </button>
    </div>
  </div>
</template>
