<script setup lang="ts">
// Sync: connect to a self-hosted GReader server (FreshRSS / Miniflux), sync
// now, disconnect; plus a list of unavailable third-party services. Ported from
// papr's `SyncSection`. Toasts go through the global store; the post-sync cache
// refresh inlines papr's `actions.refreshAfterBulk()`.

import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useQuery, useQueryClient } from "@tanstack/vue-query";
import * as api from "../../api";
import type { GReaderProvider } from "../../api";
import { reportError, toast } from "../../stores/toasts";
import Icon from "../Icon.vue";

const { t } = useI18n();
const qc = useQueryClient();

const status = useQuery({
  queryKey: ["freshrss-status"],
  queryFn: api.freshrssStatus,
});

const provider = ref<GReaderProvider>("freshrss");
const url = ref("");
const user = ref("");
const pass = ref("");
const busy = ref(false);

const connected = computed(() => status.data.value?.connected ?? false);
const connectedProvider = computed<GReaderProvider>(
  () => status.data.value?.provider ?? "freshrss",
);
const providerLabel = (p: GReaderProvider): string =>
  p === "miniflux" ? "Miniflux" : "FreshRSS";

const canConnect = computed(
  () => !busy.value && url.value.trim() !== "" && user.value.trim() !== "",
);

async function connect(): Promise<void> {
  if (!url.value.trim() || !user.value.trim()) return;
  busy.value = true;
  try {
    await api.freshrssConnect(url.value.trim(), user.value.trim(), pass.value, provider.value);
    await qc.invalidateQueries({ queryKey: ["freshrss-status"] });
    toast.show(t("settings.sync.connected"));
    pass.value = "";
  } catch (e) {
    reportError(e);
  } finally {
    busy.value = false;
  }
}

async function disconnect(): Promise<void> {
  busy.value = true;
  try {
    await api.freshrssDisconnect();
    await qc.invalidateQueries({ queryKey: ["freshrss-status"] });
    toast.show(t("settings.sync.disconnected"));
  } catch (e) {
    reportError(e);
  } finally {
    busy.value = false;
  }
}

// Inlined equivalent of papr's `actions.refreshAfterBulk()`.
function refreshAfterBulk(): void {
  for (const key of [
    ["smartCounts"], ["feeds"], ["folders"], ["tags"],
    ["articles"], ["article"], ["search"], ["cp-search"],
  ]) {
    qc.invalidateQueries({ queryKey: key });
  }
}

async function syncNow(): Promise<void> {
  busy.value = true;
  try {
    const n = await api.freshrssSync();
    // Sync reconciles read/starred state and may add feeds — refresh the
    // article-bearing caches, not unrelated ones (AI summaries, settings).
    refreshAfterBulk();
    toast.show(t("settings.sync.syncDone", { count: n }));
  } catch (e) {
    reportError(e);
  } finally {
    busy.value = false;
  }
}

const unavailable = [
  { name: "Feedly", initial: "F", color: "#2BB24C", reason: t("settings.sync.reasonOauth") },
  { name: "Inoreader", initial: "I", color: "#1976D2", reason: t("settings.sync.reasonOauth") },
  { name: "iCloud", initial: "☁", color: "#0089E0", reason: t("settings.sync.reasonEntitlements") },
];
</script>

<template>
  <div class="settings-group">
    <h3 class="settings-group-title">{{ t("settings.sync.greader") }}</h3>

    <template v-if="connected">
      <div class="s-service">
        <div
          class="logo"
          :style="{ background: connectedProvider === 'miniflux' ? '#1F7AEC' : '#4A4A4A' }"
        >
          {{ connectedProvider === "miniflux" ? "M" : "⚡" }}
        </div>
        <div class="info">
          <div class="title">{{ providerLabel(connectedProvider) }}</div>
          <div class="desc">{{ status.data.value?.url }}</div>
        </div>
        <span class="status on">{{ t("settings.sync.statusConnected") }}</span>
      </div>
      <div :style="{ display: 'flex', gap: '8px', marginTop: '12px' }">
        <button class="s-btn primary" @click="syncNow" :disabled="busy">
          <Icon name="refresh" :size="12" />
          {{ busy ? t("settings.sync.syncing") : t("settings.sync.syncNow") }}
        </button>
        <button class="s-btn" @click="disconnect" :disabled="busy">
          {{ t("settings.sync.disconnect") }}
        </button>
      </div>
      <p :style="{ fontSize: '12px', color: 'var(--muted)', marginTop: '12px', lineHeight: 1.5 }">
        {{ t("settings.sync.syncHint") }}
      </p>
    </template>

    <template v-else>
      <p class="modal-hint" :style="{ marginBottom: '14px' }">
        {{ t("settings.sync.connectHint") }}
      </p>
      <div :style="{ display: 'flex', flexDirection: 'column', gap: '8px' }">
        <select
          class="modal-input"
          :style="{ margin: 0 }"
          :value="provider"
          @change="provider = ($event.target as HTMLSelectElement).value as GReaderProvider"
          :aria-label="t('settings.sync.provider')"
        >
          <option value="freshrss">{{ t("settings.sync.providerFreshrss") }}</option>
          <option value="miniflux">{{ t("settings.sync.providerMiniflux") }}</option>
        </select>
        <input
          class="modal-input"
          :style="{ margin: 0 }"
          :placeholder="t('settings.sync.serverPlaceholder')"
          v-model="url"
        />
        <input
          class="modal-input"
          :style="{ margin: 0 }"
          :placeholder="t('settings.sync.userPlaceholder')"
          v-model="user"
        />
        <input
          class="modal-input"
          :style="{ margin: 0 }"
          type="password"
          :placeholder="
            provider === 'miniflux'
              ? t('settings.sync.appPassPlaceholder')
              : t('settings.sync.passPlaceholder')
          "
          v-model="pass"
        />
        <p
          v-if="provider === 'miniflux'"
          :style="{ fontSize: '12px', color: 'var(--muted)', margin: '2px 0 0', lineHeight: 1.5 }"
        >
          {{ t("settings.sync.minifluxPassHint") }}
        </p>
        <div>
          <button class="s-btn primary" @click="connect" :disabled="!canConnect">
            {{ busy ? t("settings.sync.connecting") : t("settings.sync.connect") }}
          </button>
        </div>
      </div>
    </template>
  </div>

  <div class="settings-group">
    <h3 class="settings-group-title">{{ t("settings.sync.otherServices") }}</h3>
    <div v-for="s in unavailable" :key="s.name" class="s-service" :style="{ opacity: 0.6 }">
      <div class="logo" :style="{ background: s.color }">{{ s.initial }}</div>
      <div class="info">
        <div class="title">{{ s.name }}</div>
        <div class="desc">{{ s.reason }}</div>
      </div>
      <span class="status">{{ t("settings.sync.statusUnavailable") }}</span>
    </div>
  </div>
</template>
