package ingestion

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"hive/internal/appstate"
	"hive/internal/db"
	"hive/internal/events"
	"hive/internal/models"
	"hive/internal/newsletter"
	"hive/internal/syncer"
)

// newsletterPollTimeout is the wall-clock cap for polling one newsletter mailbox.
const newsletterPollTimeout = 90 * time.Second

// schedulerTick is how often the background loop wakes to look for due feeds.
const schedulerTick = 60 * time.Second

// RefreshScope selects which feeds a refresh run touches.
type RefreshScope int

const (
	// ScopeAll refreshes every feed and newsletter — manual refresh / OPML import.
	ScopeAll RefreshScope = iota
	// ScopeDue refreshes only sources whose interval has elapsed — the scheduler.
	ScopeDue
	// ScopeFeed refreshes a single feed (or newsletter source) — the per-feed
	// manual refresh. The target id is RefreshAll's `target` argument.
	ScopeFeed
	// ScopeFolder refreshes every feed in one folder — the per-folder manual
	// refresh. The target id is RefreshAll's `target` argument.
	ScopeFolder
)

// RefreshProgress is a per-run progress event streamed to the frontend (mirrors
// types.ts RefreshProgress). Event is "started" | "feedDone" | "finished".
type RefreshProgress struct {
	Event string      `json:"event"`
	Data  RefreshData `json:"data"`
}

// RefreshData carries the fields for each progress event (only the relevant
// ones are meaningful per Event).
type RefreshData struct {
	Total       int     `json:"total"`
	FeedID      int64   `json:"feedId"`
	NewArticles int     `json:"newArticles"`
	Error       *string `json:"error"`
}

type outcomeKind int

const (
	outNotModified outcomeKind = iota
	outUpdated
	outFailed
)

type fetchResult struct {
	id           int64
	url          string
	kind         outcomeKind
	parsed       *ParsedFeed
	etag         *string
	lastModified *string
	err          string
}

// fetchOne performs a conditional GET for one feed and parses the result.
func fetchOne(ctx context.Context, client *http.Client, f db.FeedToRefresh) fetchResult {
	fetched, err := ConditionalGet(ctx, client, f.FeedURL, f.ETag, f.LastModified)
	if err != nil {
		return fetchResult{id: f.ID, url: f.FeedURL, kind: outFailed, err: err.Error()}
	}
	if fetched.NotModified {
		return fetchResult{id: f.ID, url: f.FeedURL, kind: outNotModified}
	}
	parsed, err := ParseFeed(fetched.Bytes, f.FeedURL)
	if err != nil {
		return fetchResult{id: f.ID, url: f.FeedURL, kind: outFailed, err: err.Error()}
	}
	return fetchResult{
		id: f.ID, url: f.FeedURL, kind: outUpdated,
		parsed: parsed, etag: fetched.ETag, lastModified: fetched.LastModified,
	}
}

// upsertBatch inserts a feed's parsed articles, returning the count newly
// inserted (genuinely new and unread).
func upsertBatch(ctx context.Context, database *db.DB, feedID int64, articles []db.NewArticle, dedup bool, rules []models.Rule) int {
	n := 0
	for i := range articles {
		if ok, err := db.UpsertArticle(ctx, database, feedID, &articles[i], dedup, rules); err == nil && ok {
			n++
		}
	}
	return n
}

