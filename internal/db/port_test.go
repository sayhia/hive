package db

import (
	"context"
	"testing"
	"time"

	"hive/internal/models"
)

// seed opens a temp DB with one feed + one article inserted, returning the DB,
// feed id and article id. Mirrors the Rust test_db helper.
func seed(t *testing.T) (*DB, int64, int64) {
	t.Helper()
	d := openTemp(t)
	ctx := context.Background()
	feedID, err := InsertFeed(ctx, d.W, "https://example.com/feed.xml", nil, "Example Feed", nil, models.SourceRSS, nil)
	if err != nil {
		t.Fatalf("InsertFeed: %v", err)
	}
	body := "body"
	art := &NewArticle{
		GUID:        "g1",
		URL:         ptr("https://example.com/a1"),
		Title:       "An Article",
		ContentHTML: ptr("<p>body</p>"),
		BodyText:    body,
	}
	if _, err := UpsertArticle(ctx, d, feedID, art, false, nil); err != nil {
		t.Fatalf("UpsertArticle: %v", err)
	}
	var id int64
	if err := d.R.QueryRowContext(ctx, "SELECT id FROM articles").Scan(&id); err != nil {
		t.Fatalf("select article id: %v", err)
	}
	return d, feedID, id
}

func ptr[T any](v T) *T { return &v }

// The digest leads with unread (most-recent first), then falls back to read
// articles — so a read item never outranks an unread one even when it's newer,
// and a caught-up user still gets a non-empty briefing.
func TestDigestSourcePrioritizesUnread(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	feedID, err := InsertFeed(ctx, d.W, "https://example.com/feed.xml", nil, "Example Feed", nil, models.SourceRSS, nil)
	if err != nil {
		t.Fatalf("InsertFeed: %v", err)
	}

	arts := []struct {
		guid, title, pub string
		read             bool
	}{
		{"r-old", "read-old", "2026-06-20T00:00:00Z", true},
		{"r-new", "read-new", "2026-06-25T00:00:00Z", true}, // newest overall, but read
		{"u-old", "unread-old", "2026-06-21T00:00:00Z", false},
		{"u-new", "unread-new", "2026-06-24T00:00:00Z", false},
	}
	for _, a := range arts {
		pub := a.pub
		if _, err := UpsertArticle(ctx, d, feedID, &NewArticle{
			GUID: a.guid, Title: a.title, BodyText: "body", PublishedAt: &pub,
		}, false, nil); err != nil {
			t.Fatalf("UpsertArticle %s: %v", a.guid, err)
		}
		if a.read {
			var id int64
			if err := d.R.QueryRowContext(ctx, "SELECT id FROM articles WHERE guid = ?1", a.guid).Scan(&id); err != nil {
				t.Fatalf("lookup %s: %v", a.guid, err)
			}
			if err := SetRead(ctx, d.W, id, true); err != nil {
				t.Fatalf("SetRead %s: %v", a.guid, err)
			}
		}
	}

	items, err := DigestSource(ctx, d.R, 10)
	if err != nil {
		t.Fatalf("DigestSource: %v", err)
	}
	want := []string{"unread-new", "unread-old", "read-new", "read-old"}
	if len(items) != len(want) {
		t.Fatalf("digest returned %d items, want %d", len(items), len(want))
	}
	for i, w := range want {
		if items[i].Title != w {
			got := make([]string, len(items))
			for j, it := range items {
				got[j] = it.Title
			}
			t.Fatalf("digest order = %v, want %v (unread first, recency within)", got, want)
		}
	}
}

