// The subscribe mutation both discovery surfaces share (previously duplicated
// by AddFeedDialog and ExploreDialog, drifting apart on which caches they
// invalidated). One place owns: the AddFeed call, the article-bearing cache
// invalidations, and the success toast. Error handling stays with the caller —
// the hub shows an inline error for the main input and a toast for row adds.

import { useMutation, useQueryClient } from "@tanstack/vue-query";
import { useI18n } from "vue-i18n";
import * as api from "../api";
import { toast } from "../stores/toasts";
import type { Feed } from "../types";

export function useSubscribe(onAdded?: (feed: Feed) => void) {
  const qc = useQueryClient();
  const { t } = useI18n();

  return useMutation({
    mutationFn: ({ url, folderId }: { url: string; folderId: number | null }) =>
      api.addFeed(url, folderId),
    onSuccess: (feed) => {
      // Only the article-bearing caches — refreshing unrelated ones (AI
      // summaries, settings, storage) is wasted work.
      for (const key of [["feeds"], ["folders"], ["articles"], ["counts"], ["tags"]]) {
        qc.invalidateQueries({ queryKey: key });
      }
      toast.show(t("hub.subscribed", { title: feed.title }));
      onAdded?.(feed);
    },
  });
}
