package db

import (
	"context"
	"strings"
	"testing"

	"hive/internal/models"
)

// seedTwoArticleHighlights inserts: 1 feed, 2 articles in that feed,
// 3 highlights total (2 on the first article, 1 on the second), with
// monotonically increasing created_at via 3 separate inserts so the
// SQLite default datetime('now') tie-breaks naturally — but we sort
// by id DESC as a secondary so the test is robust to same-second writes.
// Returns the feed/article ids for assertion.
func seedTwoArticleHighlights(t *testing.T, d *DB) (feedID int64, art1, art2 int64) {
	t.Helper()
	ctx := context.Background()
	siteURL := "https://example.com"
	favicon := "https://example.com/favicon.ico"
	var err error
	feedID, err = InsertFeed(ctx, d.W, "https://example.com/feed.xml", &siteURL, "Example Feed", nil, models.SourceRSS, nil)
	if err != nil {
		t.Fatalf("InsertFeed: %v", err)
	}
	if _, err := d.W.ExecContext(ctx, "UPDATE feeds SET favicon_url=?1 WHERE id=?2", favicon, feedID); err != nil {
		t.Fatalf("set favicon: %v", err)
	}
	art1 = insertArticleForTest(t, d, feedID, "First article", "https://example.com/a")
	art2 = insertArticleForTest(t, d, feedID, "Second article", "https://example.com/b")

	for _, h := range []NewHighlight{
		{ArticleID: art1, Quote: "alpha quote", Color: "yellow", Note: ""},
		{ArticleID: art1, Quote: "beta quote", Color: "yellow", Note: "my note"},
		{ArticleID: art2, Quote: "gamma quote", Color: "pink", Note: ""},
	} {
		if _, err := InsertHighlight(ctx, d.W, h); err != nil {
			t.Fatalf("InsertHighlight: %v", err)
		}
	}
	return feedID, art1, art2
}

