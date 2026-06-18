// Viewport-driven list-row translation jobs. Unlike the reader's body
// translation store (stores/translation.ts, streaming + sequenced), these
// jobs translate only title/snippet previews with small one-shot calls, and
// are scheduled as virtualized rows become visible. Results are cached
// backend-side per (article, lang, engine), so re-visiting rows is free.
// Ported from papr's listTranslation.ts (zustand → Pinia).

import { defineStore } from "pinia";
import { reactive } from "vue";
import * as api from "../api";
import { reportError } from "./toasts";
import type { ArticleSummary } from "../types";

export type ListTranslationStatus = "queued" | "translating" | "done" | "error";

export interface ListTranslationJob {
  status: ListTranslationStatus;
  articleId: number;
  lang: string;
  engine: string;
  /** Source fields at enqueue time — a changed source re-queues the row. */
  sourceTitle: string;
  sourceSnippet: string | null;
  title: string | null;
  snippet: string | null;
}

const MAX_CONCURRENT = 3;

const keyFor = (articleId: number, lang: string, engine: string) =>
  `${articleId}:${lang}:${engine}`;

export const listTranslationKey = keyFor;

/** A finished/failed result only lands if the job entry still describes the
 *  same source text — a re-queue for changed source must not be overwritten
 *  by a stale response. */
const matchesJobSource = (
  current: ListTranslationJob | undefined,
  expected: ListTranslationJob,
) =>
  current?.articleId === expected.articleId &&
  current.lang === expected.lang &&
  current.engine === expected.engine &&
  current.sourceTitle === expected.sourceTitle &&
  current.sourceSnippet === expected.sourceSnippet;

export const useListTranslation = defineStore("listTranslation", () => {
  const jobs = reactive<Record<string, ListTranslationJob>>({});

  function runNext(): void {
    const active = Object.values(jobs).filter((j) => j.status === "translating").length;
    if (active >= MAX_CONCURRENT) return;
    const nextKey = Object.keys(jobs).find((k) => jobs[k].status === "queued");
    if (!nextKey) return;

    const job = { ...jobs[nextKey] };
    jobs[nextKey].status = "translating";

    api
      .translateArticlePreview(job.articleId, job.lang, job.engine)
      .then((res) => {
        if (!matchesJobSource(jobs[nextKey], job)) return;
        Object.assign(jobs[nextKey], {
          status: "done",
          title: res.title || null,
          snippet: res.snippet || null,
          lang: res.lang,
          engine: res.engine,
        });
      })
      .catch((err) => {
        if (matchesJobSource(jobs[nextKey], job)) jobs[nextKey].status = "error";
        reportError(err);
      })
      .finally(runNext);

    runNext();
  }

  /** Queue translations for the currently visible rows. Skips rows already
   *  queued/done for the same source text; an errored or source-changed row
   *  re-queues. */
  function enqueueVisible(articles: ArticleSummary[], lang: string, engine: string): void {
    const targetLang = lang.trim();
    if (!targetLang) return;

    let changed = false;
    for (const article of articles) {
      const key = keyFor(article.id, targetLang, engine);
      const sourceTitle = article.title;
      const sourceSnippet = article.snippet ?? null;
      const existing = jobs[key];
      const sourceChanged =
        existing?.sourceTitle !== sourceTitle || existing?.sourceSnippet !== sourceSnippet;
      if (existing && existing.status !== "error" && !sourceChanged) continue;
      jobs[key] = {
        articleId: article.id,
        title: existing?.title ?? null,
        snippet: existing?.snippet ?? null,
        sourceTitle,
        sourceSnippet,
        status: "queued",
        lang: targetLang,
        engine,
      };
      changed = true;
    }
    if (changed) runNext();
  }

  return { jobs, enqueueVisible };
});
