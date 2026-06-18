// hive-cli is the agent-facing command line for hive: read, search, and manage
// the same library the desktop app uses, over the same SQLite file (WAL +
// busy_timeout make concurrent use with a running GUI safe). Every command
// prints one JSON document to stdout — no prose, no progress — so LLM agents
// and scripts can consume it directly.
//
// It links only hive/internal packages: the Wails service layer (hive/services)
// would drag native webview dependencies into a headless binary. Event emits
// inside shared code (refresh progress, feeds-updated) are already no-ops
// without a running Wails app (see internal/events).
//
// Deliberately smaller than the GUI's add-feed flow: subscribe accepts direct
// feed URLs, HTML pages carrying <link rel="alternate"> discovery, and
// rsshub:// short links — not the YouTube/Reddit page-resolution specials.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"hive/internal/appstate"
	"hive/internal/db"
	"hive/internal/ingestion"
	"hive/internal/models"
	"hive/internal/sources"
)

func usage() {
	fmt.Fprint(os.Stderr, `hive-cli — agent-facing interface to the hive RSS library (JSON out)

Usage: hive-cli [-data DIR] <command> [args]

Commands:
  home                        Library overview: counts + latest unread titles
  feeds                       All subscriptions (id, title, folder, unread)
  list [flags]                Article summaries (newest first)
      -feed ID | -unread | -starred | -later | -tag ID
      -limit N (default 20) | -offset N
  search <query> [-limit N]   Full-text search over titles and bodies
  read <articleID> [-mark]    Full article (text body); -mark also marks read
  mark <articleID> <flags>    -read=t|f  -star=t|f  -later=t|f
  tags                        All tags
  subscribe <url> [-folder NAME]   Add a feed (URL / page / rsshub://)
  refresh [-feed ID] [-folder ID]  Fetch feeds now (all, one, or a folder)

  -data DIR                   Override the data directory (default: the app's)
`)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func emit(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fail(err)
	}
}

// dataDir mirrors main.go's choice so the CLI opens the same library as the
// desktop app.
func dataDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base, _ = os.UserHomeDir()
	}
	return filepath.Join(base, "Hive")
}

func main() {
	root := flag.NewFlagSet("hive-cli", flag.ExitOnError)
	dir := root.String("data", "", "data directory (defaults to the desktop app's)")
	root.Usage = usage
	_ = root.Parse(os.Args[1:])
	args := root.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	d := *dir
	if d == "" {
		d = dataDir()
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		fail(err)
	}
	state, err := appstate.New(d)
	if err != nil {
		fail(err)
	}
	defer state.Close()
	ctx := context.Background()

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "home":
		cmdHome(ctx, state)
	case "feeds":
		cmdFeeds(ctx, state)
	case "list":
		cmdList(ctx, state, rest)
	case "search":
		cmdSearch(ctx, state, rest)
	case "read":
		cmdRead(ctx, state, rest)
	case "mark":
		cmdMark(ctx, state, rest)
	case "tags":
		cmdTags(ctx, state)
	case "subscribe":
		cmdSubscribe(ctx, state, rest)
	case "refresh":
		cmdRefresh(ctx, state, rest)
	default:
		usage()
		os.Exit(2)
	}
}

func cmdHome(ctx context.Context, s *appstate.State) {
	counts, err := db.SmartCounts(ctx, s.DB.R)
	if err != nil {
		fail(err)
	}
	latest, err := db.ListArticles(ctx, s.DB.R,
		models.ArticleQuery{Kind: models.QueryUnread}, false, nil, false, 10, 0)
	if err != nil {
		fail(err)
	}
	feeds, err := db.ListFeeds(ctx, s.DB.R)
	if err != nil {
		fail(err)
	}
	emit(map[string]any{
		"counts":       counts,
		"feedCount":    len(feeds),
		"latestUnread": latest,
	})
}

func cmdFeeds(ctx context.Context, s *appstate.State) {
	feeds, err := db.ListFeeds(ctx, s.DB.R)
	if err != nil {
		fail(err)
	}
	emit(feeds)
}

