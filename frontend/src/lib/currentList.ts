// Read the article list currently shown in the middle pane straight from the
// Vue Query cache — used by keyboard shortcuts and reader navigation so they
// always agree with what the user sees.
//
// Ported from papr's lib/currentList.ts (@tanstack/react-query → vue-query,
// zustand `useUi.getState()` → Pinia `useUi()`). In a component you'd reach the
// QueryClient with `import { useQueryClient } from "@tanstack/vue-query"`; this
// helper instead takes one explicitly so it stays callable from plain functions.
//
// The cache key MUST match the ArticleList browse query:
//   ["articles", query, unreadOnly, sortOldest]
// where `query` is the ArticleQuery object, and the cached value is the
// infinite-query shape `{ pages: ArticleSummary[][] }`.

import type { QueryClient } from "@tanstack/vue-query";
import { useUi } from "../stores/ui";
import type { ArticleSummary } from "../types";

export function currentList(qc: QueryClient): ArticleSummary[] {
  // Pinia store properties are auto-unwrapped on the store instance, so read
  // them straight off `ui` (no `.value`). Callers run inside an active Pinia.
  const ui = useUi();
  const inf = qc.getQueryData(["articles", ui.query, ui.unreadOnly, ui.sortOldest]) as
    | { pages: ArticleSummary[][] }
    | undefined;
  return inf?.pages.flat() ?? [];
}
