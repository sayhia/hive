package opml

import (
	"strings"
	"testing"

	"hive/internal/db"
)

// deref returns the pointed-to string, or "" for nil — handy for assertions
// mirroring Rust's folder.as_deref().
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func findByURL(t *testing.T, feeds []Entry, url string) Entry {
	t.Helper()
	for _, f := range feeds {
		if f.XMLURL == url {
			return f
		}
	}
	t.Fatalf("no feed with xmlUrl %q in %#v", url, feeds)
	return Entry{}
}

func TestParsesFlatFeedList(t *testing.T) {
	xml := `<opml version="2.0"><head/><body>
		<outline text="Blog A" xmlUrl="https://a.example/feed.xml"/>
		<outline text="Blog B" xmlUrl="https://b.example/feed.xml"/>
	</body></opml>`
	feeds, err := Parse(xml)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(feeds) != 2 {
		t.Fatalf("len = %d, want 2", len(feeds))
	}
	if feeds[0].Title != "Blog A" {
		t.Errorf("title = %q, want Blog A", feeds[0].Title)
	}
	if feeds[0].XMLURL != "https://a.example/feed.xml" {
		t.Errorf("xmlUrl = %q", feeds[0].XMLURL)
	}
	if feeds[0].Folder != nil {
		t.Errorf("folder = %v, want nil", *feeds[0].Folder)
	}
}

func TestParsesFeedsNestedInAFolder(t *testing.T) {
	xml := `<opml version="2.0"><head/><body>
		<outline text="Tech">
			<outline text="Feed 1" xmlUrl="https://1.example/f"/>
			<outline text="Feed 2" xmlUrl="https://2.example/f"/>
		</outline>
	</body></opml>`
	feeds, err := Parse(xml)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(feeds) != 2 {
		t.Fatalf("len = %d, want 2", len(feeds))
	}
	for _, f := range feeds {
		if deref(f.Folder) != "Tech" {
			t.Errorf("folder = %q, want Tech", deref(f.Folder))
		}
	}
}

func TestFolderLabelFallsBackToTitleAttribute(t *testing.T) {
	// A folder outline labelled only with `title` (no `text`) — common in
	// real-world exports. Its feeds must land in a folder named "News", not a
	// folder with an empty name.
	xml := `<opml version="2.0"><head/><body>
		<outline title="News">
			<outline text="Feed" xmlUrl="https://n.example/f"/>
		</outline>
	</body></opml>`
	feeds, err := Parse(xml)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(feeds) != 1 {
		t.Fatalf("len = %d, want 1", len(feeds))
	}
	if deref(feeds[0].Folder) != "News" {
		t.Errorf("folder = %q, want News", deref(feeds[0].Folder))
	}
}

func TestFeedTitleFallsBackToTitleThenURL(t *testing.T) {
	xml := `<opml version="2.0"><head/><body>
		<outline title="Titled Feed" xmlUrl="https://t.example/f"/>
		<outline xmlUrl="https://u.example/f"/>
	</body></opml>`
	feeds, err := Parse(xml)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if feeds[0].Title != "Titled Feed" {
		t.Errorf("title[0] = %q, want Titled Feed", feeds[0].Title)
	}
	// No text and no title — the URL itself is the last-resort label.
	if feeds[1].Title != "https://u.example/f" {
		t.Errorf("title[1] = %q, want URL fallback", feeds[1].Title)
	}
}

func TestBuildRoundTripsThroughParse(t *testing.T) {
	tech := "Tech"
	input := []db.ExportFeed{
		{Title: "Folderless", FeedURL: "https://x.example/f", Folder: nil},
		{Title: "In Folder", FeedURL: "https://y.example/f", Folder: &tech},
	}
	xml, err := Build(input)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	feeds, err := Parse(xml)
	if err != nil {
		t.Fatalf("re-parse: %v\nxml:\n%s", err, xml)
	}
	if len(feeds) != 2 {
		t.Fatalf("len = %d, want 2\nxml:\n%s", len(feeds), xml)
	}
	folderless := findByURL(t, feeds, "https://x.example/f")
	if folderless.Folder != nil {
		t.Errorf("folderless folder = %q, want nil", deref(folderless.Folder))
	}
	foldered := findByURL(t, feeds, "https://y.example/f")
	if foldered.Title != "In Folder" {
		t.Errorf("foldered title = %q, want In Folder", foldered.Title)
	}
	if deref(foldered.Folder) != "Tech" {
		t.Errorf("foldered folder = %q, want Tech", deref(foldered.Folder))
	}
}

