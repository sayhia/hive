package translate

import (
	"reflect"
	"strings"
	"testing"
)

// ── chunkBlocks ──────────────────────────────────────────────────────────────

func TestGroupsMultipleBlocksIntoOneBatchUnderBudget(t *testing.T) {
	out := chunkBlocks("<p>a</p><p>b</p><p>c</p>", 1000)
	want := []string{"<p>a</p><p>b</p><p>c</p>"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %#v, want %#v", out, want)
	}
}

func TestSplitsIntoBatchesWhenOverBudget(t *testing.T) {
	// Each `<p>x</p>` is 8 bytes; a budget of 8 forces one block per batch.
	out := chunkBlocks("<p>a</p><p>b</p><p>c</p>", 8)
	want := []string{"<p>a</p>", "<p>b</p>", "<p>c</p>"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %#v, want %#v", out, want)
	}
	// Reassembly reproduces the source block sequence.
	if got := strings.Join(out, ""); got != "<p>a</p><p>b</p><p>c</p>" {
		t.Fatalf("reassembly = %q", got)
	}
}

func TestNeverSplitsASingleOversizedBlock(t *testing.T) {
	big := "<p>" + strings.Repeat("x", 100) + "</p>"
	out := chunkBlocks(big, 8)
	want := []string{big}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %#v, want %#v", out, want)
	}
}

func TestUnwrapsASingleGenericContainer(t *testing.T) {
	// The wrapping <div> is dropped so its children can be batched.
	out := chunkBlocks("<div><p>a</p><p>b</p></div>", 8)
	want := []string{"<p>a</p>", "<p>b</p>"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %#v, want %#v", out, want)
	}
}

func TestUnwrapsNestedGenericContainers(t *testing.T) {
	out := chunkBlocks("<div><section><p>a</p><p>b</p></section></div>", 8)
	want := []string{"<p>a</p>", "<p>b</p>"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %#v, want %#v", out, want)
	}
}

func TestDoesNotUnwrapStructuralContainers(t *testing.T) {
	// A <ul> carries list structure; its <li> children must stay wrapped.
	out := chunkBlocks("<ul><li>a</li><li>b</li></ul>", 1000)
	want := []string{"<ul><li>a</li><li>b</li></ul>"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %#v, want %#v", out, want)
	}
}

func TestKeepsImagesInsideTheirBlock(t *testing.T) {
	html := `<p>intro</p><figure><img src="https://e.com/a.png"></figure>`
	out := chunkBlocks(html, 8)
	if len(out) != 2 {
		t.Fatalf("got %d batches, want 2: %#v", len(out), out)
	}
	if !strings.Contains(out[1], "https://e.com/a.png") {
		t.Fatalf("image URL missing from batch: %q", out[1])
	}
}

func TestEmptyOrWhitespaceInputYieldsNoBatches(t *testing.T) {
	if got := chunkBlocks("", 1000); len(got) != 0 {
		t.Fatalf("empty: got %#v", got)
	}
	if got := chunkBlocks("   \n  ", 1000); len(got) != 0 {
		t.Fatalf("whitespace: got %#v", got)
	}
}

func TestBareTextIsASingleBatch(t *testing.T) {
	out := chunkBlocks("just text", 1000)
	want := []string{"just text"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %#v, want %#v", out, want)
	}
}

func TestGreedyBatchingPacksUntilBudgetThenBreaks(t *testing.T) {
	// Two 8-byte blocks fit in a 16-byte budget; the third forces a new batch.
	out := chunkBlocks("<p>a</p><p>b</p><p>c</p>", 16)
	want := []string{"<p>a</p><p>b</p>", "<p>c</p>"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %#v, want %#v", out, want)
	}
	if got := strings.Join(out, ""); got != "<p>a</p><p>b</p><p>c</p>" {
		t.Fatalf("reassembly = %q", got)
	}
}

// ── translateSystemPrompt ────────────────────────────────────────────────────

func TestPromptNamesTheTargetLanguage(t *testing.T) {
	p := translateSystemPrompt("Simplified Chinese")
	if !strings.Contains(p, "Simplified Chinese") {
		t.Fatalf("missing language: %s", p)
	}
}

func TestPromptDemandsHTMLBePreserved(t *testing.T) {
	p := strings.ToLower(translateSystemPrompt("Japanese"))
	if !strings.Contains(p, "html") {
		t.Fatalf("no HTML mention: %s", p)
	}
	if !strings.Contains(p, "preserve") && !strings.Contains(p, "keep") {
		t.Fatalf("no preserve directive: %s", p)
	}
}

