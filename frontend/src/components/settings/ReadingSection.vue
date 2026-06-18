<script setup lang="ts">
// Reading: body font + size + line spacing, body max width, reading-time
// toggle, and the default open mode (reader / extracted full text / web view —
// individual feeds override it in Subscriptions). Ported from papr's
// `ReadingSection`. The reader sliders drive `ui.setReader` live (cheap
// localStorage write + reactive preview), so they use `change`, not `commit`.

import { useI18n } from "vue-i18n";
import { useUi, READER_BOUNDS, type OpenMode } from "../../stores/ui";
import SettingsRow from "./SettingsRow.vue";
import SettingsToggle from "./SettingsToggle.vue";
import SettingsSlider from "./SettingsSlider.vue";
import SettingsSelect from "./SettingsSelect.vue";
import FontPicker from "./FontPicker.vue";

const { t } = useI18n();
const ui = useUi();

const openModeOptions = [
  { value: "reader", label: t("settings.subscriptions.openReader") },
  { value: "extracted", label: t("settings.subscriptions.openExtracted") },
  { value: "web", label: t("settings.subscriptions.openWeb") },
];
</script>

<template>
  <div class="settings-group">
    <h3 class="settings-group-title">{{ t("settings.reading.font") }}</h3>
    <FontPicker
      axis="ui"
      :label="t('settings.fonts.uiFont')"
      :desc="t('settings.fonts.uiFontDesc')"
    />
    <FontPicker
      axis="reader"
      :label="t('settings.fonts.readerFont')"
      :desc="t('settings.fonts.readerFontDesc')"
    />
    <SettingsRow :label="t('settings.reading.fontSize')" v-slot="{ ariaLabel }">
      <SettingsSlider
        :value="ui.readerSize"
        :min="READER_BOUNDS.size.min"
        :max="READER_BOUNDS.size.max"
        unit="px"
        :aria-label="ariaLabel"
        @change="ui.setReader({ readerSize: $event })"
      />
    </SettingsRow>
    <SettingsRow :label="t('settings.reading.lineHeight')" v-slot="{ ariaLabel }">
      <SettingsSlider
        :value="ui.readerLeading"
        :min="READER_BOUNDS.leading.min"
        :max="READER_BOUNDS.leading.max"
        :step="5"
        unit="%"
        :aria-label="ariaLabel"
        @change="ui.setReader({ readerLeading: $event })"
      />
    </SettingsRow>
  </div>

  <div class="settings-group">
    <h3 class="settings-group-title">{{ t("settings.reading.layout") }}</h3>
    <SettingsRow :label="t('settings.reading.maxWidth')" v-slot="{ ariaLabel }">
      <SettingsSlider
        :value="ui.readerWidth"
        :min="READER_BOUNDS.width.min"
        :max="READER_BOUNDS.width.max"
        :step="20"
        unit="px"
        :aria-label="ariaLabel"
        @change="ui.setReader({ readerWidth: $event })"
      />
    </SettingsRow>
    <SettingsRow
      :label="t('settings.reading.readingTime')"
      :desc="t('settings.reading.readingTimeDesc')"
      v-slot="{ ariaLabel }"
    >
      <SettingsToggle
        :checked="ui.prefs.showReadingTime"
        :aria-label="ariaLabel"
        @change="ui.setPref({ showReadingTime: $event })"
      />
    </SettingsRow>
    <SettingsRow
      :label="t('settings.reading.transSwitch')"
      :desc="t('settings.reading.transSwitchDesc')"
      v-slot="{ ariaLabel }"
    >
      <SettingsToggle
        :checked="ui.prefs.showTransSwitch"
        :aria-label="ariaLabel"
        @change="ui.setPref({ showTransSwitch: $event })"
      />
    </SettingsRow>
  </div>

  <div class="settings-group">
    <h3 class="settings-group-title">{{ t("settings.reading.openModeTitle") }}</h3>
    <SettingsRow
      :label="t('settings.reading.defaultOpenMode')"
      :desc="t('settings.reading.defaultOpenModeDesc')"
      v-slot="{ ariaLabel }"
    >
      <SettingsSelect
        :value="ui.prefs.defaultOpenMode"
        :options="openModeOptions"
        :aria-label="ariaLabel"
        @change="ui.setPref({ defaultOpenMode: $event as OpenMode })"
      />
    </SettingsRow>
  </div>
</template>