func TestBlankFolderLabelImportsFeedsAsUngrouped(t *testing.T) {
	// A folder outline labelled with only whitespace must not carry that blank
	// name down to db.CreateFolder (which rejects an empty name and would abort
	// the whole transactional import). Its feeds import ungrouped instead.
	xml := "<opml version=\"2.0\"><head/><body>" +
		"<outline text=\"   \">" +
		"<outline text=\"Feed\" xmlUrl=\"https://b.example/f\"/>" +
		"</outline>" +
		"</body></opml>"
	feeds, err := Parse(xml)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(feeds) != 1 {
		t.Fatalf("len = %d, want 1", len(feeds))
	}
	if feeds[0].Folder != nil {
		t.Errorf("a whitespace-only folder label must resolve to ungrouped, got %q", deref(feeds[0].Folder))
	}
}

func TestBlankFeedLabelFallsBackToURL(t *testing.T) {
	// A feed outline whose `text` is only whitespace is treated as unlabelled —
	// its URL is the last-resort title, not a blank string.
	xml := "<opml version=\"2.0\"><head/><body>" +
		"<outline text=\"  \" xmlUrl=\"https://u.example/f\"/>" +
		"</body></opml>"
	feeds, err := Parse(xml)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if feeds[0].Title != "https://u.example/f" {
		t.Errorf("title = %q, want URL fallback", feeds[0].Title)
	}
}

func TestParseRejectsMalformedDocument(t *testing.T) {
	if _, err := Parse("not opml at all"); err == nil {
		t.Fatal("expected error for malformed document")
	}
}

func TestBareAmpersandsAreTolerated(t *testing.T) {
	// Real-world exports emit raw `&` in URLs and titles instead of `&amp;`. A
	// single one used to fail the whole document; the import must now survive,
	// with the `&` preserved in the decoded value.
	xml := `<opml version="1.0"><head/><body>
		<outline title="Cell Death & Disease" type="rss"
		         xmlUrl="https://www.science.org/action/showFeed?type=etoc&feed=rss&jc=stm"/>
	</body></opml>`
	feeds, err := Parse(xml)
	if err != nil {
		t.Fatalf("a bare & must not fail the parse: %v", err)
	}
	if len(feeds) != 1 {
		t.Fatalf("len = %d, want 1", len(feeds))
	}
	if feeds[0].Title != "Cell Death & Disease" {
		t.Errorf("title = %q", feeds[0].Title)
	}
	want := "https://www.science.org/action/showFeed?type=etoc&feed=rss&jc=stm"
	if feeds[0].XMLURL != want {
		t.Errorf("xmlUrl = %q, want %q", feeds[0].XMLURL, want)
	}
}

func TestAlreadyEscapedEntitiesRoundTrip(t *testing.T) {
	// A correctly-escaped file must be left untouched: `&amp;` stays a single
	// `&` rather than being double-escaped into `&amp;amp;`.
	xml := `<opml version="1.0"><head/><body>
		<outline title="Tom &amp; Jerry &#39;90" xmlUrl="https://x.example/f?a=1&amp;b=2"/>
	</body></opml>`
	feeds, err := Parse(xml)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if feeds[0].Title != "Tom & Jerry '90" {
		t.Errorf("title = %q, want Tom & Jerry '90", feeds[0].Title)
	}
	if feeds[0].XMLURL != "https://x.example/f?a=1&b=2" {
		t.Errorf("xmlUrl = %q, want ...a=1&b=2", feeds[0].XMLURL)
	}
}

func TestDuplicateFeedURLIsCollapsed(t *testing.T) {
	// NetNewsWire-style OPML exports repeat the same feed across multiple folder
	// views ("Today", "All Articles", "Unread"). The importer must collapse them
	// by xmlUrl and keep the first occurrence (preserving the original folder).
	xml := `<opml version="1.0"><head/><body>
		<outline title="Today" xmlUrl="https://x.example/feed"/>
		<outline title="All Articles" xmlUrl="https://x.example/feed"/>
		<outline title="Other" xmlUrl="https://y.example/feed"/>
	</body></opml>`
	feeds, err := Parse(xml)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(feeds) != 2 {
		t.Fatalf("len = %d, want 2", len(feeds))
	}
	if feeds[0].XMLURL != "https://x.example/feed" {
		t.Errorf("feeds[0].xmlUrl = %q", feeds[0].XMLURL)
	}
	if feeds[0].Title != "Today" {
		t.Errorf("feeds[0].title = %q, want Today (first occurrence wins)", feeds[0].Title)
	}
	if feeds[1].XMLURL != "https://y.example/feed" {
		t.Errorf("feeds[1].xmlUrl = %q", feeds[1].XMLURL)
	}
}

