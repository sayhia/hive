<script setup lang="ts">
// A small modal for editing one feed, opened from the sidebar's right-click
// "Edit…". Deliberately lightweight: right-clicking a feed shouldn't drag the
// whole Settings window open just to rename it. The form itself is
// FeedEditFields — the same component Settings → Subscriptions renders inline,
// so the two entry points can't drift apart.
//
// The feed is looked up live from the feeds query rather than passed in as a
// snapshot, so the fields reflect each save; if it disappears (unsubscribed
// elsewhere) the dialog closes itself.

import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useQuery } from "@tanstack/vue-query";
import * as api from "../api";
import { useFocusTrap } from "../composables/useFocusTrap";
import Icon from "./Icon.vue";
import FeedAvatar from "./FeedAvatar.vue";
import FeedEditFields from "./settings/FeedEditFields.vue";

const props = defineProps<{ feedId: number }>();
const emit = defineEmits<{ (e: "close"): void }>();

const { t } = useI18n();

const dialogRef = ref<HTMLDivElement | null>(null);
useFocusTrap(dialogRef);

const feeds = useQuery({ queryKey: ["feeds"], queryFn: api.listFeeds });
const feed = computed(() => feeds.data.value?.find((f) => f.id === props.feedId));

// Gone (unsubscribed from elsewhere) → nothing left to edit. The data guard keeps
// the initial undefined-while-loading state from closing us immediately.
watch(feed, (f) => {
  if (feeds.data.value && !f) emit("close");
});

// Escape closes from anywhere inside (capture so it beats the global key map).
function onWindowKey(e: KeyboardEvent) {
  if (e.key === "Escape") {
    e.stopPropagation();
    emit("close");
  }
}
onMounted(() => window.addEventListener("keydown", onWindowKey, true));
onUnmounted(() => window.removeEventListener("keydown", onWindowKey, true));
</script>

<template>
  <Teleport to="body">
    <div class="modal-backdrop" @click="emit('close')">
      <div
        class="modal modal-feed-edit"
        ref="dialogRef"
        role="dialog"
        aria-modal="true"
        :aria-label="t('settings.subscriptions.editTitle')"
        @click.stop
      >
        <div class="feed-edit-head">
          <FeedAvatar v-if="feed" :feed="feed" :size="20" />
          <h2>{{ feed ? feed.title : t("settings.subscriptions.editTitle") }}</h2>
          <button
            class="tb-btn"
            @click="emit('close')"
            :title="t('common.close')"
            :aria-label="t('common.close')"
          >
            <Icon name="x" :size="15" />
          </button>
        </div>
        <div class="feed-edit-body">
          <!-- Saving is the dialog's terminal action: commit, then dismiss. -->
          <FeedEditFields v-if="feed" :feed="feed" @saved="emit('close')" />
        </div>
      </div>
    </div>
  </Teleport>
</template>
