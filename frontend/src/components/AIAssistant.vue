<script setup lang="ts">
// App-level AI assistant: three library-wide modes the per-article summary
// drawer can't cover — Ask (RAG Q&A over the user's subscriptions), Chat (a plain
// assistant conversation, no feeds/articles), and Digest (a synthesized briefing
// of recent articles). All stream tokens over the same channel as the summary and
// render sanitized markdown. Mounted at the App level, so it works with no
// article open.
//
// Ask and Chat are multi-turn conversations sharing one useConversation factory:
// each question is a turn (question + streamed answer, plus retrieved sources for
// Ask), prior turns are threaded back as history so follow-ups keep context, and
// an in-flight answer can be stopped (cancelling the backend generation, not just
// the UI). They differ only in the streaming call — api.aiAsk vs api.aiChat.

import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import * as api from "../api";
import type { AiChatTurn, AiEvent, AiSource } from "../types";
import { useUi } from "../stores/ui";
import { useFocusTrap } from "../composables/useFocusTrap";
import { reportError, toast } from "../stores/toasts";
import { renderMarkdown } from "../lib/markdown";
import { makeLinkClickHandler } from "../lib/readerLinks";
import Icon from "./Icon.vue";

const props = defineProps<{ initialMode?: "ask" | "digest" | "chat" }>();
const emit = defineEmits<{
  (e: "close"): void;
  (e: "open-settings", section?: string): void;
}>();

const { t, tm, rt } = useI18n();
const ui = useUi();

const dialogRef = ref<HTMLDivElement | null>(null);
useFocusTrap(dialogRef);

const mode = ref<"ask" | "digest" | "chat">(props.initialMode ?? "chat");

// The digest corpus size shown in the scope caption. Read from the
// ai_digest_limit setting (default 80) so it matches what the backend actually
// uses; the AI settings section lets the user change it.
const digestLimit = ref(80);

// noKey → link to settings; noArticles → the digest has nothing to work with
// (Ask never raises it); anything else → a plain retry.
type FailReason = "noKey" | "noArticles" | "generic";

function reasonFromError(e: unknown): FailReason {
  const code = e && typeof e === "object" && "code" in e ? (e as { code?: string }).code : undefined;
  if (code === "noAiKey") return "noKey";
  if (code === "noArticles") return "noArticles";
  return "generic";
}

// Parse an integer setting value, falling back to def and clamping to [lo, hi].
function clampInt(v: string | null, def: number, lo: number, hi: number): number {
  const n = v == null ? NaN : parseInt(v, 10);
  if (Number.isNaN(n)) return def;
  return Math.min(hi, Math.max(lo, n));
}

// ── Digest: a single streaming task (accumulate deltas, track busy, classify
// failures). A per-run sequence guards a stale run overwriting a newer one. ──
interface StreamState {
  text: string;
  busy: boolean;
  failed: boolean;
  reason: FailReason;
}
function useStream() {
  const state = reactive<StreamState>({ text: "", busy: false, failed: false, reason: "generic" });
  let seq = 0;
  function run(start: (onToken: (e: AiEvent) => void) => Promise<void>) {
    const mine = ++seq;
    state.text = "";
    state.failed = false;
    state.busy = true;
    let sawError = false;
    start((ev) => {
      if (seq !== mine) return;
      if (ev.type === "delta") state.text += ev.data;
      else if (ev.type === "error") {
        sawError = true;
        state.reason = "generic";
        state.failed = true;
      }
    })
      .then(() => {
        // A resolved-but-empty stream is surfaced as a failure so the user gets
        // a retry instead of a blank panel.
        if (seq === mine && !state.text && !sawError) {
          state.reason = "generic";
          state.failed = true;
        }
      })
      .catch((e) => {
        if (seq !== mine || sawError) return;
        state.reason = reasonFromError(e);
        state.failed = true;
        // noKey / noArticles render their own guidance, so only the generic
        // path also needs a toast.
        if (state.reason === "generic") reportError(e);
      })
      .finally(() => {
        if (seq === mine) state.busy = false;
      });
  }
  return { state, run };
}
const digest = useStream();
const digestHtml = computed(() => (digest.state.text ? renderMarkdown(digest.state.text) : ""));

