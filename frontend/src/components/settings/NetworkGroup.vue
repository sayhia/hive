<script setup lang="ts">
// Network: proxy, fetch concurrency, request timeout. Ported from papr's
// `NetworkGroup`. Toasts go through the global store. Stored slider values are
// clamped to each slider's range so a stale / corrupt value can't show a NaN or
// out-of-range readout.

import { onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import * as api from "../../api";
import { reportError, toast } from "../../stores/toasts";
import { clampSetting } from "./helpers";
import SettingsRow from "./SettingsRow.vue";
import SettingsSelect from "./SettingsSelect.vue";
import SettingsSlider from "./SettingsSlider.vue";

const { t } = useI18n();

const proxy = ref("system");
const customProxy = ref("");
const concurrency = ref(6);
const timeoutSec = ref(30);

onMounted(() => {
  Promise.all([
    api.getSetting("net_proxy"),
    api.getSetting("net_concurrency"),
    api.getSetting("net_timeout_sec"),
  ])
    .then(([p, c, to]) => {
      if (p === "system" || p === "none") proxy.value = p;
      else if (p) {
        proxy.value = "custom";
        customProxy.value = p;
      }
      // Clamp to each slider's range (concurrency 1–16, timeout 5–120) so a
      // stale or corrupt stored value can't show a NaN / out-of-range readout.
      if (c) concurrency.value = clampSetting(c, 6, 1, 16);
      if (to) timeoutSec.value = clampSetting(to, 30, 5, 120);
    })
    .catch(() => {});
});

function saveProxy(mode: string, custom: string): void {
  const value = mode === "custom" ? custom : mode;
  api
    .setSetting("net_proxy", value)
    .then(() => api.applyNetworkSettings())
    .then(() => toast.show(t("settings.advanced.proxyApplied")))
    .catch((e) => reportError(e));
}

function onProxyChange(v: string): void {
  proxy.value = v;
  if (v !== "custom") saveProxy(v, "");
}

function onConcurrencyCommit(v: number): void {
  api.setSetting("net_concurrency", String(v)).catch(() => {});
}

function onTimeoutCommit(v: number): void {
  api
    .setSetting("net_timeout_sec", String(v))
    .then(() => api.applyNetworkSettings())
    .catch(() => {});
}
</script>

<template>
  <div class="settings-group">
    <h3 class="settings-group-title">{{ t("settings.advanced.network") }}</h3>
    <SettingsRow :label="t('settings.advanced.proxy')" v-slot="{ ariaLabel }">
      <SettingsSelect
        :value="proxy"
        :options="[
          { value: 'system', label: t('settings.advanced.proxySystem') },
          { value: 'none', label: t('settings.advanced.proxyNone') },
          { value: 'custom', label: t('settings.advanced.proxyCustom') },
        ]"
        :aria-label="ariaLabel"
        @change="onProxyChange($event)"
      />
    </SettingsRow>
    <SettingsRow
      v-if="proxy === 'custom'"
      :label="t('settings.advanced.proxyAddress')"
      :desc="t('settings.advanced.proxyAddressDesc')"
      v-slot="{ ariaLabel }"
    >
      <input
        class="s-text-input"
        v-model="customProxy"
        placeholder="http://host:port"
        :aria-label="ariaLabel"
        @blur="saveProxy('custom', customProxy)"
      />
    </SettingsRow>
    <SettingsRow
      :label="t('settings.advanced.concurrency')"
      :desc="t('settings.advanced.concurrencyDesc')"
      v-slot="{ ariaLabel }"
    >
      <SettingsSlider
        :value="concurrency"
        :min="1"
        :max="16"
        :aria-label="ariaLabel"
        @change="concurrency = $event"
        @commit="onConcurrencyCommit"
      />
    </SettingsRow>
    <SettingsRow :label="t('settings.advanced.timeout')" v-slot="{ ariaLabel }">
      <SettingsSlider
        :value="timeoutSec"
        :min="5"
        :max="120"
        :step="5"
        :unit="t('settings.advanced.secondsUnit')"
        :aria-label="ariaLabel"
        @change="timeoutSec = $event"
        @commit="onTimeoutCommit"
      />
    </SettingsRow>
  </div>
</template>
