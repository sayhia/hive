package services

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"

	"hive/internal/ai"
	"hive/internal/apperr"
	"hive/internal/appstate"
	"hive/internal/db"
	"hive/internal/events"
	"hive/internal/models"
	"hive/internal/sanitize"
	"hive/internal/translate"
)

// AIService exposes the streaming AI commands. Unlike papr's Tauri Channel<T>,
// Wails streams over named events: each call carries a streamID and tokens are
// emitted on the event "ai:<streamID>" (the frontend subscribes before calling
// and unsubscribes on the terminal {type:"done"} / {type:"error"}). The first
// parameter is context.Context so the frontend can cancel a stream (it is not
// part of the generated TS signature).
type AIService struct {
	app *appstate.State
}

// aiEmitter forwards AI events to the per-request Wails event and accumulates
// the delta text so the caller can persist a completed result.
type aiEmitter struct {
	seq *events.Sequencer
	acc strings.Builder
}

func (e *aiEmitter) emit(ev ai.Event) {
	if ev.Type == ai.EventDelta {
		e.acc.WriteString(ev.Data)
	}
	// Sequenced so the frontend can restore emit order: Wails dispatches each
	// event on its own goroutine, which otherwise lets rapid tokens race and
	// arrive shuffled. See events.Sequencer.
	e.seq.Emit(ev)
}

// Summarize streams an AI summary of one article; a completed summary is
// persisted to the article so the next open is instant.
func (s *AIService) Summarize(ctx context.Context, articleID int64, streamID string) error {
	title, text, err := db.ArticleText(ctx, s.app.DB.R, articleID)
	if err != nil {
		return err
	}
	// A title-only item gives the model nothing to summarize — bail rather than
	// let it fabricate (and then persist) a summary from the bare title.
	if strings.TrimSpace(text) == "" {
		return apperr.Code("noArticleBody")
	}
	cfg, err := ai.ConfigFromSettings(ctx, s.app.DB.R)
	if err != nil {
		return err
	}
	// LanguageFromSettings already maps the setting to a prompt directive; do
	// not wrap it in ResponseLanguage again — the directive string matches no
	// language code, so the second mapping always fell through to English and a
	// zh/ja user got English summaries.
	lang := ai.LanguageFromSettings(ctx, s.app.DB.R)
	em := &aiEmitter{seq: events.NewSequencer("ai:" + streamID)}
	if err := ai.Summarize(ctx, s.app.HTTP(), cfg, title, text, lang, em.emit); err != nil {
		return err
	}
	// Persist only a summary that streamed to completion (the panel wasn't
	// closed mid-stream) — caching a truncated fragment would show a broken
	// half-summary with no way to regenerate it.
	if ctx.Err() == nil && strings.TrimSpace(em.acc.String()) != "" {
		_ = db.SetAiSummary(ctx, s.app.DB.W, articleID, strings.TrimSpace(em.acc.String()))
	}
	return nil
}

