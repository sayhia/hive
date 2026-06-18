<script setup lang="ts">
// Notifications: system-notification flags + a night Do-Not-Disturb flag, each
// a persisted backend setting. Ported from papr's `NotificationsSection`.

import { useI18n } from "vue-i18n";
import SettingFlag from "./SettingFlag.vue";

const { t } = useI18n();
</script>

<template>
  <div class="settings-group">
    <h3 class="settings-group-title">{{ t("settings.notifications.system") }}</h3>
    <SettingFlag
      setting-key="notify_enabled"
      :fallback="true"
      :label="t('settings.notifications.allow')"
      :desc="t('settings.notifications.allowDesc')"
    />
    <SettingFlag
      setting-key="notify_badge"
      :fallback="true"
      :label="t('settings.notifications.badge')"
      :desc="t('settings.notifications.badgeDesc')"
    />
    <SettingFlag
      setting-key="notify_sound"
      :label="t('settings.notifications.sound')"
      :desc="t('settings.notifications.soundDesc')"
    />
  </div>
  <div class="settings-group">
    <h3 class="settings-group-title">{{ t("settings.notifications.dnd") }}</h3>
    <SettingFlag
      setting-key="notify_dnd_night"
      :label="t('settings.notifications.dndNight')"
      :desc="t('settings.notifications.dndNightDesc')"
    />
  </div>
</template>