func cmdList(ctx context.Context, s *appstate.State, args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	feed := fs.Int64("feed", 0, "articles of one feed id")
	tag := fs.Int64("tag", 0, "articles carrying one tag id")
	unread := fs.Bool("unread", false, "unread only")
	starred := fs.Bool("starred", false, "starred only")
	later := fs.Bool("later", false, "read-later only")
	limit := fs.Int64("limit", 20, "max rows")
	offset := fs.Int64("offset", 0, "skip rows")
	_ = fs.Parse(args)

	q := models.ArticleQuery{Kind: models.QueryAll}
	switch {
	case *feed > 0:
		q = models.ArticleQuery{Kind: models.QueryFeed, Value: feed}
	case *tag > 0:
		q = models.ArticleQuery{Kind: models.QueryTag, Value: tag}
	case *unread:
		q = models.ArticleQuery{Kind: models.QueryUnread}
	case *starred:
		q = models.ArticleQuery{Kind: models.QueryStarred}
	case *later:
		q = models.ArticleQuery{Kind: models.QueryReadLater}
	}
	arts, err := db.ListArticles(ctx, s.DB.R, q, false, nil, false, *limit, *offset)
	if err != nil {
		fail(err)
	}
	emit(arts)
}

func cmdSearch(ctx context.Context, s *appstate.State, args []string) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	limit := fs.Int64("limit", 20, "max rows")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		fail(fmt.Errorf("search needs a query"))
	}
	query := strings.Join(fs.Args(), " ")
	arts, err := db.ListArticles(ctx, s.DB.R,
		models.ArticleQuery{Kind: models.QueryAll}, false, &query, false, *limit, 0)
	if err != nil {
		fail(err)
	}
	emit(arts)
}

func cmdRead(ctx context.Context, s *appstate.State, args []string) {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	mark := fs.Bool("mark", false, "also mark the article read")
	id := parseIDThenFlags(fs, args, "read")
	detail, err := db.GetArticle(ctx, s.DB.R, id)
	if err != nil {
		fail(err)
	}
	// Agents want text, not HTML: body_text is already the extracted plain
	// text the FTS index and list snippets use.
	var bodyText string
	if err := s.DB.R.QueryRowContext(ctx,
		"SELECT body_text FROM articles WHERE id = ?1", id).Scan(&bodyText); err == nil {
		detail.ContentHTML = nil
		detail.ExtractedHTML = nil
	}
	if *mark && !detail.IsRead {
		if err := db.SetRead(ctx, s.DB.W, id, true); err != nil {
			fail(err)
		}
		enqueueIfConnected(ctx, s, id, "read", true)
		detail.IsRead = true
	}
	emit(map[string]any{"article": detail, "bodyText": bodyText})
}

func cmdMark(ctx context.Context, s *appstate.State, args []string) {
	fs := flag.NewFlagSet("mark", flag.ExitOnError)
	read := fs.String("read", "", "t|f")
	star := fs.String("star", "", "t|f")
	later := fs.String("later", "", "t|f")
	id := parseIDThenFlags(fs, args, "mark")

	changed := map[string]bool{}
	if v, ok := parseBoolFlag(*read); ok {
		if err := db.SetRead(ctx, s.DB.W, id, v); err != nil {
			fail(err)
		}
		enqueueIfConnected(ctx, s, id, "read", v)
		changed["read"] = v
	}
	if v, ok := parseBoolFlag(*star); ok {
		if err := db.SetStarred(ctx, s.DB.W, id, v); err != nil {
			fail(err)
		}
		enqueueIfConnected(ctx, s, id, "starred", v)
		changed["starred"] = v
	}
	if v, ok := parseBoolFlag(*later); ok {
		if err := db.SetReadLater(ctx, s.DB.W, id, v); err != nil {
			fail(err)
		}
		changed["readLater"] = v
	}
	if len(changed) == 0 {
		fail(fmt.Errorf("mark needs at least one of -read/-star/-later (t|f)"))
	}
	emit(map[string]any{"id": id, "changed": changed})
}

func cmdTags(ctx context.Context, s *appstate.State) {
	tags, err := db.ListTags(ctx, s.DB.R)
	if err != nil {
		fail(err)
	}
	emit(tags)
}