// TestConnection sends one minimal completion through the same path summaries
// use (auth header, endpoint, model name, SSE parsing) and reports the model
// that answered plus the round-trip time, or the provider's own error text.
// It takes the values currently in the settings form, so the button works
// before saving; blanks fall back to the per-provider defaults. Time-boxed so
// a blackholed endpoint fails in 20s instead of waiting out the 300s
// streaming timeout. Costs a handful of tokens.
func (s *AIService) TestConnection(ctx context.Context, provider, apiKey, model, baseURL string) (*ai.TestOutcome, error) {
	cfg, err := ai.NewConfig(&provider, &apiKey, &model, &baseURL)
	if err != nil {
		return nil, err
	}
	out, err := ai.TestConnection(ctx, s.app.HTTP(), cfg)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Runtime-tunable AI knobs. Each has a compiled-in default and a settings-table
// key; the AI settings section writes the keys and Ask/Digest read them per
// request (clamped in intSetting), so they can be tuned without a rebuild.
// Defaults mirror the values the feature shipped with. The frontend also reads
// ai_digest_limit / ai_history_turns / ai_source_chip_cap (AIAssistant.vue).
const (
	defaultRAGLimit     = 20  // FTS-relevance hits for a keyword question
	defaultRecentLimit  = 40  // recent-article fallback when FTS finds nothing
	defaultDigestLimit  = 80  // recent articles the digest briefing synthesizes
	defaultSummaryChars = 500 // per-article summary/excerpt length fed to Ask
	defaultHistoryTurns = 6   // prior exchanges kept as conversation context

	settingRAGLimit     = "ai_rag_limit"
	settingRecentLimit  = "ai_recent_limit"
	settingDigestLimit  = "ai_digest_limit"
	settingSummaryChars = "ai_summary_chars"
	settingHistoryTurns = "ai_history_turns"
)

// intSetting reads an integer AI knob from the settings table, falling back to
// def when unset or unparseable, and clamps the result to [lo, hi]. The AI
// settings section writes these keys; Ask/Digest resolve them per request so a
// change takes effect without restarting.
func intSetting(ctx context.Context, q db.Querier, key string, def, lo, hi int) int {
	v, err := db.GetSetting(ctx, q, key)
	if err != nil || v == nil {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(*v))
	if err != nil {
		return def
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// aiSource is one retrieved article surfaced to the UI as a clickable citation.
// Emitted (JSON-encoded) as the sources event ahead of the streamed answer.
type aiSource struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	FeedTitle string `json:"feedTitle"`
}

// Ask answers a question using the user's subscribed articles as RAG context
// (FTS5 keyword retrieval), streaming the answer. Each article contributes a
// compact summary (cached AI summary if present, else a body excerpt) rather
// than its full body, so many articles fit in a bounded context. `history` is
// the prior conversation (alternating user/assistant turns, empty for the first
// question) so a follow-up keeps its context.
func (s *AIService) Ask(ctx context.Context, question string, history []ai.Message, streamID string) error {
	cfg, err := ai.ConfigFromSettings(ctx, s.app.DB.R)
	if err != nil {
		return err
	}
	q := s.app.DB.R
	ragLimit := intSetting(ctx, q, settingRAGLimit, defaultRAGLimit, 1, 200)
	recentLimit := intSetting(ctx, q, settingRecentLimit, defaultRecentLimit, 1, 200)
	summaryChars := intSetting(ctx, q, settingSummaryChars, defaultSummaryChars, 100, 4000)
	historyTurns := intSetting(ctx, q, settingHistoryTurns, defaultHistoryTurns, 0, 50)
	hits, err := db.SearchArticlesForRAG(ctx, q, question, int64(ragLimit), int64(summaryChars))
	if err != nil {
		return err
	}
	// FTS keyword retrieval finds nothing for questions that carry no content
	// terms to match — "what are today's headlines?", "what's new?". Rather than
	// answer "no articles were provided", fall back to the most recent articles
	// so overview/recency questions (and the suggested prompts) get a real
	// answer. A truly empty library still yields no hits here, correctly.
	// `recent` lets Ask frame the fallback context as a recent selection so the
	// model doesn't report the N articles as the user's complete set.
	recent := false
	if len(hits) == 0 {
		recent = true
		hits, err = db.RecentArticlesForRAG(ctx, q, int64(recentLimit), int64(summaryChars))
		if err != nil {
			return err
		}
	}
	var contextBuf strings.Builder
	for _, h := range hits {
		// Feed a compact summary per article — the cached AI summary if the
		// article has one, else a body excerpt (both assembled in the query) —
		// rather than the full body. Ask includes many articles, so summaries
		// keep the context and token spend bounded while preserving the gist,
		// and it avoids an N+1 (one ArticleText query per hit).
		summary := strings.TrimSpace(h.Summary)
		if summary == "" {
			continue
		}
		fmt.Fprintf(&contextBuf, "## %s — %s\n%s\n\n", h.Title, h.FeedTitle, summary)
	}
	// LanguageFromSettings already maps the setting to a prompt directive; do
	// not wrap it in ResponseLanguage again — the directive string matches no
	// language code, so the second mapping always fell through to English and a
	// zh/ja user got English summaries.
	lang := ai.LanguageFromSettings(ctx, s.app.DB.R)
	em := &aiEmitter{seq: events.NewSequencer("ai:" + streamID)}
	// Emit the retrieved articles first — sequenced ahead of the answer tokens,
	// so the frontend receives sources before the first delta and can show them
	// as clickable citations. The prompt still asks the model to name titles;
	// this surfaces the actual article ids so a click can open them.
	if len(hits) > 0 {
		srcs := make([]aiSource, 0, len(hits))
		for _, h := range hits {
			srcs = append(srcs, aiSource{ID: h.ID, Title: h.Title, FeedTitle: h.FeedTitle})
		}
		if b, err := json.Marshal(srcs); err == nil {
			em.emit(ai.Event{Type: ai.EventSources, Data: string(b)})
		}
	}
	return ai.Ask(ctx, s.app.HTTP(), cfg, question, contextBuf.String(), lang, history, recent, historyTurns, em.emit)
}

// Chat streams a plain assistant reply with no article context (pure chat),
// keeping the recent conversation for follow-ups.
func (s *AIService) Chat(ctx context.Context, question string, history []ai.Message, streamID string) error {
	cfg, err := ai.ConfigFromSettings(ctx, s.app.DB.R)
	if err != nil {
		return err
	}
	historyTurns := intSetting(ctx, s.app.DB.R, settingHistoryTurns, defaultHistoryTurns, 0, 50)
	lang := ai.LanguageFromSettings(ctx, s.app.DB.R)
	em := &aiEmitter{seq: events.NewSequencer("ai:" + streamID)}
	return ai.Chat(ctx, s.app.HTTP(), cfg, question, lang, history, historyTurns, em.emit)
}

// Digest streams an AI briefing that synthesizes the most recent articles by theme.
func (s *AIService) Digest(ctx context.Context, streamID string) error {
	cfg, err := ai.ConfigFromSettings(ctx, s.app.DB.R)
	if err != nil {
		return err
	}
	digLimit := intSetting(ctx, s.app.DB.R, settingDigestLimit, defaultDigestLimit, 1, 300)
	items, err := db.DigestSource(ctx, s.app.DB.R, int64(digLimit))
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return apperr.Code("noArticles")
	}
	var corpus strings.Builder
	for _, it := range items {
		fmt.Fprintf(&corpus, "- [%s] %s: %s\n", it.FeedTitle, it.Title, truncateRunes(it.Text, 400))
	}
	// LanguageFromSettings already maps the setting to a prompt directive; do
	// not wrap it in ResponseLanguage again — the directive string matches no
	// language code, so the second mapping always fell through to English and a
	// zh/ja user got English summaries.
	lang := ai.LanguageFromSettings(ctx, s.app.DB.R)
	em := &aiEmitter{seq: events.NewSequencer("ai:" + streamID)}
	return ai.Digest(ctx, s.app.HTTP(), cfg, corpus.String(), lang, em.emit)
}

// Translate translates one article's body into lang using engine ("llm" /
// "google" / "deepl" / "bing"), streaming progress per batch over the event
// "translate:<streamID>". The reassembled, sanitized result is cached on the
// article and reused on the next open. Each batch is re-sanitized in the
// service since engine output (LLM or MT) is untrusted.
func (s *AIService) Translate(ctx context.Context, articleID int64, lang, engine, streamID string) error {
	detail, err := db.GetArticle(ctx, s.app.DB.R, articleID)
	if err != nil {
		return err
	}
	// Translate the richest body available: extracted full text if present,
	// otherwise the feed's own HTML.
	source := ""
	if detail.ExtractedHTML != nil && strings.TrimSpace(*detail.ExtractedHTML) != "" {
		source = *detail.ExtractedHTML
	} else if detail.ContentHTML != nil {
		source = *detail.ContentHTML
	}
	if strings.TrimSpace(source) == "" {
		return apperr.Code("noArticleBody")
	}
	target := strings.TrimSpace(lang)
	if target == "" {
		target = translateTargetLang(ctx, s.app.DB.R)
	}
	// Normalise the engine so the cache key matches what the reader queries with
	// (translate.Translate maps any unknown engine to the LLM path internally).
	switch engine {
	case "google", "deepl", "bing":
	default:
		engine = "llm"
	}

	// Sequenced like the AI stream so reordered Wails delivery can't shuffle the
	// translated batches (see events.Sequencer).
	seq := events.NewSequencer("translate:" + streamID)
	emit := func(e translate.Event) {
		if e.Data.HTML != "" {
			e.Data.HTML = sanitize.Sanitize(e.Data.HTML, "")
		}
		seq.Emit(e)
	}
	result, title, err := translate.Translate(ctx, s.app.HTTP(), s.app.DB.R, source, detail.Title, target, engine, emit)
	if err != nil {
		return err
	}
	final := strings.TrimSpace(sanitize.Sanitize(result, ""))
	if final != "" && ctx.Err() == nil {
		var titlePtr *string
		if t := strings.TrimSpace(title); t != "" {
			titlePtr = &t
		}
		_ = db.UpsertTranslation(ctx, s.app.DB.W, articleID, target, engine, titlePtr, final)
	}
	return nil
}

// TranslateArticlePreview translates only the lightweight fields the article
// list shows — title + snippet. It does not reuse Translate: the reader call
// streams full article HTML, whereas list previews are small one-shot jobs the
// frontend schedules as rows enter the virtualized viewport, cached
// independently per (article, lang, engine) so scrolling back is free.
func (s *AIService) TranslateArticlePreview(ctx context.Context, articleID int64, lang, engine string) (*models.ArticlePreviewTranslation, error) {
	target := strings.TrimSpace(lang)
	if target == "" {
		target = translateTargetLang(ctx, s.app.DB.R)
	}
	switch engine {
	case "google", "deepl", "bing":
	default:
		engine = "llm"
	}

	if title, snippet, ok, err := db.GetPreviewTranslation(ctx, s.app.DB.R, articleID, target, engine); err != nil {
		return nil, err
	} else if ok {
		return &models.ArticlePreviewTranslation{
			ArticleID: articleID, Title: title, Snippet: snippet, Lang: target, Engine: engine,
		}, nil
	}

	srcTitle, srcSnippet, err := db.ArticlePreviewText(ctx, s.app.DB.R, articleID)
	if err != nil {
		return nil, err
	}
	// The snippet rides through the engine as a single tiny paragraph; the
	// title goes through Translate's own title path. html.EscapeString keeps a
	// snippet containing markup-ish text from being parsed as tags.
	frag := "<p>" + html.EscapeString(srcSnippet) + "</p>"
	body, trTitle, err := translate.Translate(ctx, s.app.HTTP(), s.app.DB.R, frag, srcTitle, target, engine, nil)
	if err != nil {
		return nil, err
	}
	outTitle := strings.TrimSpace(trTitle)
	outSnippet := strings.TrimSpace(sanitize.HTMLToText(body))
	if outTitle == "" && outSnippet == "" {
		return nil, apperr.Code("translateFailed")
	}
	if err := db.SetPreviewTranslation(ctx, s.app.DB.W, articleID, target, engine, outTitle, outSnippet); err != nil {
		return nil, err
	}
	return &models.ArticlePreviewTranslation{
		ArticleID: articleID, Title: outTitle, Snippet: outSnippet, Lang: target, Engine: engine,
	}, nil
}

// translateTargetLang resolves the article-translation target: the dedicated
// translate_target_lang setting, falling back to the UI language, then English.
func translateTargetLang(ctx context.Context, q db.Querier) string {
	if v, _ := db.GetSetting(ctx, q, "translate_target_lang"); v != nil && strings.TrimSpace(*v) != "" {
		return *v
	}
	if v, _ := db.GetSetting(ctx, q, "language"); v != nil && strings.TrimSpace(*v) != "" {
		return *v
	}
	return "en"
}
