<script setup lang="ts">
// "Add into folder" select, shared by the subscribe surfaces. Owns its
// folders query and always renders — it used to hide itself when the user
// had no folders yet, which read as "订阅归类没有了" on a fresh library.
// Instead the last option creates a folder inline (prompt → create → select),
// so filing new subscriptions works even from zero.

import { useQuery, useQueryClient } from "@tanstack/vue-query";
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import * as api from "../api";
import { reportError, toast } from "../stores/toasts";
import PromptDialog from "./PromptDialog.vue";

const props = defineProps<{
  modelValue: number | null;
}>();

const emit = defineEmits<{
  (e: "update:modelValue", v: number | null): void;
}>();

const { t } = useI18n();
const qc = useQueryClient();
const folders = useQuery({ queryKey: ["folders"], queryFn: api.listFolders });

const creating = ref(false);

function onChange(e: Event): void {
  const el = e.target as HTMLSelectElement;
  if (el.value === "__new__") {
    // Open the prompt and snap the select back to the current pick — the
    // sentinel must never linger as the shown value if the prompt is
    // cancelled.
    creating.value = true;
    el.value = props.modelValue != null ? String(props.modelValue) : "";
    return;
  }
  emit("update:modelValue", el.value ? Number(el.value) : null);
}

function createAndSelect(name: string): void {
  creating.value = false;
  api
    .createFolder(name)
    .then((id) => {
      qc.invalidateQueries({ queryKey: ["folders"] });
      emit("update:modelValue", id);
      toast.show(t("app.folderCreated"));
    })
    .catch(reportError);
}
</script>

<template>
  <select
    class="s-select"
    :aria-label="t('hub.folderLabel')"
    :value="modelValue ?? ''"
    @change="onChange"
  >
    <option value="">{{ t("hub.noFolder") }}</option>
    <option v-for="f in folders.data.value ?? []" :key="f.id" :value="f.id">
      {{ f.name }}
    </option>
    <option value="__new__">{{ t("hub.newFolderOption") }}</option>
  </select>

  <PromptDialog
    v-if="creating"
    :title="t('app.newFolderTitle')"
    :placeholder="t('app.folderNamePlaceholder')"
    @submit="createAndSelect"
    @cancel="creating = false"
  />
</template>