// ── Multi-turn conversation: Ask (RAG over subscriptions) and Chat (plain) ──
interface ConvTurn {
  question: string;
  answer: string;
  html: string;
  sources: AiSource[];
  sourcesExpanded: boolean;
  busy: boolean;
  failed: boolean;
  reason: FailReason;
  cancelled: boolean;
}
// A generous RAG limit can return dozens of sources; show the first few as chips
// and fold the rest behind a "+N more" toggle. Read from ai_source_chip_cap
// (default 12); adjustable in the AI settings section.
const sourceChipCap = ref(12);
// How many recent completed exchanges to send back as context — enough for
// follow-ups, capped so a long conversation can't grow the input unbounded. Read
// from ai_history_turns (default 6); the backend re-clamps as a backstop.
const historyTurns = ref(6);

const question = ref("");
const onLinkClick = makeLinkClickHandler(null);

// Suggested prompts for the Ask empty state (an i18n array). `rt` resolves each
// raw message so it works whether tm returns strings or compiled messages.
const askPresets = computed<string[]>(() => {
  const raw = tm("ai.askSuggestions") as unknown[];
  return Array.isArray(raw) ? raw.map((m) => rt(m as never)) : [];
});

// One multi-turn conversation, parameterized by the streaming call. Ask and Chat
// each own an instance; the only difference is the call (Ask retrieves article
// context and emits sources, Chat is plain), so the turn model is shared.
function useConversation(call: typeof api.aiAsk) {
  const turns = reactive<ConvTurn[]>([]);
  // The in-flight answer's handle, so Stop / close / reset cancel the backend
  // generation instead of leaving it running (and billing) in the dark.
  let handle: { cancel: () => void } | null = null;
  const busy = computed(() => turns.some((tn) => tn.busy));

  // The last historyTurns completed (non-failed, answered) exchanges, as
  // alternating user/assistant messages — the context a follow-up sees.
  function history(): AiChatTurn[] {
    const recent = turns.filter((tn) => !tn.failed && tn.answer).slice(-historyTurns.value);
    const h: AiChatTurn[] = [];
    for (const tn of recent) {
      h.push({ role: "user", content: tn.question });
      h.push({ role: "assistant", content: tn.answer });
    }
    return h;
  }

  function run(q: string) {
    const prior = history();
    const turn = reactive<ConvTurn>({
      question: q, answer: "", html: "", sources: [], sourcesExpanded: false,
      busy: true, failed: false, reason: "generic", cancelled: false,
    });
    turns.push(turn);
    scrollSoon();
    let sawError = false;
    const h = call(q, prior, (ev) => {
      if (ev.type === "delta") {
        turn.answer += ev.data;
        // Re-render only this turn's markdown per token (O(1) in conversation length).
        turn.html = renderMarkdown(turn.answer);
        scrollSoon();
      } else if (ev.type === "sources") {
        // Ask emits retrieved sources; Chat never does (turn.sources stays empty).
        try {
          turn.sources = JSON.parse(ev.data) as AiSource[];
        } catch {
          /* malformed sources payload — just show the answer without citations */
        }
      } else if (ev.type === "error") {
        sawError = true;
        turn.reason = "generic";
        turn.failed = true;
      }
    });
    handle = h;
    h.then(() => {
      // A resolved-but-empty stream (no tokens, no error, not stopped) → retryable failure.
      if (!turn.answer && !sawError && !turn.cancelled) {
        turn.reason = "generic";
        turn.failed = true;
      }
    })
      .catch((e) => {
        // A user-initiated Stop rejects the promise too; that isn't an error.
        if (sawError || turn.cancelled) return;
        turn.reason = reasonFromError(e);
        turn.failed = true;
        if (turn.reason === "generic") reportError(e);
      })
      .finally(() => {
        turn.busy = false;
        if (handle === h) handle = null;
      });
  }

  function stop() {
    const tn = turns.find((x) => x.busy);
    if (tn) tn.cancelled = true;
    handle?.cancel();
  }
  function retry(turn: ConvTurn) {
    if (busy.value) return;
    const i = turns.indexOf(turn);
    if (i >= 0) turns.splice(i, 1);
    run(turn.question);
  }
  function cancel() {
    handle?.cancel();
    handle = null;
  }
  function reset() {
    cancel();
    turns.splice(0, turns.length);
  }
  return { turns, busy, run, stop, retry, cancel, reset };
}

