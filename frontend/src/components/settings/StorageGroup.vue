<script setup lang="ts">
// Storage: real database size, retention cleanup, vacuum. Ported from papr's
// `StorageGroup`. Toasts go through the global store.

import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useQuery, useQueryClient } from "@tanstack/vue-query";
import * as api from "../../api";
import { reportError, toast } from "../../stores/toasts";
import { formatBytes } from "./helpers";
import SettingsRow from "./SettingsRow.vue";
import SettingsSelect from "./SettingsSelect.vue";

const { t } = useI18n();
const qc = useQueryClient();

const stats = useQuery({
  queryKey: ["storage-stats"],
  queryFn: api.storageStats,
});

const retention = ref("forever");
const busy = ref(false);

onMounted(() => {
  api
    .getSetting("retention_days")
    .then((v) => {
      if (v) retention.value = v;
    })
    .catch(() => {});
});

async function cleanup(): Promise<void> {
  if (retention.value === "forever") {
    toast.show(t("settings.advanced.cleanupForever"));
    return;
  }
  busy.value = true;
  try {
    const n = await api.cleanupArticles(Number(retention.value));
    await qc.invalidateQueries();
    toast.show(
      n > 0
        ? t("settings.advanced.cleanupDone", { count: n })
        : t("settings.advanced.cleanupNone"),
    );
  } catch (e) {
    reportError(e);
  } finally {
    busy.value = false;
  }
}

async function vacuum(): Promise<void> {
  busy.value = true;
  try {
    await api.vacuumDb();
    await qc.invalidateQueries({ queryKey: ["storage-stats"] });
    toast.show(t("settings.advanced.vacuumDone"));
  } catch (e) {
    reportError(e);
  } finally {
    busy.value = false;
  }
}

function onRetentionChange(v: string): void {
  retention.value = v;
  api.setSetting("retention_days", v).catch(() => {});
}

const s = computed(() => stats.data.value);
const usageDesc = computed(() =>
  s.value
    ? t("settings.advanced.dbUsageDesc", {
        articles: s.value.articleCount,
        feeds: s.value.feedCount,
      })
    : t("settings.advanced.calculating"),
);
</script>

<template>
  <div class="settings-group">
    <h3 class="settings-group-title">{{ t("settings.advanced.storage") }}</h3>
    <SettingsRow :label="t('settings.advanced.dbUsage')" :desc="usageDesc">
      <span class="s-value">{{ s ? formatBytes(s.dbBytes) : "—" }}</span>
    </SettingsRow>
    <SettingsRow
      :label="t('settings.advanced.retention')"
      :desc="t('settings.advanced.retentionDesc')"
      v-slot="{ ariaLabel }"
    >
      <SettingsSelect
        :value="retention"
        :options="[
          { value: '30', label: t('settings.advanced.retention30') },
          { value: '90', label: t('settings.advanced.retention90') },
          { value: '180', label: t('settings.advanced.retention180') },
          { value: 'forever', label: t('settings.advanced.retentionForever') },
        ]"
        :aria-label="ariaLabel"
        @change="onRetentionChange($event)"
      />
    </SettingsRow>
    <SettingsRow
      :label="t('settings.advanced.cleanupNow')"
      :desc="t('settings.advanced.cleanupNowDesc')"
    >
      <button class="s-btn" @click="cleanup" :disabled="busy">
        {{ t("settings.advanced.cleanup") }}
      </button>
    </SettingsRow>
    <SettingsRow
      :label="t('settings.advanced.vacuum')"
      :desc="t('settings.advanced.vacuumDesc')"
    >
      <button class="s-btn" @click="vacuum" :disabled="busy">
        {{ t("settings.advanced.compress") }}
      </button>
    </SettingsRow>
  </div>
</template>
