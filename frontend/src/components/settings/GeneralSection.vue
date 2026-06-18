<script setup lang="ts">
// General: feed-refresh cadence, read behaviour, startup. Ported from papr's
// `GeneralSection` (+ its `LaunchAtLogin` sub-row). papr's autostart used the
// Tauri autostart plugin; hive has no such plugin, so "Launch at login" is a
// stub persisted to the `autostart_enabled` backend setting.

import { onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import * as api from "../../api";
import { useUi } from "../../stores/ui";
import { clampSetting, OFF_INTERVAL } from "./helpers";
import SettingsRow from "./SettingsRow.vue";
import SettingsToggle from "./SettingsToggle.vue";
import SettingsSelect from "./SettingsSelect.vue";
import SettingsSlider from "./SettingsSlider.vue";
import type { StartupView } from "../../stores/ui";

const { t } = useI18n();
const ui = useUi();

const autoRefresh = ref(true);
const refreshMins = ref(30);

// Open-at-login — backed by a persisted setting (autostart plugin stub).
const launchAtLogin = ref(false);

onMounted(() => {
  api
    .getSetting("refresh_interval_min")
    .then((v) => {
      const n = v ? Number(v) : 30;
      // A finite interval at/above the "off" sentinel means auto-refresh is
      // disabled; anything else is a live interval clamped to the slider's
      // 5–120 range (a stale larger value would otherwise show e.g. "150
      // minutes" with the thumb stuck at 120, and a NaN would read "NaN").
      if (Number.isFinite(n) && n >= 100000) autoRefresh.value = false;
      else {
        autoRefresh.value = true;
        refreshMins.value = clampSetting(v ?? null, 30, 5, 120);
      }
    })
    .catch(() => {});

  api
    .getSetting("autostart_enabled")
    .then((v) => {
      if (v != null && v !== "") launchAtLogin.value = v === "1";
    })
    .catch(() => {});
});

function writeInterval(auto: boolean, mins: number): void {
  api
    .setSetting("refresh_interval_min", auto ? String(mins) : String(OFF_INTERVAL))
    .catch(() => {});
}

function onAutoRefresh(v: boolean): void {
  autoRefresh.value = v;
  writeInterval(v, refreshMins.value);
}

function onLaunchAtLogin(v: boolean): void {
  launchAtLogin.value = v;
  api.setSetting("autostart_enabled", v ? "1" : "0").catch(() => {});
}
</script>

<template>
  <div class="settings-group">
    <h3 class="settings-group-title">{{ t("settings.general.refresh") }}</h3>
    <SettingsRow
      :label="t('settings.general.autoRefresh')"
      :desc="t('settings.general.autoRefreshDesc')"
      v-slot="{ ariaLabel }"
    >
      <SettingsToggle :checked="autoRefresh" :aria-label="ariaLabel" @change="onAutoRefresh" />
    </SettingsRow>
    <SettingsRow
      v-if="autoRefresh"
      :label="t('settings.general.refreshInterval')"
      :desc="t('settings.general.refreshIntervalDesc')"
      v-slot="{ ariaLabel }"
    >
      <SettingsSlider
        :value="refreshMins"
        :min="5"
        :max="120"
        :step="5"
        :unit="t('settings.general.minutesUnit')"
        :aria-label="ariaLabel"
        @change="refreshMins = $event"
        @commit="writeInterval(true, $event)"
      />
    </SettingsRow>
  </div>

  <div class="settings-group">
    <h3 class="settings-group-title">{{ t("settings.general.readBehavior") }}</h3>
    <SettingsRow :label="t('settings.general.markReadOnOpen')" v-slot="{ ariaLabel }">
      <SettingsToggle
        :checked="ui.prefs.markReadOnOpen"
        :aria-label="ariaLabel"
        @change="ui.setPref({ markReadOnOpen: $event })"
      />
    </SettingsRow>
    <SettingsRow
      :label="t('settings.general.markReadOnScroll')"
      :desc="t('settings.general.markReadOnScrollDesc')"
      v-slot="{ ariaLabel }"
    >
      <SettingsToggle
        :checked="ui.prefs.markReadOnScroll"
        :aria-label="ariaLabel"
        @change="ui.setPref({ markReadOnScroll: $event })"
      />
    </SettingsRow>
  </div>

  <div class="settings-group">
    <h3 class="settings-group-title">{{ t("settings.general.startup") }}</h3>
    <SettingsRow
      :label="t('settings.general.launchAtLogin')"
      :desc="t('settings.general.launchAtLoginDesc')"
      v-slot="{ ariaLabel }"
    >
      <SettingsToggle :checked="launchAtLogin" :aria-label="ariaLabel" @change="onLaunchAtLogin" />
    </SettingsRow>
    <SettingsRow
      :label="t('settings.general.startupView')"
      :desc="t('settings.general.startupViewDesc')"
      v-slot="{ ariaLabel }"
    >
      <SettingsSelect
        :value="ui.prefs.startupView"
        :options="[
          { value: 'all', label: t('settings.general.startupAll') },
          { value: 'unread', label: t('smart.unread') },
          { value: 'starred', label: t('smart.starred') },
          { value: 'last', label: t('settings.general.startupLast') },
        ]"
        :aria-label="ariaLabel"
        @change="ui.setPref({ startupView: $event as StartupView })"
      />
    </SettingsRow>
    <SettingsRow :label="t('settings.general.hideReadOnStartup')" v-slot="{ ariaLabel }">
      <SettingsToggle
        :checked="ui.prefs.hideReadOnStartup"
        :aria-label="ariaLabel"
        @change="ui.setPref({ hideReadOnStartup: $event })"
      />
    </SettingsRow>
  </div>
</template>
