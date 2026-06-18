<script setup lang="ts">
// The add/edit form for a single filter rule, with a debounced dry-run preview
// and a confirmation before a destructive `skip` backfill. Ported from papr's
// `RuleEditor`. Toasts go through the global store.

import { onUnmounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import * as api from "../../api";
import { reportError, toast } from "../../stores/toasts";
import type { Feed, Rule, RuleAction, RuleField, RulePreview } from "../../types";
import SettingsSelect from "./SettingsSelect.vue";
import ConfirmDialog from "../ConfirmDialog.vue";

const props = defineProps<{
  /** The rule being edited, or `null` for the add form. */
  rule: Rule | null;
  feeds: Feed[];
}>();

const emit = defineEmits<{
  (e: "cancel"): void;
  (e: "saved"): void;
}>();

const { t } = useI18n();

const name = ref(props.rule?.name ?? "");
const query = ref(props.rule?.query ?? "");
const field = ref<RuleField>(props.rule?.field ?? "title");
const action = ref<RuleAction>(props.rule?.action ?? "skip");
const scope = ref(props.rule?.feedId == null ? "" : String(props.rule.feedId));
const busy = ref(false);
const preview = ref<RulePreview | null>(null);
const previewing = ref(false);
// When a `skip` rule would delete existing matches, hold that count here to pop
// a confirmation before the destructive backfill runs.
const pendingDelete = ref<number | null>(null);

// Debounced dry-run: count matching stored articles as the draft changes.
let timer: number | undefined;
let cancelled = false;
function clearPending(): void {
  cancelled = true;
  window.clearTimeout(timer);
}
watch(
  [query, field, scope],
  () => {
    clearPending();
    const q = query.value.trim();
    if (!q) {
      preview.value = null;
      return;
    }
    previewing.value = true;
    const feedId = scope.value === "" ? null : Number(scope.value);
    // `cancelled` guards against a stale response: a request started before the
    // draft changed could otherwise resolve last and overwrite the preview for
    // the current draft.
    cancelled = false;
    timer = window.setTimeout(() => {
      api
        .previewRule(feedId, field.value, q)
        .then((r) => {
          if (!cancelled) preview.value = r;
        })
        .catch(() => {
          if (!cancelled) preview.value = null;
        })
        .finally(() => {
          if (!cancelled) previewing.value = false;
        });
    }, 400);
  },
  { immediate: true },
);
onUnmounted(clearPending);

// A rule backfills the existing backlog only when it's active: new rules start
// enabled, and an existing rule keeps its current enabled state (the editor has
// no enabled toggle — that lives in the rule list).
const willApply = props.rule ? props.rule.enabled : true;

// Save the rule, then backfill the existing articles it matches. Split out from
// `save` so the `skip` confirmation can resume here after the user agrees.
async function persist(): Promise<void> {
  busy.value = true;
  const feedId = scope.value === "" ? null : Number(scope.value);
  const q = query.value.trim();
  try {
    if (props.rule) {
      await api.updateRule(props.rule.id, name.value, props.rule.enabled, feedId, field.value, q, action.value);
    } else {
      await api.createRule(name.value, feedId, field.value, q, action.value);
    }
    const applied = willApply
      ? await api.applyRuleToExisting(feedId, field.value, q, action.value)
      : 0;
    toast.show(
      applied > 0
        ? t("settings.filters.appliedExisting", { count: applied })
        : t("settings.filters.saved"),
    );
    emit("saved");
  } catch (e) {
    reportError(e);
    busy.value = false;
  }
}

async function save(): Promise<void> {
  const q = query.value.trim();
  if (!q) {
    toast.show(t("settings.filters.needQuery"));
    return;
  }
  // `skip` deletes the existing articles it matches — confirm first, showing
  // the exact count fetched fresh (not the debounced preview, which may lag).
  if (willApply && action.value === "skip") {
    busy.value = true;
    const feedId = scope.value === "" ? null : Number(scope.value);
    try {
      const p = await api.previewRule(feedId, field.value, q);
      if (p.count > 0) {
        pendingDelete.value = p.count;
        return;
      }
    } catch (e) {
      // Never fall through to the destructive backfill when we couldn't confirm
      // how many articles it would delete.
      reportError(e);
      return;
    } finally {
      busy.value = false;
    }
  }
  await persist();
}

// hive's ConfirmDialog does not auto-close on confirm, so dismiss it here.
function onSkipConfirm(): void {
  pendingDelete.value = null;
  persist();
}
</script>

<template>
  <div class="rule-card">
    <input
      class="rule-input"
      v-model="name"
      :placeholder="t('settings.filters.namePlaceholder')"
    />
    <input
      class="rule-input"
      v-model="query"
      :placeholder="t('settings.filters.queryPlaceholder')"
    />
    <div class="rule-fields">
      <label>
        {{ t("settings.filters.matchIn") }}
        <SettingsSelect
          :value="field"
          :options="[
            { value: 'title', label: t('settings.filters.field.title') },
            { value: 'author', label: t('settings.filters.field.author') },
            { value: 'content', label: t('settings.filters.field.content') },
            { value: 'any', label: t('settings.filters.field.any') },
          ]"
          @change="field = $event as RuleField"
        />
      </label>
      <label>
        {{ t("settings.filters.thenLabel") }}
        <SettingsSelect
          :value="action"
          :options="[
            { value: 'skip', label: t('settings.filters.action.skip') },
            { value: 'read', label: t('settings.filters.action.read') },
            { value: 'star', label: t('settings.filters.action.star') },
          ]"
          @change="action = $event as RuleAction"
        />
      </label>
      <label>
        {{ t("settings.filters.scopeLabel") }}
        <SettingsSelect
          :value="scope"
          :options="[
            { value: '', label: t('settings.filters.allFeeds') },
            ...feeds.map((f) => ({ value: String(f.id), label: f.title })),
          ]"
          @change="scope = $event"
        />
      </label>
    </div>
    <div v-if="query.trim()" class="rule-preview">
      <span class="rule-preview-count">
        {{
          previewing && !preview
            ? t("settings.filters.preview.checking")
            : t("settings.filters.preview.count", { count: preview?.count ?? 0 })
        }}
      </span>
      <ul v-if="preview && preview.samples.length > 0" class="rule-preview-samples">
        <li v-for="(s, i) in preview.samples" :key="i">{{ s }}</li>
      </ul>
    </div>
    <div class="rule-card-actions">
      <button class="s-btn" @click="emit('cancel')" :disabled="busy">
        {{ t("common.cancel") }}
      </button>
      <button class="s-btn primary" @click="save" :disabled="busy">
        {{ t("common.save") }}
      </button>
    </div>

    <ConfirmDialog
      v-if="pendingDelete != null"
      :title="t('settings.filters.confirmSkipTitle')"
      :message="t('settings.filters.confirmSkipMessage', { count: pendingDelete })"
      :confirm-label="t('settings.filters.confirmSkipConfirm')"
      @confirm="onSkipConfirm"
      @cancel="pendingDelete = null"
    />
  </div>
</template>
