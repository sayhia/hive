<script setup lang="ts">
// Shortcuts: a static, grouped keyboard cheat sheet. Ported from papr's
// `ShortcutsSection`. Modifier glyphs come from the platform helper.

import { useI18n } from "vue-i18n";
import { modKey } from "../../lib/platform";

const { t } = useI18n();

const groups = [
  {
    title: t("settings.shortcuts.navigation"),
    items: [
      { desc: t("settings.shortcuts.nextArticle"), keys: ["J"] },
      { desc: t("settings.shortcuts.prevArticle"), keys: ["K"] },
      { desc: t("settings.shortcuts.pageReader"), keys: ["Space"] },
      { desc: t("settings.shortcuts.typeLarger"), keys: [modKey, "+"] },
      { desc: t("settings.shortcuts.typeSmaller"), keys: [modKey, "−"] },
      { desc: t("settings.shortcuts.typeReset"), keys: [modKey, "0"] },
      { desc: t("settings.shortcuts.openInBrowser"), keys: ["O"] },
      { desc: t("settings.shortcuts.openOriginalClick"), keys: [modKey, t("settings.shortcuts.click")] },
      { desc: t("settings.shortcuts.toggleRead"), keys: ["U"] },
      { desc: t("settings.shortcuts.exitFocus"), keys: ["Esc"] },
    ],
  },
  {
    title: t("settings.shortcuts.actions"),
    items: [
      { desc: t("settings.shortcuts.star"), keys: ["S"] },
      { desc: t("settings.shortcuts.readLater"), keys: ["B"] },
      { desc: t("settings.shortcuts.copyLink"), keys: ["C"] },
      { desc: t("settings.shortcuts.aiSummary"), keys: ["I"] },
      { desc: t("settings.shortcuts.translate"), keys: ["T"] },
      { desc: t("settings.shortcuts.markAllRead"), keys: ["⇧", "A"] },
    ],
  },
  {
    title: t("settings.shortcuts.view"),
    items: [
      { desc: t("settings.shortcuts.focusReading"), keys: ["F"] },
      { desc: t("settings.shortcuts.hideRead"), keys: ["V"] },
      { desc: t("settings.shortcuts.toggleTheme"), keys: ["⇧", "D"] },
    ],
  },
  {
    title: t("settings.shortcuts.global"),
    items: [
      { desc: t("settings.shortcuts.commandPalette"), keys: [modKey, "K"] },
      { desc: t("settings.shortcuts.commandPalette"), keys: ["/"] },
      { desc: t("settings.shortcuts.refreshAll"), keys: [modKey, "R"] },
      { desc: t("settings.shortcuts.addFeed"), keys: ["A"] },
      { desc: t("settings.shortcuts.openSettings"), keys: [modKey, ","] },
    ],
  },
];
</script>

<template>
  <div class="settings-group" v-for="g in groups" :key="g.title">
    <h3 class="settings-group-title">{{ g.title }}</h3>
    <div class="s-shortcuts">
      <div class="s-shortcut" v-for="(it, i) in g.items" :key="i">
        <span class="desc">{{ it.desc }}</span>
        <span class="keys">
          <span class="s-key" v-for="(k, j) in it.keys" :key="j">{{ k }}</span>
        </span>
      </div>
    </div>
  </div>
</template>
