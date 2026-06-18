<script setup lang="ts">
// IMAP newsletter mailbox form — the "newsletter" tab AddFeedDialog used to
// carry, now a secondary panel inside the Source Hub (newsletters are a
// far rarer add than a feed URL, so they no longer occupy a first-level tab).

import { useMutation, useQueryClient } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import * as api from "../api";
import { errorText } from "../lib/errors";
import { toast } from "../stores/toasts";
import type { Feed } from "../types";
import Icon from "./Icon.vue";

const emit = defineEmits<{
  (e: "added", feed: Feed): void;
  (e: "back"): void;
}>();

const { t } = useI18n();
const qc = useQueryClient();

/** The IMAP port `add_newsletter_source` falls back to (implicit-TLS IMAP). */
const DEFAULT_IMAP_PORT = 993;

/**
 * Coerce the free-text port field into a valid TCP port. The backend argument
 * is a `u16`, so a value out of the 1–65535 range or non-integer would fail
 * deserialization with a cryptic, non-localised error. Anything invalid falls
 * back to 993.
 */
function parsePort(raw: string): number {
  const n = Number(raw.trim());
  if (!Number.isInteger(n) || n < 1 || n > 65535) return DEFAULT_IMAP_PORT;
  return n;
}

const nlTitle = ref("");
const nlHost = ref("");
const nlPort = ref("993");
const nlUser = ref("");
const nlPass = ref("");
const nlFolder = ref("INBOX");

const addNewsletter = useMutation({
  mutationFn: () =>
    api.addNewsletterSource({
      title: nlTitle.value.trim() || null,
      host: nlHost.value.trim(),
      port: parsePort(nlPort.value),
      username: nlUser.value.trim(),
      password: nlPass.value,
      folder: nlFolder.value.trim() || "INBOX",
    }),
  onSuccess: (feed) => {
    for (const key of [["feeds"], ["folders"], ["articles"], ["counts"], ["tags"]]) {
      qc.invalidateQueries({ queryKey: key });
    }
    toast.show(t("hub.subscribed", { title: feed.title }));
    emit("added", feed);
  },
});

const ready = computed(
  () => nlHost.value.trim() !== "" && nlUser.value.trim() !== "" && nlPass.value !== "",
);

function submit(): void {
  if (ready.value && !addNewsletter.isPending.value) addNewsletter.mutate();
}

// Enter submits from any field. The `isComposing` guard skips the Enter that
// only confirms an IME candidate (CJK input).
function onKey(e: KeyboardEvent): void {
  if (e.key === "Enter" && !e.isComposing) submit();
}
</script>

<template>
  <div class="nl-form">
    <p class="modal-hint">{{ t("addFeed.newsletterHint") }}</p>
    <input
      class="modal-input"
      type="text"
      autofocus
      :placeholder="t('addFeed.nlTitlePlaceholder')"
      :aria-label="t('addFeed.nlTitleLabel')"
      v-model="nlTitle"
      @keydown="onKey"
    />
    <div :style="{ display: 'flex', gap: '8px' }">
      <input
        class="modal-input"
        type="text"
        :style="{ flex: 2 }"
        :placeholder="t('addFeed.nlHostPlaceholder')"
        :aria-label="t('addFeed.nlHostLabel')"
        v-model="nlHost"
        @keydown="onKey"
      />
      <input
        class="modal-input"
        type="text"
        :style="{ flex: 1 }"
        placeholder="993"
        :aria-label="t('addFeed.nlPortLabel')"
        v-model="nlPort"
        @keydown="onKey"
      />
    </div>
    <input
      class="modal-input"
      type="text"
      :placeholder="t('addFeed.nlUserPlaceholder')"
      :aria-label="t('addFeed.nlUserLabel')"
      v-model="nlUser"
      @keydown="onKey"
    />
    <input
      class="modal-input"
      type="password"
      :placeholder="t('addFeed.nlPassPlaceholder')"
      :aria-label="t('addFeed.nlPassLabel')"
      v-model="nlPass"
      @keydown="onKey"
    />
    <input
      class="modal-input"
      type="text"
      placeholder="INBOX"
      :aria-label="t('addFeed.nlFolderLabel')"
      v-model="nlFolder"
      @keydown="onKey"
    />
    <div v-if="addNewsletter.isError.value" class="modal-error">
      {{ errorText(addNewsletter.error.value) }}
    </div>
    <div class="modal-actions">
      <button class="s-btn" @click="emit('back')">
        {{ t("hub.back") }}
      </button>
      <button
        class="s-btn primary"
        :disabled="!ready || addNewsletter.isPending.value"
        @click="submit"
      >
        <Icon name="plus" :size="12" />
        {{ addNewsletter.isPending.value ? t("addFeed.connecting") : t("addFeed.connect") }}
      </button>
    </div>
  </div>
</template>
