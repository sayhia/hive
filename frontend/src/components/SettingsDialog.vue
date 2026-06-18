<script setup lang="ts">
// The tabbed preferences dialog. Ported from papr's SettingsDialog.tsx (React +
// react-query + Tauri → Vue 3 + vue-query + Wails), keeping the same CSS class
// names, section layout and behaviour.
//
// Differences from papr, all driven by the host:
//   • Props/callbacks → emits: `onClose`/`onAddFeed` become `close`/`add-feed`;
//     `onToast` is dropped — sections raise toasts through the global store.
//   • The shared row controls (Row / Toggle / Select / Segmented / Slider /
//     SettingFlag) and each section live in ./settings/ as small SFCs, since
//     React's cloneElement aria-label injection has no Vue equivalent and the
//     file is large. A single SFC was an option; this keeps each piece readable.
//   • Tauri-only bits are stubs (the prompt specifies them so): the app version
//     is read best-effort from a backend setting, "Launch at login" is a
//     persisted flag, and the About update check reports up-to-date.

import { computed, onMounted, onUnmounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useQuery } from "@tanstack/vue-query";
import * as api from "../api";
import { useFocusTrap } from "../composables/useFocusTrap";
import { modCombo } from "../lib/platform";
import Icon, { type IconName } from "./Icon.vue";
import { useAppVersion } from "./settings/useAppVersion";
import GeneralSection from "./settings/GeneralSection.vue";
import AppearanceSection from "./settings/AppearanceSection.vue";
import ReadingSection from "./settings/ReadingSection.vue";
import SubscriptionsSection from "./settings/SubscriptionsSection.vue";
import FiltersSection from "./settings/FiltersSection.vue";
import SyncSection from "./settings/SyncSection.vue";
import ShortcutsSection from "./settings/ShortcutsSection.vue";
import NotificationsSection from "./settings/NotificationsSection.vue";
import AiSection from "./settings/AiSection.vue";
import AdvancedSection from "./settings/AdvancedSection.vue";
import AboutSection from "./settings/AboutSection.vue";

const props = defineProps<{
  initialSection?: string;
}>();

const emit = defineEmits<{
  (e: "close"): void;
  (e: "add-feed"): void;
}>();

const { t } = useI18n();

// `labelKey` holds an i18n key — resolved with t() at render time.
// Per-section icon-tile colours — a cohesive cool range (blue / cyan / teal /
// violet / slate), no warm hues, so the settings nav matches hive's identity.
const SECTIONS: { id: string; labelKey: string; icon: IconName; color: string }[] = [
  { id: "general", labelKey: "settings.nav.general", icon: "settings", color: "#64748B" },
  { id: "appearance", labelKey: "settings.nav.appearance", icon: "globe", color: "#3D7DFF" },
  { id: "reading", labelKey: "settings.nav.reading", icon: "eye", color: "#0E9FB5" },
  { id: "subscriptions", labelKey: "settings.nav.subscriptions", icon: "rss", color: "#6366F1" },
  { id: "filters", labelKey: "settings.nav.filters", icon: "mute", color: "#8B5CF6" },
  { id: "sync", labelKey: "settings.nav.sync", icon: "refresh", color: "#0FB5A6" },
  { id: "shortcuts", labelKey: "settings.nav.shortcuts", icon: "command", color: "#5B6CD6" },
  { id: "notifications", labelKey: "settings.nav.notifications", icon: "inbox", color: "#2E8FD0" },
  { id: "ai", labelKey: "settings.nav.ai", icon: "sparkle-fill", color: "#7C5CFF" },
  { id: "advanced", labelKey: "settings.nav.advanced", icon: "sort", color: "#566273" },
  { id: "about", labelKey: "settings.nav.about", icon: "sparkle", color: "#1E54CC" },
];

const section = ref(props.initialSection ?? "general");
const windowRef = ref<HTMLDivElement | null>(null);
const version = useAppVersion();
useFocusTrap(windowRef);

const feeds = useQuery({ queryKey: ["feeds"], queryFn: api.listFeeds });
const feedList = computed(() => feeds.data.value ?? []);
const feedCount = computed(() => feeds.data.value?.length ?? 0);

const cur = computed(() => SECTIONS.find((s) => s.id === section.value)!);

const subs = computed<Record<string, string>>(() => ({
  general: t("settings.sub.general"),
  appearance: t("settings.sub.appearance"),
  reading: t("settings.sub.reading"),
  subscriptions: t("settings.sub.subscriptions", { count: feedCount.value }),
  filters: t("settings.sub.filters"),
  sync: t("settings.sub.sync"),
  shortcuts: t("settings.sub.shortcuts"),
  notifications: t("settings.sub.notifications"),
  ai: t("settings.sub.ai"),
  advanced: t("settings.sub.advanced"),
  about: t("settings.sub.about"),
}));

// Escape closes the dialog regardless of which control holds focus. Captured so
// it pre-empts background handlers (App.vue's global key map skips Settings).
function onKey(e: KeyboardEvent): void {
  if (e.key === "Escape") {
    e.stopPropagation();
    emit("close");
  }
}
onMounted(() => window.addEventListener("keydown", onKey, true));
onUnmounted(() => window.removeEventListener("keydown", onKey, true));
</script>

<template>
  <Teleport to="body">
    <div class="settings-backdrop" @click="emit('close')">
      <div
        class="settings-window"
        ref="windowRef"
        role="dialog"
        aria-modal="true"
        :aria-label="t('settings.title')"
        @click.stop
      >
        <div class="settings-sidebar">
          <div class="settings-sidebar-title">
            {{ t("settings.title") }}
            <span class="badge">{{ modCombo(",") }}</span>
          </div>
          <div
            v-for="s in SECTIONS"
            :key="s.id"
            class="settings-nav-item"
            :class="{ active: section === s.id }"
            @click="section = s.id"
          >
            <span class="nav-ico" :style="{ background: s.color }">
              <Icon :name="s.icon" :size="11" color="#fff" />
            </span>
            {{ t(s.labelKey) }}
          </div>
          <div class="settings-nav-spacer" />
          <div class="settings-version">Hive<template v-if="version"> {{ version }}</template></div>
        </div>

        <div class="settings-content">
          <div class="settings-header">
            <h2>{{ t(cur.labelKey) }}</h2>
            <span class="sub">{{ subs[section] }}</span>
          </div>
          <button class="settings-close" @click="emit('close')" :title="t('settings.closeTitle')">
            <Icon name="x" :size="15" />
          </button>

          <div class="settings-scroll">
            <GeneralSection v-if="section === 'general'" />
            <AppearanceSection v-else-if="section === 'appearance'" />
            <ReadingSection v-else-if="section === 'reading'" />
            <SubscriptionsSection
              v-else-if="section === 'subscriptions'"
              :feeds="feedList"
              @add-feed="emit('add-feed')"
            />
            <FiltersSection v-else-if="section === 'filters'" :feeds="feedList" />
            <SyncSection v-else-if="section === 'sync'" />
            <ShortcutsSection v-else-if="section === 'shortcuts'" />
            <NotificationsSection v-else-if="section === 'notifications'" />
            <AiSection v-else-if="section === 'ai'" />
            <AdvancedSection v-else-if="section === 'advanced'" />
            <AboutSection v-else-if="section === 'about'" />
          </div>
        </div>
      </div>
    </div>
  </Teleport>
</template>
