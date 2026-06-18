package db

import (
	"strings"

	"hive/internal/models"
)

// mdEscape escapes the minimal set of Markdown special characters that, in
// inline/blockquote position, would otherwise change rendering: backslash,
// backtick, asterisk, underscore, hash, and the link brackets. We don't touch
// '<' or '&' — most renderers treat blockquote content as plain text.
var mdEscaper = strings.NewReplacer(
	`\`, `\\`,
	"`", "\\`",
	`*`, `\*`,
	`_`, `\_`,
	`#`, `\#`,
	`[`, `\[`,
	`]`, `\]`,
)

// mdInline escapes a string for use inside a single Markdown line (heading,
// paragraph, blockquote line). Newlines are preserved literally — the
// caller decides how to format them (blockquote re-prefixes each line with `> `).
func mdInline(s string) string {
	return mdEscaper.Replace(s)
}

// BuildHighlightsMarkdown renders a list of highlights — already in the order
// they should appear in the export — as a single Markdown document grouped by
// article. The exportDate is injected (rather than read from the clock) so the
// function stays pure and deterministically testable.
//
// Format:
//
//	# Highlights — exported <date>
//
//	## [Article title](url)
//	*Feed name*
//
//	> highlight quote
//
//	*optional note*
//
//	---
//
//	> next quote on the same article
//
//	---
//
//	## [Next article](url)
//	...
func BuildHighlightsMarkdown(rows []models.HighlightWithContext, exportDate string) string {
	var b strings.Builder
	b.WriteString("# Highlights — exported ")
	b.WriteString(exportDate)
	b.WriteString("\n\n")

	if len(rows) == 0 {
		b.WriteString("*No highlights yet.*\n")
		return b.String()
	}

	// Walk in input order; emit a header whenever ArticleID changes.
	var prevArticle int64 = -1
	for i, r := range rows {
		if r.ArticleID != prevArticle {
			if prevArticle != -1 {
				b.WriteString("\n")
			}
			writeArticleHeader(&b, r)
			prevArticle = r.ArticleID
		} else {
			// Separator between highlights inside the same article block.
			b.WriteString("\n---\n\n")
		}
		writeQuote(&b, r.Quote)
		if note := strings.TrimSpace(r.Note); note != "" {
			b.WriteString("\n*")
			b.WriteString(mdInline(note))
			b.WriteString("*\n")
		}
		_ = i
	}
	return b.String()
}

func writeArticleHeader(b *strings.Builder, r models.HighlightWithContext) {
	title := mdInline(r.ArticleTitle)
	b.WriteString("## ")
	if r.ArticleURL != nil && *r.ArticleURL != "" {
		b.WriteString("[")
		b.WriteString(title)
		b.WriteString("](")
		b.WriteString(*r.ArticleURL)
		b.WriteString(")")
	} else {
		b.WriteString(title)
	}
	b.WriteString("\n*")
	b.WriteString(mdInline(r.FeedTitle))
	b.WriteString("*\n\n")
}

func writeQuote(b *strings.Builder, quote string) {
	// Escape, then re-prefix each line with "> " so multi-line quotes
	// stay inside the blockquote.
	esc := mdInline(quote)
	for i, line := range strings.Split(esc, "\n") {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("> ")
		b.WriteString(line)
	}
	b.WriteString("\n")
}