const ask = useConversation(api.aiAsk);
const chat = useConversation(api.aiChat);
function activeConv() {
  return mode.value === "chat" ? chat : ask;
}
// Template-facing views of the active conversation (unwrapped to a plain array /
// boolean so the template doesn't see nested refs).
const activeTurns = computed(() => activeConv().turns);
const activeBusy = computed(() => activeConv().busy.value);

function submitCurrent() {
  const c = activeConv();
  const q = question.value.trim();
  if (!q || c.busy.value) return;
  question.value = "";
  resetTextareaHeight();
  c.run(q);
}
function stopCurrent() {
  activeConv().stop();
}
function retryTurn(turn: ConvTurn) {
  activeConv().retry(turn);
}
function askPreset(q: string) {
  if (ask.busy.value) return;
  ask.run(q);
}
function newConversation() {
  activeConv().reset();
  question.value = "";
  resetTextareaHeight();
  nextTick(() => textareaRef.value?.focus());
}

function copyTurn(turn: ConvTurn) {
  if (!turn.answer) return;
  navigator.clipboard.writeText(turn.answer).then(
    () => toast.show(t("reader.textCopied")),
    () => {},
  );
}

function openSource(id: number) {
  ui.openArticle(id);
  emit("close");
}

// ── autoscroll: follow the stream only while the user is at the bottom, so
// scrolling up to re-read isn't yanked back down by incoming tokens. ──
const scrollRef = ref<HTMLElement | null>(null);
let stick = true;
function onScroll() {
  const el = scrollRef.value;
  if (el) stick = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
}
function scrollSoon() {
  if (!stick) return;
  nextTick(() => {
    const el = scrollRef.value;
    if (el) el.scrollTop = el.scrollHeight;
  });
}

// ── textarea auto-grow (up to a cap, then it scrolls) ──
const textareaRef = ref<HTMLTextAreaElement | null>(null);
const TEXTAREA_MAX = 132;
function autoGrow() {
  const el = textareaRef.value;
  if (!el) return;
  el.style.height = "auto";
  el.style.height = Math.min(el.scrollHeight, TEXTAREA_MAX) + "px";
}
function resetTextareaHeight() {
  const el = textareaRef.value;
  if (el) el.style.height = "auto";
}

// Enter sends; Shift+Enter inserts a newline. isComposing guards IME input (a
// Chinese/Japanese candidate is confirmed with Enter — that must not send).
function onKeydown(e: KeyboardEvent) {
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
    e.preventDefault();
    submitCurrent();
  }
}

// Digest is expensive (it synthesizes ~30 articles), so it isn't pre-run; the
// first time the user lands on the tab kicks it off, and the header re-runs it.
function ensureDigest() {
  if (digest.state.text || digest.state.busy || digest.state.failed) return;
  digest.run((onToken) => api.aiDigest(onToken));
}
function regenerateDigest() {
  if (!digest.state.busy) digest.run((onToken) => api.aiDigest(onToken));
}

function openSettings() {
  emit("open-settings", "ai");
}

watch(mode, (m) => {
  if (m === "digest") ensureDigest();
});

// Escape closes from anywhere inside (capture so it beats the global map).
function onWindowKey(e: KeyboardEvent) {
  if (e.key === "Escape") {
    e.stopPropagation();
    emit("close");
  }
}
onMounted(() => {
  window.addEventListener("keydown", onWindowKey, true);
  // Load the runtime-tunable knobs the AI settings section writes. The backend
  // reads its own copy per request; these drive the matching UI behaviour
  // (chip cap, history depth, digest scope caption).
  Promise.all([
    api.getSetting("ai_source_chip_cap"),
    api.getSetting("ai_history_turns"),
    api.getSetting("ai_digest_limit"),
  ])
    .then(([chip, hist, dig]) => {
      sourceChipCap.value = clampInt(chip, 12, 1, 100);
      historyTurns.value = clampInt(hist, 6, 0, 50);
      digestLimit.value = clampInt(dig, 80, 1, 300);
    })
    .catch(() => {});
  if (mode.value === "digest") ensureDigest();
});
onUnmounted(() => {
  window.removeEventListener("keydown", onWindowKey, true);
  // Closing the panel mid-stream must stop the backend generation, not leave it
  // running (and billing) to completion in the background — for both modes.
  ask.cancel();
  chat.cancel();
});
</script>

