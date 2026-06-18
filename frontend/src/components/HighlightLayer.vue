<script setup lang="ts">
// Highlight & annotation layer for the Reader (feature F7).
//
// Owns the text-selection UI laid over the article body:
//   1. a popover shown when text is selected (HighlightPopover in `pending`
//      mode) — merged colour picker + note textarea, so the whole "colour
//      + optional note" step happens in one dialog rather than a colour
//      toolbar followed by a re-opened popover.
//   2. the same popover in `hl` mode for editing / deleting an existing
//      highlight (opened by clicking a rendered <mark>).
//
// The pure re-anchoring lives in `lib/anchor.ts`; the DOM wrapping in
// `lib/highlightDom.ts`. This component is the glue that calls them and the
// highlight CRUD service methods.
//
// Ported from papr's HighlightLayer.tsx (React → Vue 3 <script setup>).
//
// ── PROPS CONTRACT (for Reader.vue / integration) ────────────────────────────
//   articleId: number              — the article whose body is highlighted.
//   bodyEl: HTMLElement | null     — the *resolved* `.article-body` DOM element
//                                    the highlights are applied into. Reader
//                                    passes its `bodyRef` value (an HTMLElement
//                                    or null until the body mounts).
//
// This replaces papr's `bodyRef` + `bodyVersion` pair: a MutationObserver on the
// body's child list re-applies the <mark>s whenever Reader swaps the body markup
// (extract toggle, extraction finishing, translation), so no separate version
// string is needed. `bodyEl` is watched so listeners + the observer rebind when
// the element instance itself changes (article switch / reopen race).

import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import * as api from "../api";
import { reportError, withUndo } from "../stores/toasts";
import { useUi } from "../stores/ui";
import {
  applyHighlights,
  clearHighlights,
  plainText,
  selectionAnchor,
} from "../lib/highlightDom";
import { captureContext } from "../lib/anchor";
import type { Highlight } from "../types";
import HighlightPopover from "./HighlightPopover.vue";

const props = defineProps<{
  /** The article whose body is highlighted. */
  articleId: number;
  /** The rendered article-body element the highlights are applied into.
   *  `null` until Reader's body div has mounted. */
  bodyEl: HTMLElement | null;
}>();

const { t } = useI18n();
const ui = useUi();
const highlights = ref<Highlight[]>([]);

/** The pending selection's popover — merges what used to be a separate
 *  colour toolbar and post-create edit popover into a single dialog.
 *  When set, the popover renders at (x, y) with the captured `quote`
 *  and offers colour swatches + a note textarea; committing persists
 *  the highlight, Escape drops the whole thing. */
const creating = ref<{ x: number; y: number; quote: string; textOffset: number } | null>(null);

// The highlight whose edit popover is open, plus where to anchor it. Stores the
// id (not a snapshot) so the popover always reflects the live highlight — a
// recolour re-renders its active swatch immediately.
const editing = ref<{ hlId: number; x: number; y: number } | null>(null);

// The live highlight backing the open edit popover, looked up fresh from the
// current set — so a recolour (which reloads `highlights`) is reflected in the
// popover's active swatch. `undefined` once the highlight is deleted, which also
// tears the popover down.
const editingHl = computed(() =>
  editing.value ? highlights.value.find((h) => h.id === editing.value!.hlId) : undefined,
);

// Load the article's stored highlights.
const reload = () => {
  api
    .listHighlights(props.articleId)
    .then((hs) => (highlights.value = hs))
    .catch(() => (highlights.value = []));
};

// ── re-apply <mark>s to the live body ───────────────────────────────────────
// The <mark> overlay is injected into DOM that Vue owns via v-html, so anything
// that re-populates the body — Vue resetting its innerHTML, or the body element
// only filling in *after* this binds (the reopen race that left highlights blank
// until the next edit) — silently drops every mark. A MutationObserver re-applies
// them whenever the body's child list changes.
let obs: MutationObserver | null = null;

const applyMarks = (el: HTMLElement) => {
  // Suspend observation while we mutate so our own <mark> edits do not
  // re-trigger the callback (which would loop).
  obs?.disconnect();
  if (highlights.value.length === 0) clearHighlights(el);
  else {
    applyHighlights(el, highlights.value);
    focusPendingHighlight(el);
  }
  obs?.observe(el, { childList: true, subtree: true });
};

// If the ui store has a pending highlight id (set when the user clicked a
// HighlightCard), scroll its <mark> into view and play a focus flash, then
// consume the pending id. If the mark isn't in this article body the pending
// id is left untouched so a later mount on the right article can still locate
// it.
function focusPendingHighlight(rootEl: HTMLElement): void {
  const pending = ui.pendingHighlightId;
  if (pending === null) return;
  const target = rootEl.querySelector<HTMLElement>(`mark[data-hl="${pending}"]`);
  if (!target) return;
  target.scrollIntoView({ behavior: "smooth", block: "center" });
  target.classList.add("hl-flash");
  window.setTimeout(() => target.classList.remove("hl-flash"), 1500);
  ui.consumePendingHighlight();
}

