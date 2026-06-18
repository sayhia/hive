// Background article-translation jobs (Pinia). A translation runs to completion
// independently of which article is on screen, so several can be in flight at
// once. Progress arrives per batch. Ported from papr's translation.ts.

import { defineStore } from "pinia";
import { ref } from "vue";
import * as api from "../api";
import { reportError } from "./toasts";

export type JobStatus = "translating" | "done" | "error";

export interface TranslationJob {
  status: JobStatus;
  done: number;
  total: number;
  html: string;
  /** Translated title (arrives on the start event; empty until then). */
  title: string;
  lang: string;
  engine: string;
}

export const useTranslationJobs = defineStore("translation", () => {
  const jobs = ref<Record<number, TranslationJob>>({});

  function patch(articleId: number, fn: (j: TranslationJob) => TranslationJob) {
    const cur = jobs.value[articleId];
    if (!cur) return;
    jobs.value = { ...jobs.value, [articleId]: fn(cur) };
  }

  function translate(articleId: number, lang: string, engine: string) {
    const cur = jobs.value[articleId];
    if (cur?.status === "translating" && cur.lang === lang && cur.engine === engine) return;
    jobs.value = {
      ...jobs.value,
      [articleId]: { status: "translating", done: 0, total: 0, html: "", title: "", lang, engine },
    };

    // Guard every mutation against this run's identity: a newer translation for
    // the same article (the user switched target language/engine) replaces the
    // job, and this run's late batch events / settle must not clobber the new one.
    const owns = () => {
      const j = jobs.value[articleId];
      return j != null && j.lang === lang && j.engine === engine;
    };

    api
      .aiTranslate(articleId, lang, engine, (e) => {
        if (!owns()) return;
        if (e.type === "start") {
          patch(articleId, (j) => ({ ...j, total: e.data.total, title: e.data.title || j.title }));
        } else if (e.type === "batch") {
          patch(articleId, (j) => ({ ...j, done: e.data.done, html: j.html + e.data.html }));
        } else if (e.type === "done") {
          patch(articleId, (j) => ({ ...j, html: e.data.html, title: e.data.title || j.title, status: "done" }));
        }
      })
      .then(() => {
        if (owns()) patch(articleId, (j) => (j.status === "translating" ? { ...j, status: "done" } : j));
      })
      .catch((err) => {
        if (!owns()) return;
        patch(articleId, (j) => ({ ...j, status: "error" }));
        reportError(err);
      });
  }

  function clear(articleId: number) {
    if (!(articleId in jobs.value)) return;
    const rest = { ...jobs.value };
    delete rest[articleId];
    jobs.value = rest;
  }

  return { jobs, translate, clear };
});