<template>
  <Teleport to="body">
    <div class="modal-backdrop" @click="emit('close')">
      <div
        class="modal modal-ai"
        ref="dialogRef"
        role="dialog"
        aria-modal="true"
        :aria-label="t('ai.title')"
        @click.stop
      >
        <div class="ai-asst-head">
          <span class="accent-ico"><Icon name="sparkle-fill" :size="16" /></span>
          <div class="ai-asst-tabs" role="tablist" :aria-label="t('ai.title')">
            <button
              role="tab"
              :aria-selected="mode === 'chat'"
              :class="['ai-asst-tab', { active: mode === 'chat' }]"
              @click="mode = 'chat'"
            >
              <Icon name="sparkle" :size="13" /> {{ t("ai.chat") }}
            </button>
            <button
              role="tab"
              :aria-selected="mode === 'ask'"
              :class="['ai-asst-tab', { active: mode === 'ask' }]"
              @click="mode = 'ask'"
            >
              <Icon name="search" :size="13" /> {{ t("ai.ask") }}
            </button>
            <button
              role="tab"
              :aria-selected="mode === 'digest'"
              :class="['ai-asst-tab', { active: mode === 'digest' }]"
              @click="mode = 'digest'"
            >
              <Icon name="list" :size="13" /> {{ t("ai.digest") }}
            </button>
          </div>
          <button
            v-if="(mode === 'ask' || mode === 'chat') && activeTurns.length"
            class="tb-btn"
            @click="newConversation"
            :title="t('ai.newChat')"
            :aria-label="t('ai.newChat')"
          >
            <Icon name="plus" :size="15" />
          </button>
          <button
            class="tb-btn"
            @click="emit('close')"
            :title="t('common.close')"
            :aria-label="t('common.close')"
          >
            <Icon name="x" :size="15" />
          </button>
        </div>

        <!-- Ask (RAG over subscriptions) & Chat (plain) share the conversation UI -->
        <div v-if="mode === 'ask' || mode === 'chat'" class="ai-asst-body">
          <div class="ai-asst-scroll" ref="scrollRef" @scroll="onScroll">
            <div v-if="activeTurns.length === 0" class="ai-asst-empty">
              <template v-if="mode === 'ask'">
                <Icon name="search" :size="22" />
                <p>{{ t("ai.askEmpty") }}</p>
                <div v-if="askPresets.length" class="ai-asst-presets">
                  <button
                    v-for="(p, i) in askPresets"
                    :key="i"
                    type="button"
                    class="ai-asst-preset"
                    @click="askPreset(p)"
                  >
                    <Icon name="sparkle" :size="12" /> {{ p }}
                  </button>
                </div>
              </template>
              <template v-else>
                <Icon name="sparkle" :size="22" />
                <p>{{ t("ai.chatEmpty") }}</p>
              </template>
            </div>
            <div v-for="(turn, i) in activeTurns" :key="i" class="ai-asst-turn">
              <div class="ai-asst-q">{{ turn.question }}</div>
              <div v-if="turn.busy && !turn.answer" class="ai-loading">
                <span class="ai-dot" /><span class="ai-dot" /><span class="ai-dot" />
                <span :style="{ marginLeft: '4px' }">{{ t("ai.thinking") }}</span>
              </div>
              <div v-else-if="turn.failed" class="ai-error">
                <Icon name="alert" :size="18" />
                <span v-if="turn.reason === 'noKey'">{{ t("error.noAiKey") }}</span>
                <span v-else>{{ t("ai.askError") }}</span>
                <button v-if="turn.reason === 'noKey'" class="empty-retry" @click="openSettings">
                  <Icon name="settings" :size="12" /> {{ t("reader.aiOpenSettings") }}
                </button>
                <button v-else class="empty-retry" @click="retryTurn(turn)">
                  <Icon name="refresh" :size="12" /> {{ t("common.retry") }}
                </button>
              </div>
              <template v-else>
                <div class="ai-prose" @click="onLinkClick" v-html="turn.html" />
                <div v-if="turn.busy" class="ai-generating">
                  <span class="ai-dot" /> {{ t("reader.aiGenerating") }}
                </div>
                <template v-else>
                  <div v-if="turn.sources.length" class="ai-asst-sources">
                    <span class="ai-asst-sources-label">{{ t("ai.sources") }}</span>
                    <button
                      v-for="src in (turn.sourcesExpanded ? turn.sources : turn.sources.slice(0, sourceChipCap))"
                      :key="src.id"
                      type="button"
                      class="ai-asst-source"
                      :title="`${src.title} — ${src.feedTitle}`"
                      @click="openSource(src.id)"
                    >
                      <span class="ai-asst-source-title">{{ src.title }}</span>
                      <span class="ai-asst-source-feed">{{ src.feedTitle }}</span>
                    </button>
                    <button
                      v-if="turn.sources.length > sourceChipCap"
                      type="button"
                      class="ai-asst-source ai-asst-source-more"
                      @click="turn.sourcesExpanded = !turn.sourcesExpanded"
                    >
                      {{ turn.sourcesExpanded ? t("ai.sourcesLess") : t("ai.sourcesMore", { count: turn.sources.length - sourceChipCap }) }}
                    </button>
                  </div>
                  <div class="ai-asst-turn-actions">
                    <button class="s-btn" @click="copyTurn(turn)">
                      <Icon name="copy" :size="12" /> {{ t("common.copy") }}
                    </button>
                  </div>
                </template>
              </template>
            </div>
          </div>
          <form class="ai-asst-input" @submit.prevent="submitCurrent">
            <textarea
              ref="textareaRef"
              v-model="question"
              autofocus
              rows="1"
              :placeholder="
                mode === 'chat'
                  ? activeTurns.length
                    ? t('ai.chatFollowUp')
                    : t('ai.chatPlaceholder')
                  : activeTurns.length
                    ? t('ai.askFollowUp')
                    : t('ai.askPlaceholder')
              "
              :aria-label="mode === 'chat' ? t('ai.chat') : t('ai.ask')"
              @keydown="onKeydown"
              @input="autoGrow"
            />
            <button
              v-if="activeBusy"
              type="button"
              class="s-btn ai-asst-send ai-asst-stop"
              @click="stopCurrent"
              :aria-label="t('ai.stop')"
              :title="t('ai.stop')"
            >
              <Icon name="x" :size="14" />
            </button>
            <button
              v-else
              type="submit"
              class="s-btn primary ai-asst-send"
              :disabled="!question.trim()"
              :aria-label="t('ai.send')"
              :title="t('ai.send')"
            >
              <Icon name="send" :size="14" />
            </button>
          </form>
          <p class="ai-asst-foot">{{ mode === "chat" ? t("ai.chatHint") : t("ai.askHint") }}</p>
        </div>

        <!-- Digest: a themed briefing of recent articles -->
        <div v-else class="ai-asst-body">
          <p class="ai-asst-scope">{{ t("ai.digestScope", { count: digestLimit }) }}</p>
          <div class="ai-asst-scroll">
            <div v-if="digest.state.busy && !digest.state.text" class="ai-loading">
              <span class="ai-dot" /><span class="ai-dot" /><span class="ai-dot" />
              <span :style="{ marginLeft: '4px' }">{{ t("ai.digestThinking") }}</span>
            </div>
            <div v-else-if="digest.state.failed" class="ai-error">
              <Icon name="alert" :size="18" />
              <span v-if="digest.state.reason === 'noKey'">{{ t("error.noAiKey") }}</span>
              <span v-else-if="digest.state.reason === 'noArticles'">{{ t("ai.digestNoArticles") }}</span>
              <span v-else>{{ t("ai.digestError") }}</span>
              <button v-if="digest.state.reason === 'noKey'" class="empty-retry" @click="openSettings">
                <Icon name="settings" :size="12" /> {{ t("reader.aiOpenSettings") }}
              </button>
              <button
                v-else-if="digest.state.reason !== 'noArticles'"
                class="empty-retry"
                @click="regenerateDigest"
              >
                <Icon name="refresh" :size="12" /> {{ t("common.retry") }}
              </button>
            </div>
            <template v-else-if="digest.state.text">
              <div class="ai-prose" @click="onLinkClick" v-html="digestHtml" />
              <div v-if="digest.state.busy" class="ai-generating">
                <span class="ai-dot" /> {{ t("reader.aiGenerating") }}
              </div>
              <div v-else class="ai-asst-digest-actions">
                <button class="s-btn" @click="regenerateDigest">
                  <Icon name="refresh" :size="12" /> {{ t("reader.aiRegenerate") }}
                </button>
              </div>
            </template>
          </div>
          <p class="ai-asst-foot">{{ t("reader.aiDisclaimer") }}</p>
        </div>
      </div>
    </div>
  </Teleport>
</template>