// ── body event listeners (selection popover + mark clicks) ──────────────────
const onMouseUp = () => {
  const el = props.bodyEl;
  if (!el) return;
  // Defer so the browser has committed the selection.
  window.setTimeout(() => {
    const sel = selectionAnchor(el);
    if (!sel) {
      creating.value = null;
      return;
    }
    const range = window.getSelection()?.getRangeAt(0);
    const rect = range?.getBoundingClientRect();
    if (!rect) return;
    // Anchor the popover *below* the selection so the caret stays visible
    // while the user reads what they just picked. Popover's own measure()
    // flips it above if it would clip the viewport.
    creating.value = {
      x: rect.left,
      y: rect.bottom + 8,
      quote: sel.quote,
      textOffset: sel.textOffset,
    };
  }, 0);
};

// Clicking an existing highlight <mark> opens its edit popover.
const onBodyClick = (e: MouseEvent) => {
  const mark = (e.target as HTMLElement).closest("mark[data-hl]");
  if (!mark) return;
  e.preventDefault();
  e.stopPropagation();
  const id = Number((mark as HTMLElement).dataset.hl);
  if (!highlights.value.some((h) => h.id === id)) return;
  // If a pending-create popover happens to be open (rare — the user
  // clicked a mark instead of picking a colour), drop it before opening
  // the edit popover so the two dialogs never overlap.
  creating.value = null;
  const r = mark.getBoundingClientRect();
  editing.value = { hlId: id, x: r.left, y: r.bottom + 6 };
};

// Bind the observer + listeners to a body element; returns a cleanup fn.
function bind(el: HTMLElement): () => void {
  obs = new MutationObserver(() => applyMarks(el));
  applyMarks(el);
  el.addEventListener("mouseup", onMouseUp);
  el.addEventListener("click", onBodyClick, true);
  return () => {
    obs?.disconnect();
    obs = null;
    el.removeEventListener("mouseup", onMouseUp);
    el.removeEventListener("click", onBodyClick, true);
  };
}

let unbind: (() => void) | null = null;

// Rebind whenever the body element instance changes (article switch / the
// reopen race where bodyEl is null on first run and fills in later).
watch(
  () => props.bodyEl,
  (el) => {
    unbind?.();
    unbind = null;
    if (el) unbind = bind(el);
  },
  { immediate: true },
);

// Re-apply when the highlight set changes (create / delete / recolour reload).
watch(highlights, () => {
  if (props.bodyEl) applyMarks(props.bodyEl);
});

// Reload the stored highlights and reset the popover when the article changes.
watch(
  () => props.articleId,
  () => {
    highlights.value = [];
    editing.value = null;
    creating.value = null;
    reload();
  },
  { immediate: true },
);

onBeforeUnmount(() => {
  unbind?.();
  unbind = null;
});

// ── CRUD ────────────────────────────────────────────────────────────────────
// Persist the currently-pending selection with the colour + note that
// the merged popover just committed. Runs from HighlightPopover's
// onCreate callback; parent-owned so that reload / selection-cleanup
// stays in one place.
const createHighlight = async (color: string, note: string) => {
  const el = props.bodyEl;
  const cur = creating.value;
  if (!el || !cur) return;
  const ctx = captureContext(
    plainText(el),
    cur.textOffset,
    cur.textOffset + cur.quote.length,
  );
  try {
    await api.createHighlight({
      articleId: props.articleId,
      quote: cur.quote,
      prefix: ctx.prefix,
      suffix: ctx.suffix,
      textOffset: cur.textOffset,
      color,
      note,
    });
    window.getSelection()?.removeAllRanges();
    reload();
  } catch (e) {
    reportError(e);
  }
};

// Deleting a highlight runs behind an Undo window: it leaves the overlay at
// once, but is only removed from the database ~6s later unless the user takes it
// back. A snapshot is kept so Undo can restore it in reading order.
const deleteHighlight = (hl: Highlight) => {
  withUndo({
    text: t("highlights.deleted"),
    apply: () => (highlights.value = highlights.value.filter((h) => h.id !== hl.id)),
    commit: () => {
      api.deleteHighlight(hl.id).catch(reportError);
    },
    revert: () =>
      (highlights.value = [...highlights.value, hl].sort(
        (a, b) => a.textOffset - b.textOffset,
      )),
  });
};
</script>

<template>
  <!-- Selection popover: replaces the old separate colour toolbar. Renders
       a colour row + note textarea so the user can pick a colour and start
       typing without a second click. Escape aborts (no persist). -->
  <HighlightPopover
    v-if="creating && !editing"
    :pending="{ quote: creating.quote }"
    :x="creating.x"
    :y="creating.y"
    :on-close="() => (creating = null)"
    :on-create="createHighlight"
  />
  <!-- Edit popover for an already-persisted highlight. -->
  <HighlightPopover
    v-if="editingHl && editing"
    :key="editingHl.id"
    :hl="editingHl"
    :x="editing.x"
    :y="editing.y"
    :on-close="() => (editing = null)"
    :on-changed="reload"
    :on-delete="deleteHighlight"
  />
</template>

<style>
/* Focus flash for the highlight the user clicked on the highlights pane.
   Unscoped: the <mark> lives inside the article body which Vue owns via
   v-html, so a scoped selector wouldn't reach it. */
@keyframes hl-flash-anim {
  0%   { box-shadow: 0 0 0 0 var(--accent, oklch(0.57 0.105 78)); }
  60%  { box-shadow: 0 0 0 8px transparent; }
  100% { box-shadow: 0 0 0 0 transparent; }
}
mark.hl-flash {
  animation: hl-flash-anim 1.4s ease-out;
}
</style>