// RecentArticlesForRAG is Ask's zero-hit fallback: it returns recent articles
// unread-first (same ordering as the digest) and — unlike DigestSource — carries
// the article id + feed title so the answer's source chips can link back.
func TestRecentArticlesForRAG(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	feedID, err := InsertFeed(ctx, d.W, "https://example.com/feed.xml", nil, "Example Feed", nil, models.SourceRSS, nil)
	if err != nil {
		t.Fatalf("InsertFeed: %v", err)
	}
	arts := []struct {
		guid, title, pub string
		read             bool
	}{
		{"r-new", "read-new", "2026-06-25T00:00:00Z", true}, // newest overall, but read
		{"u-old", "unread-old", "2026-06-21T00:00:00Z", false},
		{"u-new", "unread-new", "2026-06-24T00:00:00Z", false},
	}
	for _, a := range arts {
		pub := a.pub
		if _, err := UpsertArticle(ctx, d, feedID, &NewArticle{
			GUID: a.guid, Title: a.title, BodyText: "body", PublishedAt: &pub,
		}, false, nil); err != nil {
			t.Fatalf("UpsertArticle %s: %v", a.guid, err)
		}
		if a.read {
			var id int64
			if err := d.R.QueryRowContext(ctx, "SELECT id FROM articles WHERE guid = ?1", a.guid).Scan(&id); err != nil {
				t.Fatalf("lookup %s: %v", a.guid, err)
			}
			if err := SetRead(ctx, d.W, id, true); err != nil {
				t.Fatalf("SetRead %s: %v", a.guid, err)
			}
		}
	}

	hits, err := RecentArticlesForRAG(ctx, d.R, 10, 500)
	if err != nil {
		t.Fatalf("RecentArticlesForRAG: %v", err)
	}
	want := []string{"unread-new", "unread-old", "read-new"}
	if len(hits) != len(want) {
		t.Fatalf("got %d hits, want %d", len(hits), len(want))
	}
	for i, w := range want {
		if hits[i].Title != w {
			got := make([]string, len(hits))
			for j, h := range hits {
				got[j] = h.Title
			}
			t.Fatalf("order = %v, want %v (unread first, recency within)", got, want)
		}
	}
	// Fallback hits must carry id + feed title (for clickable source chips) and a
	// non-empty summary (the compact per-article context fed to the model).
	for _, h := range hits {
		if h.ID == 0 {
			t.Errorf("hit %q has zero id", h.Title)
		}
		if h.FeedTitle != "Example Feed" {
			t.Errorf("hit %q feed = %q, want Example Feed", h.Title, h.FeedTitle)
		}
		if h.Summary == "" {
			t.Errorf("hit %q has empty summary", h.Title)
		}
	}
}