func cmdSubscribe(ctx context.Context, s *appstate.State, args []string) {
	fs := flag.NewFlagSet("subscribe", flag.ExitOnError)
	folder := fs.String("folder", "", "file the feed under this folder (created if missing)")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		fail(fmt.Errorf("subscribe needs a URL"))
	}
	rawURL := strings.TrimSpace(fs.Arg(0))
	client := s.HTTP()

	// rsshub:// short links expand against the configured instance.
	if len(rawURL) >= 9 && strings.EqualFold(rawURL[:9], "rsshub://") {
		inst := sources.DefaultRSSHubInstance
		if v, _ := db.GetSetting(ctx, s.DB.R, "rsshub_instance"); v != nil && strings.TrimSpace(*v) != "" {
			inst = *v
		}
		if expanded := sources.ExpandRSSHub(rawURL, inst); expanded != nil {
			rawURL = *expanded
		}
	}

	feedURL := rawURL
	body, contentType, finalURL, err := ingestion.Get(ctx, client, feedURL)
	if err != nil {
		fail(err)
	}
	if !ingestion.LooksLikeFeed(body) {
		// An HTML page: discover its advertised feed.
		found := ingestion.DiscoverFeeds(ingestion.DecodeHTML(body, contentType), finalURL)
		if len(found) == 0 {
			fail(fmt.Errorf("no feed found at %s", rawURL))
		}
		feedURL = found[0]
		if body, _, _, err = ingestion.Get(ctx, client, feedURL); err != nil {
			fail(err)
		}
	}
	parsed, err := ingestion.ParseFeed(body, feedURL)
	if err != nil {
		fail(err)
	}

	var folderID *int64
	if strings.TrimSpace(*folder) != "" {
		fid, err := db.FolderIDByName(ctx, s.DB.W, strings.TrimSpace(*folder))
		if err != nil {
			fail(err)
		}
		folderID = &fid
	}

	title := feedURL
	if parsed.Title != nil && *parsed.Title != "" {
		title = *parsed.Title
	}
	kind := ingestion.RefineSourceType(ingestion.DetectSourceType(feedURL), parsed, feedURL)
	id, err := db.InsertFeed(ctx, s.DB.W, feedURL, parsed.SiteURL, title, parsed.Description, kind, folderID)
	if err != nil {
		fail(err)
	}
	added := 0
	for i := range parsed.Articles {
		if ok, err := db.UpsertArticle(ctx, s.DB, id, &parsed.Articles[i], false, nil); err == nil && ok {
			added++
		}
	}
	emit(map[string]any{"id": id, "title": title, "feedUrl": feedURL, "articles": added})
}

func cmdRefresh(ctx context.Context, s *appstate.State, args []string) {
	fs := flag.NewFlagSet("refresh", flag.ExitOnError)
	feed := fs.Int64("feed", 0, "refresh one feed id")
	folder := fs.Int64("folder", 0, "refresh one folder id")
	_ = fs.Parse(args)

	scope := ingestion.ScopeAll
	target := int64(0)
	switch {
	case *feed > 0:
		scope, target = ingestion.ScopeFeed, *feed
	case *folder > 0:
		scope, target = ingestion.ScopeFolder, *folder
	}
	n, err := ingestion.RefreshAll(ctx, s, nil, true, scope, target)
	if err != nil {
		fail(err)
	}
	emit(map[string]any{"newArticles": n})
}

// ── helpers ──────────────────────────────────────────────────────────────────

// parseIDThenFlags supports the natural `cmd <id> -flag` order: Go's flag
// package stops at the first positional argument, so `read 17 -mark` would
// otherwise silently ignore -mark. Parse once, take the id from the first
// positional, then re-parse whatever followed it as flags.
func parseIDThenFlags(fs *flag.FlagSet, args []string, cmd string) int64 {
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) == 0 {
		fail(fmt.Errorf("%s needs an article id", cmd))
	}
	id, err := strconv.ParseInt(rest[0], 10, 64)
	if err != nil {
		fail(fmt.Errorf("%s: bad article id %q", cmd, rest[0]))
	}
	if len(rest) > 1 {
		_ = fs.Parse(rest[1:])
	}
	return id
}

func parseBoolFlag(v string) (val, ok bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "t", "true", "1", "yes", "on":
		return true, true
	case "f", "false", "0", "no", "off":
		return false, true
	default:
		return false, false
	}
}

// enqueueIfConnected mirrors the GUI's state setters: when a GReader server is
// linked, a local read/starred change is queued so the next sync pushes it.
func enqueueIfConnected(ctx context.Context, s *appstate.State, id int64, field string, value bool) {
	if db.IsFreshrssConnected(ctx, s.DB.R) {
		_ = db.EnqueueSync(ctx, s.DB.W, id, field, value)
	}
}