// ── stripCodeFence ───────────────────────────────────────────────────────────

func TestStripsLanguageTaggedFence(t *testing.T) {
	if got := stripCodeFence("```html\n<p>x</p>\n```"); got != "<p>x</p>" {
		t.Fatalf("got %q", got)
	}
}

func TestStripsBareFence(t *testing.T) {
	if got := stripCodeFence("```\n<p>x</p>\n```"); got != "<p>x</p>" {
		t.Fatalf("got %q", got)
	}
}

func TestLeavesUnfencedContentUntouched(t *testing.T) {
	if got := stripCodeFence("<p>x</p>"); got != "<p>x</p>" {
		t.Fatalf("got %q", got)
	}
	if got := stripCodeFence("  <p>x</p>  "); got != "<p>x</p>" {
		t.Fatalf("got %q", got)
	}
}

// ── languageName ─────────────────────────────────────────────────────────────

func TestLanguageNameMapsCodesWithEnglishFallback(t *testing.T) {
	cases := map[string]string{
		"zh": "Simplified Chinese",
		"ja": "Japanese",
		"en": "English",
		"xx": "English",
	}
	for code, want := range cases {
		if got := languageName(code); got != want {
			t.Fatalf("languageName(%q) = %q, want %q", code, got, want)
		}
	}
}

// ── text-node extraction / rewrite (the machine-translation path) ─────────────
//
// The network call sits between collecting the source text and writing the
// result back; rewriteWith stands in for it with a synchronous mapper so the
// structure-preserving collect → apply → serialize round-trip can be tested.

func rewriteWith(html string, f func([]string) []string) string {
	nodes := parseFragmentChildren(html)
	slots, cores := collectText(nodes)
	translated := f(cores)
	apply(slots, translated)
	return serializeFragment(nodes)
}

func upper(cores []string) []string {
	out := make([]string, len(cores))
	for i, c := range cores {
		out[i] = strings.ToUpper(c)
	}
	return out
}

func TestRewritePreservesInlineTagsAndAttributes(t *testing.T) {
	out := rewriteWith(`<p>Hello <a href="/x">world</a></p>`, upper)
	if out != `<p>HELLO <a href="/x">WORLD</a></p>` {
		t.Fatalf("got %q", out)
	}
}

func TestRewritePreservesWhitespaceBetweenInlineWords(t *testing.T) {
	// The trailing space on "a " must survive so the words stay separated.
	out := rewriteWith("<p>a <b>b</b></p>", upper)
	if out != "<p>A <b>B</b></p>" {
		t.Fatalf("got %q", out)
	}
}

func TestRewriteSkipsCodeAndPreText(t *testing.T) {
	out := rewriteWith("<p>run</p><pre>let x = 1;</pre>", upper)
	if out != "<p>RUN</p><pre>let x = 1;</pre>" {
		t.Fatalf("got %q", out)
	}
}

