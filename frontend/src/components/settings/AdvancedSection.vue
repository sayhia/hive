<script setup lang="ts">
// Advanced: storage, network, an experimental dedup flag, and the danger zone.
// AI provider config + assistant tuning moved to its own AI section (AiSection).
// Ported from papr's `AdvancedSection`.

import { useI18n } from "vue-i18n";
import StorageGroup from "./StorageGroup.vue";
import FontStorage from "./FontStorage.vue";
import NetworkGroup from "./NetworkGroup.vue";
import SettingFlag from "./SettingFlag.vue";
import DangerZone from "./DangerZone.vue";

const { t } = useI18n();
</script>

<template>
  <StorageGroup />
  <FontStorage />
  <NetworkGroup />
  <div class="settings-group">
    <h3 class="settings-group-title">{{ t("settings.advanced.experimental") }}</h3>
    <SettingFlag
      setting-key="dedup_enabled"
      :label="t('settings.advanced.dedup')"
      :desc="t('settings.advanced.dedupDesc')"
    />
  </div>
  <DangerZone />
</template>