// RefreshAll refreshes feeds (bounded concurrency) selected by scope. It streams
// per-feed progress over emit (when non-nil), emits feeds-updated, runs
// retention cleanup, syncs to a connected server, and returns the new-article
// count. Mirrors scheduler.rs refresh_all.
func RefreshAll(ctx context.Context, app *appstate.State, emit func(RefreshProgress), waitIfBusy bool, scope RefreshScope, target int64) (int, error) {
	// Only one refresh at a time. wait_if_busy callers (OPML import) queue;
	// everyone else bows out cleanly.
	if waitIfBusy {
		app.LockRefresh()
	} else if !app.TryLockRefresh() {
		if emit != nil {
			emit(RefreshProgress{Event: "started", Data: RefreshData{Total: 0}})
			emit(RefreshProgress{Event: "finished", Data: RefreshData{NewArticles: 0}})
		}
		return 0, nil
	}
	defer app.UnlockRefresh()

	r := app.DB.R
	globalMin := int64(30)
	if v, _ := db.GetSetting(ctx, r, "refresh_interval_min"); v != nil {
		if m, err := strconv.ParseInt(*v, 10, 64); err == nil && m >= 5 {
			if m > db.RefreshOffMinutes {
				m = db.RefreshOffMinutes
			}
			globalMin = m
		}
	}

	var feeds []db.FeedToRefresh
	var newsletters []db.NewsletterToPoll
	switch scope {
	case ScopeAll:
		feeds, _ = db.FeedsToRefresh(ctx, r)
		newsletters, _ = db.NewsletterSourcesToPoll(ctx, r)
	case ScopeDue:
		feeds, _ = db.FeedsDueForRefresh(ctx, r, globalMin)
		newsletters, _ = db.NewsletterSourcesDueToPoll(ctx, r, globalMin)
	case ScopeFeed:
		feeds, _ = db.FeedsToRefreshForFeed(ctx, r, target)
		newsletters, _ = db.NewsletterSourcesForFeed(ctx, r, target)
	case ScopeFolder:
		feeds, _ = db.FeedsToRefreshInFolder(ctx, r, target)
		newsletters, _ = db.NewsletterSourcesInFolder(ctx, r, target)
	}
	concurrency := int(db.SettingInt(ctx, r, "net_concurrency", 6))
	if concurrency < 1 {
		concurrency = 1
	} else if concurrency > 16 {
		concurrency = 16
	}
	dedup := db.SettingFlag(ctx, r, "dedup_enabled", false)
	rules, _ := db.ActiveRules(ctx, r)

	// An idle scheduler tick with nothing due bows out before the heavier tail.
	if scope == ScopeDue && len(feeds) == 0 && len(newsletters) == 0 {
		if emit != nil {
			emit(RefreshProgress{Event: "started", Data: RefreshData{Total: 0}})
			emit(RefreshProgress{Event: "finished", Data: RefreshData{NewArticles: 0}})
		}
		return 0, nil
	}

	if emit != nil {
		emit(RefreshProgress{Event: "started", Data: RefreshData{Total: len(feeds)}})
	}

	// Fetch feeds concurrently, bounded by a semaphore; process each as it lands.
	sem := make(chan struct{}, concurrency)
	resCh := make(chan fetchResult, len(feeds))
	var wg sync.WaitGroup
	client := app.HTTP()
	for _, f := range feeds {
		wg.Add(1)
		go func(f db.FeedToRefresh) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			resCh <- fetchOne(ctx, client, f)
		}(f)
	}
	go func() { wg.Wait(); close(resCh) }()

	w := app.DB.W
	totalNew := 0
	for res := range resCh {
		newHere := 0
		var errStr *string
		switch res.kind {
		case outNotModified:
			_ = db.TouchFeed(ctx, w, res.id)
		case outFailed:
			_ = db.SetFeedError(ctx, w, res.id, res.err)
			e := res.err
			errStr = &e
		case outUpdated:
			newHere = upsertBatch(ctx, app.DB, res.id, res.parsed.Articles, dedup, rules)
			_ = db.UpdateFeedMeta(ctx, w, res.id, res.parsed.Title, res.parsed.SiteURL, res.parsed.Description, res.parsed.Icon)
			_ = db.SetFeedFetchState(ctx, w, res.id, res.etag, res.lastModified, nil)
			refined := RefineSourceType(models.SourceRSS, res.parsed, res.url)
			_ = db.RefineFeedSourceType(ctx, w, res.id, refined)
		}
		totalNew += newHere
		if emit != nil {
			emit(RefreshProgress{Event: "feedDone", Data: RefreshData{FeedID: res.id, NewArticles: newHere, Error: errStr}})
		}
	}

	totalNew += pollNewsletters(ctx, app, newsletters, dedup, rules)

	// Retention: drop old read articles, throttled to once per day.
	if v, _ := db.GetSetting(ctx, r, "retention_days"); v != nil {
		if days, err := strconv.ParseInt(*v, 10, 64); err == nil {
			now := time.Now().Unix()
			lastRun := db.SettingInt(ctx, r, "retention_last_run", 0)
			if now-lastRun >= 86_400 {
				if _, err := db.CleanupOldArticles(ctx, app.DB, days); err == nil {
					_ = db.SetSetting(ctx, w, "retention_last_run", strconv.FormatInt(now, 10))
				}
			}
		}
	}

	if emit != nil {
		emit(RefreshProgress{Event: "finished", Data: RefreshData{NewArticles: totalNew}})
	}
	events.Emit("feeds-updated", totalNew)

	// Reconcile with the sync server, if connected. A sync mutates state, so
	// emit feeds-updated again afterwards.
	if db.IsFreshrssConnected(ctx, r) {
		if _, err := syncer.Sync(ctx, app.HTTP(), app.DB); err == nil {
			events.Emit("feeds-updated", 0)
		}
	}
	// TODO(phase5): notify::update_badge + tray::refresh here.
	return totalNew, nil
}

// pollNewsletters polls every newsletter source over IMAP (bounded concurrency)
// and ingests new messages. A per-mailbox failure is recorded as the feed's
// fetch_error and does not abort the others.
func pollNewsletters(ctx context.Context, app *appstate.State, sources []db.NewsletterToPoll, dedup bool, rules []models.Rule) int {
	if len(sources) == 0 {
		return 0
	}
	sem := make(chan struct{}, 4)
	type nlRes struct {
		feedID   int64
		articles []db.NewArticle
		err      string
	}
	resCh := make(chan nlRes, len(sources))
	var wg sync.WaitGroup
	for _, src := range sources {
		wg.Add(1)
		go func(src db.NewsletterToPoll) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			pollCtx, cancel := context.WithTimeout(ctx, newsletterPollTimeout)
			defer cancel()
			articles, err := newsletter.Poll(pollCtx, src.Config)
			if err != nil {
				resCh <- nlRes{feedID: src.FeedID, err: err.Error()}
				return
			}
			resCh <- nlRes{feedID: src.FeedID, articles: articles}
		}(src)
	}
	go func() { wg.Wait(); close(resCh) }()

	w := app.DB.W
	total := 0
	for res := range resCh {
		if res.err != "" {
			_ = db.SetFeedError(ctx, w, res.feedID, res.err)
			continue
		}
		total += upsertBatch(ctx, app.DB, res.feedID, res.articles, dedup, rules)
		_ = db.TouchFeed(ctx, w, res.feedID)
	}
	return total
}

// SpawnScheduler starts the background refresh loop. The app must stay resident
// for this to run.
func SpawnScheduler(app *appstate.State) {
	go func() {
		time.Sleep(8 * time.Second)
		for {
			_, _ = RefreshAll(context.Background(), app, nil, false, ScopeDue, 0)
			time.Sleep(schedulerTick)
		}
	}()
}