func TestRewriteKeepsImagesAndBlocksWithNoText(t *testing.T) {
	// No translatable text nodes: collectText returns nothing, and the fragment
	// serializes back unchanged (Go renders the void <img> self-closed).
	html := `<figure><img src="https://e.com/a.png"></figure>`
	want := `<figure><img src="https://e.com/a.png"/></figure>`
	if out := rewriteWith(html, upper); out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestRewriteToleratesAShortTranslationCount(t *testing.T) {
	// An engine returning too few segments must not panic or misalign: the
	// unmatched node keeps its source text.
	out := rewriteWith("<p>one</p><p>two</p>", func([]string) []string {
		return []string{"UNO"}
	})
	if out != "<p>UNO</p><p>two</p>" {
		t.Fatalf("got %q", out)
	}
}

func TestRewriteToleratesAnOverlongTranslationCount(t *testing.T) {
	// Extra segments are dropped rather than misaligned (apply stops at the
	// shorter side).
	out := rewriteWith("<p>one</p>", func([]string) []string {
		return []string{"UNO", "DOS", "TRES"}
	})
	if out != "<p>UNO</p>" {
		t.Fatalf("got %q", out)
	}
}

// ── per-engine budgets / language codes ──────────────────────────────────────

func TestChunkBudgetIsTunedPerEngine(t *testing.T) {
	// Google (GET query) is the smallest; DeepL/Bing (POST) the largest; the LLM
	// uses the shared token-bounded default; an unknown engine falls back to it.
	cases := map[string]int{
		"google": 1500,
		"deepl":  8000,
		"bing":   8000,
		"llm":    translateChunkBudget,
		"???":    translateChunkBudget,
	}
	for engine, want := range cases {
		if got := chunkBudget(engine); got != want {
			t.Fatalf("chunkBudget(%q) = %d, want %d", engine, got, want)
		}
	}
}

func TestEngineCodesMapTargetsWithEnglishFallback(t *testing.T) {
	if googleCode("zh") != "zh-CN" {
		t.Fatalf("googleCode(zh) = %q", googleCode("zh"))
	}
	if googleCode("ja") != "ja" {
		t.Fatalf("googleCode(ja) = %q", googleCode("ja"))
	}
	if deeplCode("ja") != "JA" {
		t.Fatalf("deeplCode(ja) = %q", deeplCode("ja"))
	}
	if deeplCode("zh") != "ZH" {
		t.Fatalf("deeplCode(zh) = %q", deeplCode("zh"))
	}
	if deeplCode("xx") != "EN" {
		t.Fatalf("deeplCode(xx) = %q", deeplCode("xx"))
	}
	if bingCode("zh") != "zh-Hans" {
		t.Fatalf("bingCode(zh) = %q", bingCode("zh"))
	}
	if bingCode("xx") != "en" {
		t.Fatalf("bingCode(xx) = %q", bingCode("xx"))
	}
}

// ── DeepL timestamp derivation ───────────────────────────────────────────────
//
// The endpoint validates that the timestamp is a multiple of (1 + i-count),
// offset by that amount; the web client computes it from the count of the
// letter `i` across the texts. With no `i`s the raw clock is used verbatim.

func TestDeeplTimestampWithoutAnyIReturnsRawClock(t *testing.T) {
	const ts = 1_700_000_000_123
	if got := deeplTimestamp(0, ts); got != ts {
		t.Fatalf("got %d, want %d", got, ts)
	}
}

func TestDeeplTimestampIsAMultipleOfICountPlusOneOffsetByIt(t *testing.T) {
	// For each i-count, the result must satisfy result % (i+1) == 0 and be the
	// next such multiple strictly above ts (ts - ts%n + n).
	for iCount := int64(1); iCount <= 20; iCount++ {
		const ts = 1_700_000_000_123
		n := iCount + 1
		got := deeplTimestamp(iCount, ts)
		if got%n != 0 {
			t.Fatalf("iCount=%d: %d is not a multiple of %d", iCount, got, n)
		}
		want := ts - (ts % n) + n
		if got != want {
			t.Fatalf("iCount=%d: got %d, want %d", iCount, got, want)
		}
		// It is the first valid timestamp strictly greater than ts.
		if got <= ts || got-ts > n {
			t.Fatalf("iCount=%d: %d is not the next multiple above %d", iCount, got, ts)
		}
	}
}

func TestDeeplTimestampAlreadyOnAMultipleStillAdvances(t *testing.T) {
	// When ts is itself a multiple of n, the web client still advances by n
	// (ts - 0 + n), so the timestamp is never equal to a bare multiple of n.
	const n = int64(5)     // i-count of 4
	ts := int64(1_000_000) // divisible by 5
	got := deeplTimestamp(4, ts)
	if got != ts+n {
		t.Fatalf("got %d, want %d", got, ts+n)
	}
}

// ── generic-wrapper unwrapping (focused, beyond the chunkBlocks cases) ────────

func TestUnwrapAllFourGenericTags(t *testing.T) {
	for _, tag := range []string{"div", "article", "section", "main"} {
		html := "<" + tag + "><p>a</p><p>b</p></" + tag + ">"
		out := chunkBlocks(html, 8)
		want := []string{"<p>a</p>", "<p>b</p>"}
		if !reflect.DeepEqual(out, want) {
			t.Fatalf("tag %q: got %#v, want %#v", tag, out, want)
		}
	}
}

func TestDoesNotUnwrapWhenWrapperHasSiblingBlocks(t *testing.T) {
	// Two top-level elements: not a single wrapper, so nothing is unwrapped and
	// the <div> is serialized as its own block.
	out := chunkBlocks("<div><p>a</p></div><p>b</p>", 1000)
	want := []string{"<div><p>a</p></div><p>b</p>"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %#v, want %#v", out, want)
	}
}

func TestUnwrapStopsAtFirstStructuralChild(t *testing.T) {
	// <div> unwraps to a single <ul>; the <ul> is structural, so unwrapping
	// stops there and the list is kept whole rather than exploded into <li>s.
	out := chunkBlocks("<div><ul><li>a</li><li>b</li></ul></div>", 1000)
	want := []string{"<ul><li>a</li><li>b</li></ul>"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %#v, want %#v", out, want)
	}
}

// ── SSE delta/error extraction (pure, no network) ────────────────────────────

func TestOpenAINullErrorFieldIsNotAnError(t *testing.T) {
	// OpenRouter and other OpenAI-compatible servers ship `"error": null` inside
	// ordinary successful chunks — it must not abort the stream.
	var full strings.Builder
	line := `data: {"choices":[{"delta":{"content":"hello"}}],"error":null}` + "\n"
	if err := handleSSELine(line, providerOpenAI, &full); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if full.String() != "hello" {
		t.Fatalf("delta = %q", full.String())
	}
}

func TestOpenAIRealErrorObjectIsSurfaced(t *testing.T) {
	var full strings.Builder
	line := `data: {"error":{"message":"rate limit exceeded"}}` + "\n"
	err := handleSSELine(line, providerOpenAI, &full)
	if err == nil || !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAIErrorObjectWithoutMessageFallsBack(t *testing.T) {
	var full strings.Builder
	line := `data: {"error":{"code":500}}` + "\n"
	err := handleSSELine(line, providerOpenAI, &full)
	if err == nil || !strings.Contains(err.Error(), "stream error") {
		t.Fatalf("err = %v", err)
	}
}

func TestAnthropicErrorEventIsSurfaced(t *testing.T) {
	var full strings.Builder
	line := `data: {"type":"error","error":{"message":"overloaded"}}` + "\n"
	err := handleSSELine(line, providerAnthropic, &full)
	if err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("err = %v", err)
	}
}

func TestAnthropicContentDeltaForwardsText(t *testing.T) {
	var full strings.Builder
	line := `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"world"}}` + "\n"
	if err := handleSSELine(line, providerAnthropic, &full); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if full.String() != "world" {
		t.Fatalf("delta = %q", full.String())
	}
}

func TestSSEIgnoresNonDataAndDoneLines(t *testing.T) {
	var full strings.Builder
	for _, line := range []string{": keep-alive comment\n", "data: [DONE]\n", "\n"} {
		if err := handleSSELine(line, providerOpenAI, &full); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if full.String() != "" {
		t.Fatalf("accumulated %q", full.String())
	}
}

func TestConsumeSSEAccumulatesAcrossLinesAndFinalUnterminatedFrame(t *testing.T) {
	// Three OpenAI deltas; the last frame is closed without a trailing newline,
	// as a non-compliant local endpoint may do — it must still be parsed.
	body := `data: {"choices":[{"delta":{"content":"a"}}]}` + "\n" +
		`data: {"choices":[{"delta":{"content":"b"}}]}` + "\n" +
		`data: {"choices":[{"delta":{"content":"c"}}]}`
	got, err := consumeSSE(strings.NewReader(body), providerOpenAI)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "abc" {
		t.Fatalf("got %q, want %q", got, "abc")
	}
}

func TestConsumeSSESurfacesMidStreamError(t *testing.T) {
	body := `data: {"choices":[{"delta":{"content":"a"}}]}` + "\n" +
		`data: {"error":{"message":"boom"}}` + "\n"
	_, err := consumeSSE(strings.NewReader(body), providerOpenAI)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

// ── Event shape (start → batch* → done) ──────────────────────────────────────

func TestEventTypesMatchTheTranslateEventContract(t *testing.T) {
	// A compile-time + runtime check that the emitted shape carries the fields
	// the frontend expects on each event variant.
	start := Event{Type: "start", Data: EventData{Total: 3}}
	batch := Event{Type: "batch", Data: EventData{HTML: "<p>x</p>", Done: 1}}
	done := Event{Type: "done", Data: EventData{HTML: "<p>x</p>"}}
	if start.Data.Total != 3 || batch.Data.Done != 1 || done.Data.HTML == "" {
		t.Fatalf("event fields not wired: %+v %+v %+v", start, batch, done)
	}
}