// insertArticleForTest inserts an article with the minimum non-null fields
// the schema requires. fetched_at and is_read default in the schema.
func insertArticleForTest(t *testing.T, d *DB, feedID int64, title, url string) int64 {
	t.Helper()
	res, err := d.W.ExecContext(context.Background(),
		`INSERT INTO articles(feed_id, guid, url, title, body_text)
		 VALUES (?1, ?2, ?3, ?4, '')`,
		feedID, title /* unique-ish */, url, title)
	if err != nil {
		t.Fatalf("insert article: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestListAllHighlightsJoinsArticleAndFeed(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	_, art1, art2 := seedTwoArticleHighlights(t, d)

	rows, err := ListAllHighlights(ctx, d.R)
	if err != nil {
		t.Fatalf("ListAllHighlights: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len=%d, want 3", len(rows))
	}

	// Newest highlight first (created last → highest id).
	if rows[0].Quote != "gamma quote" || rows[0].ArticleID != art2 {
		t.Errorf("rows[0] = %+v, want gamma on art2", rows[0])
	}
	if rows[2].Quote != "alpha quote" || rows[2].ArticleID != art1 {
		t.Errorf("rows[2] = %+v, want alpha on art1", rows[2])
	}

	// Joined fields populated.
	if rows[0].ArticleTitle != "Second article" {
		t.Errorf("ArticleTitle = %q, want %q", rows[0].ArticleTitle, "Second article")
	}
	if rows[0].FeedTitle != "Example Feed" {
		t.Errorf("FeedTitle = %q, want %q", rows[0].FeedTitle, "Example Feed")
	}
	if rows[0].FaviconURL == nil || *rows[0].FaviconURL != "https://example.com/favicon.ico" {
		t.Errorf("FaviconURL = %v, want example.com/favicon.ico", rows[0].FaviconURL)
	}
	if rows[0].ArticleURL == nil || *rows[0].ArticleURL != "https://example.com/b" {
		t.Errorf("ArticleURL = %v, want example.com/b", rows[0].ArticleURL)
	}
}

func TestListAllHighlightsEmpty(t *testing.T) {
	d := openTemp(t)
	rows, err := ListAllHighlights(context.Background(), d.R)
	if err != nil {
		t.Fatalf("ListAllHighlights: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("len=%d, want 0", len(rows))
	}
}

func TestSearchAllHighlightsMatchesQuote(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	seedTwoArticleHighlights(t, d)

	rows, err := SearchAllHighlights(ctx, d.R, "alpha")
	if err != nil {
		t.Fatalf("SearchAllHighlights: %v", err)
	}
	if len(rows) != 1 || rows[0].Quote != "alpha quote" {
		t.Errorf("got %+v, want 1 row with alpha quote", rows)
	}
}

func TestSearchAllHighlightsMatchesNote(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	seedTwoArticleHighlights(t, d)

	rows, err := SearchAllHighlights(ctx, d.R, "my note")
	if err != nil {
		t.Fatalf("SearchAllHighlights: %v", err)
	}
	if len(rows) != 1 || rows[0].Quote != "beta quote" {
		t.Errorf("got %+v, want 1 row whose note contains 'my note'", rows)
	}
}

func TestSearchAllHighlightsMatchesArticleTitle(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	seedTwoArticleHighlights(t, d)

	rows, err := SearchAllHighlights(ctx, d.R, "Second article")
	if err != nil {
		t.Fatalf("SearchAllHighlights: %v", err)
	}
	if len(rows) != 1 || rows[0].ArticleTitle != "Second article" {
		t.Errorf("got %+v, want 1 row from 'Second article'", rows)
	}
}

func TestSearchAllHighlightsEscapesLikeWildcards(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	feedID, _, _ := seedTwoArticleHighlights(t, d)
	// Insert one highlight whose quote contains a literal '%'.
	a := insertArticleForTest(t, d, feedID, "Discount post", "https://example.com/c")
	if _, err := InsertHighlight(ctx, d.W, NewHighlight{
		ArticleID: a, Quote: "save 50% today", Color: "green",
	}); err != nil {
		t.Fatalf("InsertHighlight: %v", err)
	}

	// Without escaping, "50%" would match anything starting with "50".
	// With escaping, only the literal "50%" matches.
	rows, err := SearchAllHighlights(ctx, d.R, "50%")
	if err != nil {
		t.Fatalf("SearchAllHighlights: %v", err)
	}
	if len(rows) != 1 || rows[0].Quote != "save 50% today" {
		t.Errorf("got %+v, want 1 literal '50%%' match", rows)
	}
}

func TestSearchAllHighlightsEmptyQueryReturnsAll(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	seedTwoArticleHighlights(t, d)

	rows, err := SearchAllHighlights(ctx, d.R, "")
	if err != nil {
		t.Fatalf("SearchAllHighlights: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("len=%d, want 3 (empty query → all rows)", len(rows))
	}
}

func TestBuildHighlightsMarkdownEmpty(t *testing.T) {
	out := BuildHighlightsMarkdown(nil, "2026-06-30")
	want := "# Highlights — exported 2026-06-30\n\n*No highlights yet.*\n"
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestBuildHighlightsMarkdownGroupsByArticle(t *testing.T) {
	url1 := "https://example.com/a"
	url2 := "https://example.com/b"
	rows := []models.HighlightWithContext{
		{
			Highlight:  models.Highlight{ID: 3, ArticleID: 2, Quote: "gamma", CreatedAt: "2026-06-30T10:00:00Z"},
			ArticleURL: &url2, ArticleTitle: "Second", FeedTitle: "Example Feed",
		},
		{
			Highlight:  models.Highlight{ID: 2, ArticleID: 1, Quote: "beta", Note: "my note", CreatedAt: "2026-06-30T09:00:00Z"},
			ArticleURL: &url1, ArticleTitle: "First", FeedTitle: "Example Feed",
		},
		{
			Highlight:  models.Highlight{ID: 1, ArticleID: 1, Quote: "alpha", CreatedAt: "2026-06-30T08:00:00Z"},
			ArticleURL: &url1, ArticleTitle: "First", FeedTitle: "Example Feed",
		},
	}
	out := BuildHighlightsMarkdown(rows, "2026-06-30")

	// Header first.
	if !strings.HasPrefix(out, "# Highlights — exported 2026-06-30\n\n") {
		t.Errorf("missing header in:\n%s", out)
	}
	// Article 2 must appear before article 1 (input order is preserved by group).
	idxSecond := strings.Index(out, "## [Second](https://example.com/b)")
	idxFirst := strings.Index(out, "## [First](https://example.com/a)")
	if idxSecond < 0 || idxFirst < 0 {
		t.Fatalf("missing article headers in:\n%s", out)
	}
	if idxSecond > idxFirst {
		t.Errorf("article order wrong; got Second after First")
	}
	// Quote rendered as blockquote.
	if !strings.Contains(out, "> alpha") {
		t.Errorf("missing alpha blockquote")
	}
	// Note rendered after its quote, italicised.
	if !strings.Contains(out, "> beta\n\n*my note*\n") {
		t.Errorf("missing beta blockquote followed by italic note; got:\n%s", out)
	}
	// Feed name printed once per article block.
	if strings.Count(out, "*Example Feed*") != 2 {
		t.Errorf("Feed name should appear once per article block (2 articles); count=%d", strings.Count(out, "*Example Feed*"))
	}
}

func TestBuildHighlightsMarkdownEscapesQuote(t *testing.T) {
	url := "https://example.com/p"
	rows := []models.HighlightWithContext{{
		Highlight:    models.Highlight{ID: 1, ArticleID: 1, Quote: "use *asterisks* and `backticks` and #hash", CreatedAt: "2026-06-30T00:00:00Z"},
		ArticleURL:   &url,
		ArticleTitle: "Post",
		FeedTitle:    "Feed",
	}}
	out := BuildHighlightsMarkdown(rows, "2026-06-30")
	if !strings.Contains(out, "> use \\*asterisks\\* and \\`backticks\\` and \\#hash") {
		t.Errorf("expected escaped *, `, # in quote; got:\n%s", out)
	}
}

func TestSearchAllHighlightsInRangeIncludesFromExcludesTo(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	feedID, art1, _ := seedTwoArticleHighlights(t, d)

	// seedTwoArticleHighlights leaves all 3 highlights at "now" (sqlite
	// default). Insert two more with explicit created_at so the range
	// boundary is deterministic: one exactly at "from", one exactly at "to".
	if _, err := d.W.ExecContext(ctx,
		`INSERT INTO highlights(article_id, quote, prefix, suffix, text_offset, color, note, created_at)
		 VALUES (?1, 'boundary-from', '', '', 0, 'yellow', '', '2026-07-01T00:00:00Z')`,
		art1); err != nil {
		t.Fatalf("insert boundary-from: %v", err)
	}
	if _, err := d.W.ExecContext(ctx,
		`INSERT INTO highlights(article_id, quote, prefix, suffix, text_offset, color, note, created_at)
		 VALUES (?1, 'boundary-to', '', '', 0, 'yellow', '', '2026-07-08T00:00:00Z')`,
		art1); err != nil {
		t.Fatalf("insert boundary-to: %v", err)
	}
	_ = feedID

	rows, err := SearchAllHighlightsInRange(ctx, d.R, "2026-07-01T00:00:00Z", "2026-07-08T00:00:00Z")
	if err != nil {
		t.Fatalf("SearchAllHighlightsInRange: %v", err)
	}
	quotes := make([]string, len(rows))
	for i, r := range rows {
		quotes[i] = r.Quote
	}
	found := map[string]bool{}
	for _, q := range quotes {
		found[q] = true
	}
	if !found["boundary-from"] {
		t.Errorf("expected boundary-from (at the `from` instant) to be included; got %v", quotes)
	}
	if found["boundary-to"] {
		t.Errorf("expected boundary-to (at the `to` instant) to be excluded; got %v", quotes)
	}
}

func TestSearchAllHighlightsInRangeEmpty(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	seedTwoArticleHighlights(t, d)

	rows, err := SearchAllHighlightsInRange(ctx, d.R, "2020-01-01T00:00:00Z", "2020-01-02T00:00:00Z")
	if err != nil {
		t.Fatalf("SearchAllHighlightsInRange: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("len=%d, want 0 for a range with no highlights", len(rows))
	}
}

func TestSearchAllHighlightsInRangeOrdersNewestFirst(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	_, art1, _ := seedTwoArticleHighlights(t, d)

	if _, err := d.W.ExecContext(ctx,
		`INSERT INTO highlights(article_id, quote, prefix, suffix, text_offset, color, note, created_at)
		 VALUES (?1, 'early', '', '', 0, 'yellow', '', '2026-07-02T00:00:00Z')`,
		art1); err != nil {
		t.Fatalf("insert early: %v", err)
	}
	if _, err := d.W.ExecContext(ctx,
		`INSERT INTO highlights(article_id, quote, prefix, suffix, text_offset, color, note, created_at)
		 VALUES (?1, 'late', '', '', 0, 'yellow', '', '2026-07-05T00:00:00Z')`,
		art1); err != nil {
		t.Fatalf("insert late: %v", err)
	}

	rows, err := SearchAllHighlightsInRange(ctx, d.R, "2026-07-01T00:00:00Z", "2026-07-08T00:00:00Z")
	if err != nil {
		t.Fatalf("SearchAllHighlightsInRange: %v", err)
	}
	// seedTwoArticleHighlights' 3 rows use the DB's real datetime('now')
	// default, which lands inside this range on whatever real-world day the
	// suite happens to run — so filter down to just the two explicit rows
	// this test cares about instead of asserting the range's total count.
	var filtered []string
	for _, r := range rows {
		if r.Quote == "early" || r.Quote == "late" {
			filtered = append(filtered, r.Quote)
		}
	}
	if len(filtered) != 2 {
		t.Fatalf("got %v, want exactly [late, early]", filtered)
	}
	if filtered[0] != "late" || filtered[1] != "early" {
		t.Errorf("got %v, want [late, early] (newest first)", filtered)
	}
}

func TestBuildHighlightsMarkdownNullArticleURL(t *testing.T) {
	rows := []models.HighlightWithContext{{
		Highlight:    models.Highlight{ID: 1, ArticleID: 1, Quote: "q", CreatedAt: "2026-06-30T00:00:00Z"},
		ArticleURL:   nil, // article had no URL
		ArticleTitle: "Untitled post",
		FeedTitle:    "Feed",
	}}
	out := BuildHighlightsMarkdown(rows, "2026-06-30")
	// Bare title, no link, when URL is missing.
	if !strings.Contains(out, "## Untitled post\n") {
		t.Errorf("expected bare title without link; got:\n%s", out)
	}
	if strings.Contains(out, "[Untitled post]") {
		t.Errorf("did not expect link syntax when URL is nil; got:\n%s", out)
	}
}
