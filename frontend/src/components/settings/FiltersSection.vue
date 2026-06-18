<script setup lang="ts">
// Filters: keyword rules run on every incoming article (and, on save, on
// matching existing articles). List + inline add/edit. Ported from papr's
// `FiltersSection`. Toasts go through the global store; the rule-list query is
// invalidated on change, and saving also refreshes the article-bearing caches
// the backfill touched.

import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useQuery, useQueryClient } from "@tanstack/vue-query";
import * as api from "../../api";
import { reportError, toast } from "../../stores/toasts";
import type { Feed, Rule } from "../../types";
import Icon from "../Icon.vue";
import SettingsToggle from "./SettingsToggle.vue";
import RuleEditor from "./RuleEditor.vue";

const props = defineProps<{
  feeds: Feed[];
}>();

const { t } = useI18n();
const qc = useQueryClient();

const rules = useQuery({ queryKey: ["rules"], queryFn: api.listRules });
// listRules degrades to [] on failure (indistinguishable from "no rules yet"),
// so surface a genuine load error instead of silently showing an empty list.
watch(
  () => rules.error.value,
  (e) => {
    if (e) reportError(e);
  },
);

// `null` = not editing, "new" = the add form, a Rule = editing that rule.
const editing = ref<Rule | "new" | null>(null);

const list = computed(() => rules.data.value ?? []);
const editingRuleId = computed(() =>
  editing.value && editing.value !== "new" ? editing.value.id : null,
);

function refresh(): void {
  qc.invalidateQueries({ queryKey: ["rules"] });
}

// Saving a rule also backfills existing articles (star / read / skip), so
// refresh the article list, sidebar counts and per-feed counts the backfill
// touched — not just the rule list. The editor shows the "saved"/"applied"
// toast itself, since only it knows how many articles were affected.
function afterSaved(): void {
  editing.value = null;
  refresh();
  qc.invalidateQueries({ queryKey: ["articles"] });
  // Sidebar smart-view counts live under ["smartCounts"] (App.vue mirrors the
  // legacy ["counts"] key); invalidate both so a rule's backfill (read/star/
  // skip) refreshes the sidebar badges, not just the article list.
  qc.invalidateQueries({ queryKey: ["smartCounts"] });
  qc.invalidateQueries({ queryKey: ["counts"] });
  qc.invalidateQueries({ queryKey: ["feeds"] });
}

function feedName(id: number | null): string {
  if (id == null) return t("settings.filters.allFeeds");
  return props.feeds.find((f) => f.id === id)?.title ?? t("settings.filters.allFeeds");
}

function toggle(r: Rule): void {
  api
    .updateRule(r.id, r.name, !r.enabled, r.feedId, r.field, r.query, r.action)
    .then(refresh)
    .catch((e) => reportError(e));
}

function remove(r: Rule): void {
  api
    .deleteRule(r.id)
    .then(() => {
      refresh();
      toast.show(t("settings.filters.deleted"));
    })
    .catch((e) => reportError(e));
}

function summary(r: Rule): string {
  return [
    t(`settings.filters.action.${r.action}`),
    "·",
    t(`settings.filters.field.${r.field}`),
    `“${r.query}”`,
    "·",
    feedName(r.feedId),
  ].join(" ");
}
</script>

<template>
  <div class="settings-group" :style="{ marginBottom: '18px' }">
    <h3 class="settings-group-title">{{ t("settings.filters.title") }}</h3>
    <p class="settings-group-desc">{{ t("settings.filters.intro") }}</p>
    <button
      v-if="editing !== 'new'"
      class="s-btn primary"
      :style="{ marginTop: '10px' }"
      @click="editing = 'new'"
    >
      <Icon name="plus" :size="12" /> {{ t("settings.filters.newRule") }}
    </button>
  </div>

  <RuleEditor
    v-if="editing === 'new'"
    :rule="null"
    :feeds="feeds"
    @cancel="editing = null"
    @saved="afterSaved"
  />

  <div class="settings-group">
    <div
      v-if="list.length === 0 && editing !== 'new'"
      :style="{ padding: '16px 4px', fontSize: '13px', color: 'var(--muted)' }"
    >
      {{ t("settings.filters.empty") }}
    </div>
    <template v-for="r in list" :key="r.id">
      <RuleEditor
        v-if="editingRuleId === r.id"
        :rule="r"
        :feeds="feeds"
        @cancel="editing = null"
        @saved="afterSaved"
      />
      <div v-else class="rule-row">
        <SettingsToggle :checked="r.enabled" @change="toggle(r)" />
        <div class="rule-text" :style="{ opacity: r.enabled ? 1 : 0.5 }">
          <div class="rule-name">{{ r.name || t("settings.filters.untitled") }}</div>
          <div class="rule-summary">{{ summary(r) }}</div>
        </div>
        <div class="actions">
          <button class="icon-btn" :title="t('common.rename')" @click="editing = r">
            <Icon name="settings" :size="13" />
          </button>
          <button class="icon-btn" :title="t('common.delete')" @click="remove(r)">
            <Icon name="trash" :size="13" />
          </button>
        </div>
      </div>
    </template>
  </div>
</template>