// DeleteFeeds removes many feeds in one call (the settings batch unsubscribe),
// leaving the unselected ones intact.
func TestDeleteFeeds(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	mk := func(url, title string) int64 {
		id, err := InsertFeed(ctx, d.W, url, nil, title, nil, models.SourceRSS, nil)
		if err != nil {
			t.Fatalf("InsertFeed %s: %v", title, err)
		}
		return id
	}
	a := mk("https://a.example.com/feed.xml", "A")
	b := mk("https://b.example.com/feed.xml", "B")
	c := mk("https://c.example.com/feed.xml", "C")

	if err := DeleteFeeds(ctx, d, []int64{a, c}); err != nil {
		t.Fatalf("DeleteFeeds: %v", err)
	}

	var got []int64
	rows, err := d.R.QueryContext(ctx, "SELECT id FROM feeds ORDER BY id")
	if err != nil {
		t.Fatalf("query feeds: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, id)
	}
	if len(got) != 1 || got[0] != b {
		t.Fatalf("remaining feed ids = %v, want [%d] (only B)", got, b)
	}
}

// SetFeedURL repoints a feed, clears the old URL's conditional-GET validators
// (they'd be wrong for the new URL), and refuses a URL another feed already uses.
func TestSetFeedURL(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	mk := func(url, title string) int64 {
		id, err := InsertFeed(ctx, d.W, url, nil, title, nil, models.SourceRSS, nil)
		if err != nil {
			t.Fatalf("InsertFeed %s: %v", title, err)
		}
		return id
	}
	a := mk("https://a.example.com/feed.xml", "A")
	mk("https://b.example.com/feed.xml", "B")

	// Seed conditional-GET state so we can prove the repoint clears it.
	etag, lm := `W/"abc"`, "Wed, 21 Oct 2026 07:28:00 GMT"
	if err := SetFeedFetchState(ctx, d.W, a, &etag, &lm, nil); err != nil {
		t.Fatalf("SetFeedFetchState: %v", err)
	}

	const newURL = "https://a.example.com/new.xml"
	if err := SetFeedURL(ctx, d.W, a, newURL); err != nil {
		t.Fatalf("SetFeedURL: %v", err)
	}
	var gotURL string
	if err := d.R.QueryRowContext(ctx, "SELECT feed_url FROM feeds WHERE id = ?1", a).Scan(&gotURL); err != nil {
		t.Fatalf("select feed_url: %v", err)
	}
	if gotURL != newURL {
		t.Errorf("feed_url = %q, want %q", gotURL, newURL)
	}
	var cleared int64
	if err := d.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM feeds WHERE id = ?1
		   AND etag IS NULL AND last_modified IS NULL AND last_fetched_at IS NULL`, a).
		Scan(&cleared); err != nil {
		t.Fatalf("select fetch state: %v", err)
	}
	if cleared != 1 {
		t.Error("etag / last_modified / last_fetched_at should be cleared on repoint")
	}

	// Repointing onto another feed's URL, or to an empty URL, must be refused.
	if err := SetFeedURL(ctx, d.W, a, "https://b.example.com/feed.xml"); err == nil {
		t.Error("repointing onto feed B's URL should fail (feed_url is UNIQUE)")
	}
	if err := SetFeedURL(ctx, d.W, a, "   "); err == nil {
		t.Error("empty URL should fail")
	}
}

func TestDueLogicRespectsIntervals(t *testing.T) {
	d, feedID, _ := seed(t)
	ctx := context.Background()
	const global = 30

	due := func() bool {
		feeds, err := FeedsDueForRefresh(ctx, d.R, global)
		if err != nil {
			t.Fatalf("FeedsDueForRefresh: %v", err)
		}
		for _, f := range feeds {
			if f.ID == feedID {
				return true
			}
		}
		return false
	}

	// Never fetched → always due.
	if !due() {
		t.Fatal("never-fetched feed should be due")
	}
	// Just fetched → not due under the 30-minute global interval.
	if err := SetFeedFetchState(ctx, d.W, feedID, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if due() {
		t.Fatal("just-fetched feed should not be due")
	}
	// Backdate 20 minutes: still under 30-minute global.
	if _, err := d.W.ExecContext(ctx,
		"UPDATE feeds SET last_fetched_at = datetime('now', '-20 minutes') WHERE id = ?1", feedID); err != nil {
		t.Fatal(err)
	}
	if due() {
		t.Fatal("20-min-old feed under 30-min global should not be due")
	}
	// A 15-minute per-feed override makes it due.
	if err := SetFeedRefreshInterval(ctx, d.W, feedID, ptr[int64](15)); err != nil {
		t.Fatal(err)
	}
	if !due() {
		t.Fatal("15-min override should make 20-min-old feed due")
	}
	// The "off" sentinel opts out entirely.
	if err := SetFeedRefreshInterval(ctx, d.W, feedID, ptr(RefreshOffMinutes)); err != nil {
		t.Fatal(err)
	}
	if due() {
		t.Fatal("off sentinel should exclude the feed")
	}
	// Clearing the override returns to the global interval.
	if err := SetFeedRefreshInterval(ctx, d.W, feedID, nil); err != nil {
		t.Fatal(err)
	}
	feeds, _ := FeedsDueForRefresh(ctx, d.R, 5)
	if len(feeds) == 0 {
		t.Fatal("20-min-old feed under 5-min global should be due")
	}
}

func TestTranslationRoundTrip(t *testing.T) {
	d, _, id := seed(t)
	ctx := context.Background()
	got, err := GetTranslations(ctx, d.R, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatal("fresh article should have no translations")
	}
	zhTitle := "标题"
	if err := UpsertTranslation(ctx, d.W, id, "zh", "llm", &zhTitle, "<p>译文</p>"); err != nil {
		t.Fatal(err)
	}
	got, _ = GetTranslations(ctx, d.R, id)
	if len(got) != 1 || got[0].Lang != "zh" || got[0].Engine != "llm" ||
		got[0].HTML != "<p>译文</p>" || deref(got[0].Title) != "标题" {
		t.Fatalf("translation not cached: %+v", got)
	}
	// A different (lang, engine) coexists rather than overwriting the first.
	if err := UpsertTranslation(ctx, d.W, id, "en", "google", nil, "<p>translation</p>"); err != nil {
		t.Fatal(err)
	}
	if got, _ = GetTranslations(ctx, d.R, id); len(got) != 2 {
		t.Fatalf("expected two cached translations, got %d", len(got))
	}
	// Re-translating an existing key updates in place (no duplicate row).
	if err := UpsertTranslation(ctx, d.W, id, "zh", "llm", &zhTitle, "<p>译文2</p>"); err != nil {
		t.Fatal(err)
	}
	if got, _ = GetTranslations(ctx, d.R, id); len(got) != 2 {
		t.Fatalf("re-translate should update in place, got %d", len(got))
	}
}

func TestRefineSourceTypePromotesButNeverDemotes(t *testing.T) {
	d, feedID, _ := seed(t)
	ctx := context.Background()
	kind := func() string {
		var k string
		_ = d.R.QueryRowContext(ctx, "SELECT source_type FROM feeds WHERE id = ?1", feedID).Scan(&k)
		return k
	}
	if kind() != "rss" {
		t.Fatal("test feed should start as rss")
	}
	_ = RefineFeedSourceType(ctx, d.W, feedID, models.SourceRSS)
	if kind() != "rss" {
		t.Fatal("refining to rss is a no-op")
	}
	_ = RefineFeedSourceType(ctx, d.W, feedID, models.SourcePodcast)
	if kind() != "podcast" {
		t.Fatal("should promote generic rss to podcast")
	}
	_ = RefineFeedSourceType(ctx, d.W, feedID, models.SourceMastodon)
	if kind() != "podcast" {
		t.Fatal("should never churn an already-classified feed")
	}
}

func TestOPMLExportOmitsNewsletters(t *testing.T) {
	d, _, _ := seed(t)
	ctx := context.Background()
	cfg := models.NewsletterConfig{Host: "imap.example.com", Port: 993, Username: "me@example.com", Password: "secret", Folder: "Newsletters"}
	if _, err := InsertNewsletterSource(ctx, d, "imap://me@example.com@imap.example.com:993/Newsletters", "My Newsletter", cfg); err != nil {
		t.Fatal(err)
	}
	exported, err := FeedsForExport(ctx, d.R)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported) != 1 || exported[0].FeedURL != "https://example.com/feed.xml" {
		t.Fatalf("newsletter should be omitted from export, got %+v", exported)
	}
}

func TestRulesPreviewAndApply(t *testing.T) {
	d, feedID, _ := seed(t)
	ctx := context.Background()
	// Add a couple of articles with distinctive titles.
	for i, title := range []string{"Café news roundup", "Sports update", "café latte art"} {
		_, err := UpsertArticle(ctx, d, feedID, &NewArticle{
			GUID:     "rule" + string(rune('a'+i)),
			URL:      ptr("https://example.com/r" + string(rune('a'+i))),
			Title:    title,
			BodyText: title,
		}, false, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Preview a title rule for "café" — must match both café titles
	// case-insensitively (Unicode), via unicode_lower.
	count, samples, err := PreviewRule(ctx, d.R, nil, "title", "café")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("preview count = %d, want 2 (samples %v)", count, samples)
	}
	// Apply a star rule and confirm two rows got starred.
	n, err := ApplyRuleToExisting(ctx, d.W, nil, "title", "café", "star")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("apply star affected %d rows, want 2", n)
	}
}

func TestTagsAndArticleTagging(t *testing.T) {
	d, _, articleID := seed(t)
	ctx := context.Background()
	tagID, err := CreateTag(ctx, d.W, "  Rust  ")
	if err != nil {
		t.Fatal(err)
	}
	// Case-insensitive dedup.
	if dup, _ := CreateTag(ctx, d.W, "rust"); dup != tagID {
		t.Fatalf("tag dedup failed: %d != %d", dup, tagID)
	}
	if err := SetArticleTag(ctx, d.W, articleID, tagID, true); err != nil {
		t.Fatal(err)
	}
	tags, err := TagsForArticle(ctx, d.R, articleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0].Name != "Rust" {
		t.Fatalf("article tags = %+v, want one 'Rust'", tags)
	}
	// list_tags reports a live article count.
	all, _ := ListTags(ctx, d.R)
	if len(all) != 1 || all[0].ArticleCount != 1 {
		t.Fatalf("ListTags = %+v, want one tag with count 1", all)
	}
	// Detach.
	if err := SetArticleTag(ctx, d.W, articleID, tagID, false); err != nil {
		t.Fatal(err)
	}
	tags, _ = TagsForArticle(ctx, d.R, articleID)
	if len(tags) != 0 {
		t.Fatalf("expected no tags after detach, got %+v", tags)
	}
}

func TestHighlightsCRUD(t *testing.T) {
	d, _, articleID := seed(t)
	ctx := context.Background()
	id, err := InsertHighlight(ctx, d.W, NewHighlight{
		ArticleID: articleID, Quote: "body", Prefix: "", Suffix: "", TextOffset: 0, Color: "yellow", Note: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateHighlightNote(ctx, d.W, id, "my note"); err != nil {
		t.Fatal(err)
	}
	if err := SetHighlightColor(ctx, d.W, id, "green"); err != nil {
		t.Fatal(err)
	}
	got, err := GetHighlight(ctx, d.R, id)
	if err != nil || got == nil {
		t.Fatalf("GetHighlight: %v / %v", got, err)
	}
	if got.Note != "my note" || got.Color != "green" {
		t.Fatalf("highlight not updated: %+v", got)
	}
	list, _ := ListHighlights(ctx, d.R, articleID)
	if len(list) != 1 {
		t.Fatalf("ListHighlights = %d, want 1", len(list))
	}
	if err := DeleteHighlight(ctx, d.W, id); err != nil {
		t.Fatal(err)
	}
	if got, _ := GetHighlight(ctx, d.R, id); got != nil {
		t.Fatal("highlight should be gone")
	}
}

func TestListArticlesAndMarkAllRead(t *testing.T) {
	d, feedID, _ := seed(t)
	ctx := context.Background()
	// Smart counts: one unread article from the seed.
	c, err := SmartCounts(ctx, d.R)
	if err != nil {
		t.Fatal(err)
	}
	if c.Unread != 1 {
		t.Fatalf("unread = %d, want 1", c.Unread)
	}
	// List all articles.
	arts, err := ListArticles(ctx, d.R, models.ArticleQuery{Kind: models.QueryAll}, false, nil, false, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 {
		t.Fatalf("ListArticles all = %d, want 1", len(arts))
	}
	// Full-text search for a word in the body.
	q := "body"
	found, err := ListArticles(ctx, d.R, models.ArticleQuery{Kind: models.QueryAll}, false, &q, false, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("FTS search 'body' = %d, want 1", len(found))
	}
	// Mark all read for the feed.
	n, err := MarkAllRead(ctx, d, models.ArticleQuery{Kind: models.QueryFeed, Value: ptr(feedID)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("MarkAllRead flipped %d, want 1", n)
	}
	c, _ = SmartCounts(ctx, d.R)
	if c.Unread != 0 {
		t.Fatalf("after mark-all-read unread = %d, want 0", c.Unread)
	}
}

func TestSyncQueue(t *testing.T) {
	d, _, articleID := seed(t)
	ctx := context.Background()
	// A queued change with no remote id is deferred by TakeSyncQueue.
	if err := EnqueueSync(ctx, d.W, articleID, "read", true); err != nil {
		t.Fatal(err)
	}
	entries, err := TakeSyncQueue(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries without remote_id should be deferred, got %d", len(entries))
	}
	// Once a remote id is assigned, the change drains.
	if err := SetRemoteID(ctx, d.W, articleID, "remote-123"); err != nil {
		t.Fatal(err)
	}
	entries, err = TakeSyncQueue(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].RemoteID != "remote-123" || !entries[0].Value {
		t.Fatalf("drained entries = %+v, want one read=true", entries)
	}
	// Queue is now empty.
	ids, _ := PendingSyncArticleIDs(ctx, d.R)
	if len(ids) != 0 {
		t.Fatalf("pending ids = %v, want empty", ids)
	}
}

// A guid-less feed's articles used to be keyed off their URL; the parser now
// composes a title+date key instead. The upsert must recognise a same-feed URL
// match as the same article, so pre-existing rows don't re-ingest as new when
// the key scheme changes — and so a title edit doesn't duplicate the item.
func TestSyntheticGuidFallsBackToFeedURLMatch(t *testing.T) {
	d, feedID, _ := seed(t) // seed row: guid "g1", url https://example.com/a1
	ctx := context.Background()

	relisted := &NewArticle{
		GUID:          "An Article\x1f2026-01-01T00:00:00Z",
		SyntheticGUID: true,
		URL:           ptr("https://example.com/a1"),
		Title:         "An Article",
		BodyText:      "body",
	}
	inserted, err := UpsertArticle(ctx, d, feedID, relisted, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Error("same-feed URL match must not re-ingest as a new article")
	}
	var n int
	if err := d.R.QueryRowContext(ctx, "SELECT COUNT(*) FROM articles").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("article count = %d, want 1", n)
	}

	// A genuinely new item (fresh URL, fresh key) still inserts.
	fresh := &NewArticle{
		GUID:          "Another\x1f2026-01-02T00:00:00Z",
		SyntheticGUID: true,
		URL:           ptr("https://example.com/a2"),
		Title:         "Another",
		BodyText:      "body",
	}
	inserted, err = UpsertArticle(ctx, d, feedID, fresh, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Error("new synthetic-guid article must insert")
	}

	// The same synthetic key under a *different* feed is unaffected by the
	// URL guard (scoped per feed): it dedups only via (feed_id, guid).
	otherFeed, err := InsertFeed(ctx, d.W, "https://other.example.com/feed.xml", nil, "Other", nil, models.SourceRSS, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserted, err = UpsertArticle(ctx, d, otherFeed, relisted, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Error("URL guard must be scoped to the same feed")
	}
}

// The full-archive-feed loop: a feed that ships its whole history keeps a
// purged item in its document, so the next refresh re-fetches it. Without a
// tombstone it would land again as fresh unread — resurfacing the whole read
// archive the user just cleared, every day the daily cleanup runs.
func TestRetentionTombstoneBlocksReingestion(t *testing.T) {
	d, feedID, _ := seed(t) // seed article is unread + fresh → never purged
	ctx := context.Background()

	old := time.Now().UTC().AddDate(0, 0, -90).Format(time.RFC3339)
	archived := &NewArticle{
		GUID:        "archived",
		URL:         ptr("https://example.com/archived"),
		Title:       "Archived",
		BodyText:    "old body",
		PublishedAt: &old,
	}
	if _, err := UpsertArticle(ctx, d, feedID, archived, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.W.ExecContext(ctx,
		"UPDATE articles SET is_read = 1 WHERE guid = 'archived'"); err != nil {
		t.Fatal(err)
	}

	removed, err := CleanupOldArticles(ctx, d, 30)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (only the old read article)", removed)
	}

	// The next refresh re-fetches the still-in-feed item. dedup is off (the
	// default), so only the tombstone can suppress the re-insertion.
	inserted, err := UpsertArticle(ctx, d, feedID, archived, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Error("a retention-purged article must not be re-ingested as new")
	}
	var n int
	if err := d.R.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM articles WHERE guid = 'archived'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("purged article rows = %d, want 0 (stays gone across refreshes)", n)
	}
}

// The tombstone key is (feed_id, guid): a different feed carrying the same
// guid is unaffected, and deleting the feed cascades its tombstones away.
func TestTombstoneScopedToFeedAndCascades(t *testing.T) {
	d, feedID, _ := seed(t)
	ctx := context.Background()

	old := time.Now().UTC().AddDate(0, 0, -90).Format(time.RFC3339)
	a := &NewArticle{GUID: "shared-guid", Title: "Old", BodyText: "b", PublishedAt: &old}
	if _, err := UpsertArticle(ctx, d, feedID, a, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.W.ExecContext(ctx,
		"UPDATE articles SET is_read = 1 WHERE guid = 'shared-guid'"); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanupOldArticles(ctx, d, 30); err != nil {
		t.Fatal(err)
	}

	otherFeed, err := InsertFeed(ctx, d.W, "https://other.example.com/f.xml", nil, "Other", nil, models.SourceRSS, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := UpsertArticle(ctx, d, otherFeed, a, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Error("tombstones must be scoped per feed")
	}

	// Unsubscribing the original feed cascades its tombstones away.
	if _, err := d.W.ExecContext(ctx, "DELETE FROM feeds WHERE id = ?1", feedID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := d.R.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM article_tombstones WHERE feed_id = ?1", feedID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("tombstones after feed delete = %d, want 0 (cascade)", left)
	}
}

// ── sync state reconciliation (papr #105) ──────────────────────────

// The server's unread/starred sets are authoritative: articles under
// server-known feeds are swept read unless the server says unread, starred iff
// the server says starred — including the long tail a recent-items pull never
// enumerates. Feeds the server doesn't know stay untouched.
func TestReconcileMarksTailReadAndScopesToServerFeeds(t *testing.T) {
	d, feedID, _ := seed(t) // fixture: guid g1 / url https://example.com/a1, unread
	ctx := context.Background()
	for _, a := range []struct{ guid, url string }{
		{"a2", "https://example.com/a2"},
		{"a3", "https://example.com/a3"},
	} {
		if _, err := UpsertArticle(ctx, d, feedID, &NewArticle{
			GUID: a.guid, URL: ptr(a.url), Title: a.guid, BodyText: "b",
		}, false, nil); err != nil {
			t.Fatal(err)
		}
	}
	// A local-only feed the server doesn't know about.
	local, err := InsertFeed(ctx, d.W, "https://local.example/feed.xml", nil, "Local", nil, models.SourceRSS, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertArticle(ctx, d, local, &NewArticle{
		GUID: "b1", URL: ptr("https://local.example/b1"), Title: "Local one", BodyText: "b",
	}, false, nil); err != nil {
		t.Fatal(err)
	}

	// Server: only a2 is unread, a3 is starred.
	unread := map[string]struct{}{"https://example.com/a2": {}}
	starred := map[string]struct{}{"https://example.com/a3": {}}

	changed, err := ReconcileSyncState(ctx, d, []int64{feedID}, unread, starred)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 2 {
		t.Fatalf("changed = %d, want 2 (a1 → read, a3 → read+starred; a2 unchanged)", changed)
	}

	state := func(guid string) (read, star bool) {
		if err := d.R.QueryRowContext(ctx,
			"SELECT is_read, is_starred FROM articles WHERE guid = ?1", guid).Scan(&read, &star); err != nil {
			t.Fatalf("state %s: %v", guid, err)
		}
		return
	}
	if r, _ := state("g1"); !r {
		t.Error("tail item not in unread set must be marked read")
	}
	if r, _ := state("a2"); r {
		t.Error("server-unread item must stay unread")
	}
	if r, s := state("a3"); !r || !s {
		t.Error("a3 must be read and starred")
	}
	if r, _ := state("b1"); r {
		t.Error("local-only feed is out of scope and must stay untouched")
	}
}

// A pending (un-pushed) local edit must win over the server state, so a pull
// never clobbers a change still waiting to be sent.
func TestReconcileSkipsPendingLocalEdits(t *testing.T) {
	d, feedID, aid := seed(t)
	ctx := context.Background()
	// User marked a1 unread locally; the change hasn't been pushed yet.
	if err := EnqueueSync(ctx, d.W, aid, "read", false); err != nil {
		t.Fatal(err)
	}
	// Server considers a1 read (empty unread set) — the pending edit wins.
	changed, err := ReconcileSyncState(ctx, d, []int64{feedID}, map[string]struct{}{}, map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("changed = %d, want 0 (pending article is skipped)", changed)
	}
	var read bool
	if err := d.R.QueryRowContext(ctx, "SELECT is_read FROM articles WHERE id = ?1", aid).Scan(&read); err != nil {
		t.Fatal(err)
	}
	if read {
		t.Error("a1 must keep its local (unread) state")
	}
}