// TestBuildGroupsByFolder asserts the export structure directly (not just via a
// round-trip): folderless feeds at top level, each folder a nesting <outline>
// with its feeds inside, folders sorted by name with the folderless group first.
func TestBuildGroupsByFolder(t *testing.T) {
	tech, news := "Tech", "News"
	input := []db.ExportFeed{
		{Title: "Top", FeedURL: "https://top.example/f", Folder: nil},
		{Title: "T1", FeedURL: "https://t1.example/f", Folder: &tech},
		{Title: "N1", FeedURL: "https://n1.example/f", Folder: &news},
		{Title: "T2", FeedURL: "https://t2.example/f", Folder: &tech},
	}
	out, err := Build(input)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Folder outlines carry text= but no xmlUrl/type.
	if !strings.Contains(out, `<outline text="News">`) {
		t.Errorf("missing News folder outline\n%s", out)
	}
	if !strings.Contains(out, `<outline text="Tech">`) {
		t.Errorf("missing Tech folder outline\n%s", out)
	}
	// Feeds carry type="rss" and xmlUrl=.
	if !strings.Contains(out, `xmlUrl="https://t1.example/f"`) || !strings.Contains(out, `type="rss"`) {
		t.Errorf("missing feed outline attributes\n%s", out)
	}
	// Folder outlines must not leak empty optional attributes.
	if strings.Contains(out, `xmlUrl=""`) || strings.Contains(out, `type=""`) {
		t.Errorf("folder outline leaked empty optional attrs\n%s", out)
	}
	// Sorted folders with folderless first: top-level feed before "News" before "Tech".
	top := strings.Index(out, `xmlUrl="https://top.example/f"`)
	newsIdx := strings.Index(out, `<outline text="News">`)
	techIdx := strings.Index(out, `<outline text="Tech">`)
	if !(top < newsIdx && newsIdx < techIdx) {
		t.Errorf("ordering wrong: top=%d news=%d tech=%d\n%s", top, newsIdx, techIdx, out)
	}

	// And it must round-trip.
	feeds, err := Parse(out)
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
	if len(feeds) != 4 {
		t.Fatalf("round-trip len = %d, want 4", len(feeds))
	}
	if deref(findByURL(t, feeds, "https://t2.example/f").Folder) != "Tech" {
		t.Errorf("T2 should be in Tech after round-trip")
	}
	if findByURL(t, feeds, "https://top.example/f").Folder != nil {
		t.Errorf("Top should be folderless after round-trip")
	}
}

// TestBuildSkipsEmptyURLAndBlankFolder mirrors the parse-side normalization on
// the build side: a feed with a blank/whitespace xmlUrl is dropped (it could not
// round-trip through Parse), and a blank/whitespace folder name lands a feed at
// the top level — never inside an empty-named folder outline.
func TestBuildSkipsEmptyURLAndBlankFolder(t *testing.T) {
	blank, real := "   ", "Real"
	input := []db.ExportFeed{
		// (a) empty/whitespace URL → skipped entirely.
		{Title: "No URL", FeedURL: "   ", Folder: &real},
		// (b) blank/whitespace folder name → emitted ungrouped (top-level).
		{Title: "Blank Folder", FeedURL: "https://blank.example/f", Folder: &blank},
		// A normal feed in a real folder, to prove the bucket still works.
		{Title: "Kept", FeedURL: "https://kept.example/f", Folder: &real},
	}
	out, err := Build(input)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// (a) The empty-URL feed leaves no trace.
	if strings.Contains(out, "No URL") {
		t.Errorf("empty-URL feed should be skipped, but its text appears\n%s", out)
	}
	// (b) No empty-named folder outline is emitted.
	if strings.Contains(out, `<outline text="">`) || strings.Contains(out, `<outline text="   ">`) {
		t.Errorf("a blank folder name must not produce a folder outline\n%s", out)
	}

	feeds, err := Parse(out)
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
	if len(feeds) != 2 {
		t.Fatalf("len = %d, want 2 (the empty-URL feed dropped)\n%s", len(feeds), out)
	}
	// (b) The blank-folder feed round-trips as ungrouped (top-level).
	if findByURL(t, feeds, "https://blank.example/f").Folder != nil {
		t.Errorf("blank folder name should resolve to ungrouped, got %q",
			deref(findByURL(t, feeds, "https://blank.example/f").Folder))
	}
	// The real-folder feed is unaffected.
	if deref(findByURL(t, feeds, "https://kept.example/f").Folder) != "Real" {
		t.Errorf("kept feed folder = %q, want Real",
			deref(findByURL(t, feeds, "https://kept.example/f").Folder))
	}
}
