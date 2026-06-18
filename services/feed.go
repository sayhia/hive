package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"hive/internal/apperr"
	"hive/internal/appstate"
	"hive/internal/db"
	"hive/internal/discovery"
	"hive/internal/events"
	"hive/internal/ingestion"
	"hive/internal/models"
	"hive/internal/sources"
)

// FeedService exposes feed management — both pure CRUD and the network-bound
// subscribe / discover / refresh commands.
type FeedService struct {
	app *appstate.State
}

// AddFeed subscribes to a feed. It accepts a feed URL or a website URL (feed
// auto-discovered), and recognizes multi-source URLs (YouTube channels,
// subreddits, Mastodon profiles, rsshub:// short links), fetching once so the
// feed is immediately populated. Mirrors commands.rs add_feed.
func (s *FeedService) AddFeed(rawURL string, folderID *int64) (*models.Feed, error) {
	ctx := bg()
	client := s.app.HTTP()

	// Step 0a: expand an rsshub://route short link against the configured instance.
	if len(rawURL) >= 9 && strings.EqualFold(strings.TrimSpace(rawURL)[:9], "rsshub://") {
		instance, _ := db.GetSetting(ctx, s.app.DB.R, "rsshub_instance")
		inst := sources.DefaultRSSHubInstance
		if instance != nil && strings.TrimSpace(*instance) != "" {
			inst = *instance
		}
		if expanded := sources.ExpandRSSHub(rawURL, inst); expanded != nil {
			rawURL = *expanded
		}
	}

	// Step 0: multi-source normalization (YouTube / Reddit / Mastodon).
	effectiveURL := rawURL
	var forcedType *models.SourceType
	switch n := sources.NormalizeSource(rawURL); n.Kind {
	case sources.KindFeed:
		effectiveURL = n.URL
		ft := n.SourceType
		forcedType = &ft
	case sources.KindNeedsYoutubeResolution:
		pageBytes, ct, _, err := ingestion.Get(ctx, client, n.PageURL)
		if err != nil {
			return nil, err
		}
		channelID := sources.ExtractChannelID(ingestion.DecodeHTML(pageBytes, ct))
		if channelID == nil {
			return nil, apperr.Code("youtubeChannelNotFound")
		}
		effectiveURL = sources.YoutubeFeedURL(*channelID)
		yt := models.SourceYoutube
		forcedType = &yt
	}

	// Step 1: fetch whatever the user gave us (or the normalized feed URL).
	bytes, ct, finalURL, err := ingestion.Get(ctx, client, effectiveURL)
	if err != nil {
		return nil, err
	}

	// Step 2: a feed → use it directly; otherwise discover one in the page.
	feedURL, feedBytes := finalURL, bytes
	if !ingestion.LooksLikeFeed(bytes) {
		html := ingestion.DecodeHTML(bytes, ct)
		candidates := ingestion.DiscoverFeeds(html, finalURL)
		if len(candidates) == 0 {
			return nil, apperr.Code("noFeedFound")
		}
		fb, _, _, err := ingestion.Get(ctx, client, candidates[0])
		if err != nil {
			return nil, err
		}
		feedURL, feedBytes = candidates[0], fb
	}

	// Step 3: parse and classify.
	parsed, err := ingestion.ParseFeed(feedBytes, feedURL)
	if err != nil {
		return nil, err
	}
	sourceType := models.SourceRSS
	if forcedType != nil {
		sourceType = *forcedType
	} else {
		sourceType = ingestion.RefineSourceType(ingestion.DetectSourceType(feedURL), parsed, feedURL)
	}

	title := feedURL
	if parsed.Title != nil && *parsed.Title != "" {
		title = *parsed.Title
	}
	favicon := parsed.Icon
	if favicon == nil && parsed.SiteURL != nil {
		if u, err := url.Parse(*parsed.SiteURL); err == nil && u.Host != "" {
			f := fmt.Sprintf("https://www.google.com/s2/favicons?domain=%s&sz=64", u.Hostname())
			favicon = &f
		}
	}

	// Step 4: persist.
	if existing, err := db.FindFeedByURL(ctx, s.app.DB.W, feedURL); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, apperr.Code("alreadySubscribed")
	}
	feedID, err := db.InsertFeed(ctx, s.app.DB.W, feedURL, parsed.SiteURL, title, parsed.Description, sourceType, folderID)
	if err != nil {
		return nil, err
	}
	if favicon != nil {
		_ = db.UpdateFeedMeta(ctx, s.app.DB.W, feedID, nil, nil, nil, favicon)
	}
	dedup := db.SettingFlag(ctx, s.app.DB.R, "dedup_enabled", false)
	rules, _ := db.ActiveRules(ctx, s.app.DB.R)
	for i := range parsed.Articles {
		_, _ = db.UpsertArticle(ctx, s.app.DB, feedID, &parsed.Articles[i], dedup, rules)
	}
	_ = db.TouchFeed(ctx, s.app.DB.W, feedID)
	lastFetched, _ := db.FeedLastFetched(ctx, s.app.DB.R, feedID)
	unread, _ := db.CountFeedUnread(ctx, s.app.DB.R, feedID)

	return &models.Feed{
		ID:            feedID,
		FeedURL:       feedURL,
		SiteURL:       parsed.SiteURL,
		Title:         title,
		Description:   parsed.Description,
		FaviconURL:    favicon,
		FolderID:      folderID,
		SourceType:    sourceType,
		LastFetchedAt: lastFetched,
		FetchError:    nil,
		UnreadCount:   unread,
	}, nil
}

// QuickSubscribe registers a feed from metadata the caller already has —
// directory entries carry title/site/description — without the blocking
// fetch+parse+ingest AddFeed does. Bulk subscribe fires dozens of these, so
// the row must land instantly; articles belong to the background refresh that
// follows (lastFetched stays NULL, so the feed is immediately due). The
// favicon derives from the site host like AddFeed's fallback; the source type
// from the URL shape alone (directory entries are plain feeds in practice).
func (s *FeedService) QuickSubscribe(feedURL, title string, siteURL, description *string, folderID *int64) (*models.Feed, error) {
	ctx := bg()
	feedURL = strings.TrimSpace(feedURL)
	title = strings.TrimSpace(title)
	if u, err := url.Parse(feedURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || title == "" {
		return nil, apperr.Code("noFeedFound")
	}
	if existing, err := db.FindFeedByURL(ctx, s.app.DB.W, feedURL); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, apperr.Code("alreadySubscribed")
	}
	var favicon *string
	if siteURL != nil {
		if u, err := url.Parse(*siteURL); err == nil && u.Host != "" {
			f := fmt.Sprintf("https://www.google.com/s2/favicons?domain=%s&sz=64", u.Hostname())
			favicon = &f
		}
	}
	sourceType := ingestion.DetectSourceType(feedURL)
	feedID, err := db.InsertFeed(ctx, s.app.DB.W, feedURL, siteURL, title, description, sourceType, folderID)
	if err != nil {
		return nil, err
	}
	if favicon != nil {
		_ = db.UpdateFeedMeta(ctx, s.app.DB.W, feedID, nil, nil, nil, favicon)
	}
	return &models.Feed{
		ID:          feedID,
		FeedURL:     feedURL,
		SiteURL:     siteURL,
		Title:       title,
		Description: description,
		FaviconURL:  favicon,
		FolderID:    folderID,
		SourceType:  sourceType,
	}, nil
}

// SearchFeedDirectory discovers feeds matching a query — curated directory plus
// a live page scrape when the query looks like a URL. lang scopes the directory
// to the user's UI language. Mirrors commands.rs search_feed_directory.
func (s *FeedService) SearchFeedDirectory(query, lang string) ([]discovery.Result, error) {
	ctx := bg()
	results := []discovery.Result{}

	// Lazily refresh the directory sources in the background (TTL-gated, so
	// this is a couple of file stats almost every time). When a new merge
	// lands the frontend re-queries via the directory-updated event.
	discovery.RefreshIfStale(ctx, s.app.HTTP(), s.app.DataDir(), directorySources(ctx, s.app), func() {
		events.Emit("directory-updated")
	})

	if discovery.LooksLikeURL(query) {
		target := discovery.NormalizeQueryURL(query)
		if bytes, ct, finalURL, err := ingestion.Get(ctx, s.app.HTTP(), target); err == nil {
			if ingestion.LooksLikeFeed(bytes) {
				var title *string
				if parsed, err := ingestion.ParseFeed(bytes, finalURL); err == nil {
					title = parsed.Title
				}
				results = append(results, discovery.ResultFromScrape(finalURL, title))
			} else {
				html := ingestion.DecodeHTML(bytes, ct)
				for _, feedURL := range ingestion.DiscoverFeeds(html, finalURL) {
					results = append(results, discovery.ResultFromScrape(feedURL, nil))
				}
			}
		}
	}

	for _, hit := range discovery.SearchDirectory(query, lang) {
		dup := false
		for _, r := range results {
			if r.FeedURL == hit.FeedURL {
				dup = true
				break
			}
		}
		if !dup {
			results = append(results, hit)
		}
	}
	return results, nil
}

// InitDirectory loads the cached feed directory (official + user sources) at
// startup. Called once from main.go; refreshes stay lazy (directory access).
func InitDirectory(app *appstate.State) {
	discovery.Reload(app.DataDir(), directorySources(context.Background(), app))
}

// directorySources reads the user-added directory URL list (settings key
// "directory_sources", a JSON string array). The P2-era single "directory_url"
// override migrates into the list on first read, so upgrades keep their
// custom directory.
func directorySources(ctx context.Context, app *appstate.State) []string {
	urls := []string{}
	if raw, _ := db.GetSetting(ctx, app.DB.R, "directory_sources"); raw != nil {
		_ = json.Unmarshal([]byte(*raw), &urls)
	}
	if v, _ := db.GetSetting(ctx, app.DB.R, "directory_url"); v != nil && strings.TrimSpace(*v) != "" {
		legacy := strings.TrimSpace(*v)
		dup := false
		for _, u := range urls {
			if u == legacy {
				dup = true
				break
			}
		}
		if !dup {
			urls = append(urls, legacy)
		}
		if b, err := json.Marshal(urls); err == nil {
			_ = db.SetSetting(ctx, app.DB.W, "directory_sources", string(b))
			_ = db.SetSetting(ctx, app.DB.W, "directory_url", "")
		}
	}
	return urls
}

// saveDirectorySources persists the user source list.
func saveDirectorySources(ctx context.Context, app *appstate.State, urls []string) error {
	b, err := json.Marshal(urls)
	if err != nil {
		return err
	}
	return db.SetSetting(ctx, app.DB.W, "directory_sources", string(b))
}

// DirectorySource is one directory source for the settings UI.
type DirectorySource struct {
	// URL is empty for the official built-in source.
	URL string `json:"url"`
	// Official marks the built-in source (embed fallback; not removable).
	Official bool `json:"official"`
	// Count is the source's own entry count (before cross-source dedup).
	Count int `json:"count"`
	// UpdatedAt is the source envelope's authored date; "" when unknown.
	UpdatedAt string `json:"updatedAt"`
	// CheckedAt is the last remote check (RFC3339); "" when never fetched.
	CheckedAt string `json:"checkedAt"`
	// OK is false when the source has no valid cached copy — its entries are
	// missing from the merge until a refresh succeeds.
	OK bool `json:"ok"`
}

// listDirectorySources snapshots the per-source statuses into binding shape.
func (s *FeedService) listDirectorySources(ctx context.Context) []DirectorySource {
	statuses := discovery.Sources(s.app.DataDir(), directorySources(ctx, s.app))
	out := make([]DirectorySource, 0, len(statuses))
	for _, st := range statuses {
		d := DirectorySource{
			URL:       st.URL,
			Official:  st.Official,
			Count:     st.Count,
			UpdatedAt: st.UpdatedAt,
			OK:        st.OK,
		}
		if !st.CheckedAt.IsZero() {
			d.CheckedAt = st.CheckedAt.Format(time.RFC3339)
		}
		out = append(out, d)
	}
	return out
}

// ListDirectorySources returns every directory source (official first) with
// its per-source status, for settings → subscriptions.
func (s *FeedService) ListDirectorySources() ([]DirectorySource, error) {
	return s.listDirectorySources(bg()), nil
}

// AddDirectorySource validates, fetches and registers a user directory URL,
// then rebuilds the merge. The fetch is synchronous so a typo'd or invalid
// directory errors right in the add flow instead of failing silently later.
// Returns the updated source list.
func (s *FeedService) AddDirectorySource(rawURL string) ([]DirectorySource, error) {
	ctx := bg()
	target := strings.TrimSpace(rawURL)
	if u, err := url.Parse(target); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, apperr.Code("directorySourceInvalid")
	}
	urls := directorySources(ctx, s.app)
	if target == discovery.DefaultDirectoryURL {
		return nil, apperr.Code("directorySourceDuplicate")
	}
	for _, u := range urls {
		if u == target {
			return nil, apperr.Code("directorySourceDuplicate")
		}
	}
	if len(urls) >= discovery.MaxUserSources {
		return nil, apperr.Code("directoryTooManySources")
	}
	if _, err := discovery.FetchSource(ctx, s.app.HTTP(), s.app.DataDir(), target); err != nil {
		return nil, err
	}
	urls = append(urls, target)
	if err := saveDirectorySources(ctx, s.app, urls); err != nil {
		return nil, err
	}
	discovery.Reload(s.app.DataDir(), urls)
	events.Emit("directory-updated")
	return s.listDirectorySources(ctx), nil
}

// RemoveDirectorySource unregisters a user directory URL, deletes its cache,
// and rebuilds the merge. Returns the updated source list.
func (s *FeedService) RemoveDirectorySource(rawURL string) ([]DirectorySource, error) {
	ctx := bg()
	target := strings.TrimSpace(rawURL)
	urls := directorySources(ctx, s.app)
	kept := make([]string, 0, len(urls))
	for _, u := range urls {
		if u != target {
			kept = append(kept, u)
		}
	}
	if len(kept) == len(urls) {
		return s.listDirectorySources(ctx), nil // already gone — idempotent
	}
	if err := saveDirectorySources(ctx, s.app, kept); err != nil {
		return nil, err
	}
	discovery.RemoveSourceCache(s.app.DataDir(), target)
	discovery.Reload(s.app.DataDir(), kept)
	events.Emit("directory-updated")
	return s.listDirectorySources(ctx), nil
}

// DirectoryStatus describes the live curated directory for the settings UI.
type DirectoryStatus struct {
	// Count is how many entries the live directory holds (all languages).
	Count int `json:"count"`
	// UpdatedAt is the directory envelope's authored date; empty for a
	// legacy bare-array payload.
	UpdatedAt string `json:"updatedAt"`
	// CheckedAt is when the remote was last checked (RFC3339; a 304
	// revalidation counts). Empty when nothing was ever fetched.
	CheckedAt string `json:"checkedAt"`
	// Changed reports whether a RefreshFeedDirectory call swapped in a new
	// copy (false = the remote 304'd). Always false from DirectoryStatus.
	Changed bool `json:"changed"`
}

// directoryStatus snapshots the live directory into the binding shape.
func (s *FeedService) directoryStatus() *DirectoryStatus {
	st := discovery.CurrentStatus(s.app.DataDir())
	out := &DirectoryStatus{Count: st.Count, UpdatedAt: st.UpdatedAt}
	if !st.CheckedAt.IsZero() {
		out.CheckedAt = st.CheckedAt.Format(time.RFC3339)
	}
	return out
}

// DirectoryStatus reports the curated directory's entry count, authored date,
// and last remote check for settings → subscriptions.
func (s *FeedService) DirectoryStatus() (*DirectoryStatus, error) {
	return s.directoryStatus(), nil
}

// RefreshFeedDirectory forces a synchronous remote refresh of the curated
// directory (the settings "refresh now" button), bypassing the TTL. Returns
// the post-refresh status; a failed fetch/validation errors out and keeps the
// current copy. The directory-updated event still fires on a swap so any open
// Source Hub re-renders.
func (s *FeedService) RefreshFeedDirectory() (*DirectoryStatus, error) {
	ctx := bg()
	changed, err := discovery.RefreshAll(ctx, s.app.HTTP(), s.app.DataDir(), directorySources(ctx, s.app))
	if changed {
		// A partial failure can still have landed a new merge — notify first.
		events.Emit("directory-updated")
	}
	if err != nil {
		return nil, err
	}
	st := s.directoryStatus()
	st.Changed = changed
	return st, nil
}

// FeedPreview is what PreviewFeed returns: enough to render a pre-subscribe
// peek at a feed — identity plus its most recent item titles/dates (the
// frontend derives an update-frequency hint from the dates).
type FeedPreview struct {
	Title       string            `json:"title"`
	FeedURL     string            `json:"feedUrl"`
	SiteURL     *string           `json:"siteUrl"`
	Description *string           `json:"description"`
	Items       []FeedPreviewItem `json:"items"`
}

// FeedPreviewItem is one recent item in a feed preview.
type FeedPreviewItem struct {
	Title       string  `json:"title"`
	PublishedAt *string `json:"publishedAt"`
}

// previewItemCap bounds how many recent items a preview returns.
const previewItemCap = 5

// PreviewFeed fetches and parses a feed URL (following page → feed discovery
// like AddFeed, but read-only: nothing is persisted) and returns its identity
// plus the most recent items, so the UI can show what a feed looks like before
// the user subscribes.
func (s *FeedService) PreviewFeed(rawURL string) (*FeedPreview, error) {
	ctx := bg()
	client := s.app.HTTP()

	bytes, ct, finalURL, err := ingestion.Get(ctx, client, rawURL)
	if err != nil {
		return nil, err
	}
	feedURL, feedBytes := finalURL, bytes
	if !ingestion.LooksLikeFeed(bytes) {
		html := ingestion.DecodeHTML(bytes, ct)
		candidates := ingestion.DiscoverFeeds(html, finalURL)
		if len(candidates) == 0 {
			return nil, apperr.Code("noFeedFound")
		}
		fb, _, _, err := ingestion.Get(ctx, client, candidates[0])
		if err != nil {
			return nil, err
		}
		feedURL, feedBytes = candidates[0], fb
	}
	parsed, err := ingestion.ParseFeed(feedBytes, feedURL)
	if err != nil {
		return nil, err
	}

	title := feedURL
	if parsed.Title != nil && *parsed.Title != "" {
		title = *parsed.Title
	}
	items := []FeedPreviewItem{}
	for i := range parsed.Articles {
		if len(items) == previewItemCap {
			break
		}
		a := &parsed.Articles[i]
		items = append(items, FeedPreviewItem{Title: a.Title, PublishedAt: a.PublishedAt})
	}
	return &FeedPreview{
		Title:       title,
		FeedURL:     feedURL,
		SiteURL:     parsed.SiteURL,
		Description: parsed.Description,
		Items:       items,
	}, nil
}

// ListFeeds returns every feed with a live unread count, ordered by title.
func (s *FeedService) ListFeeds() ([]models.Feed, error) {
	return db.ListFeeds(bg(), s.app.DB.R)
}

// DeleteFeeds removes a batch of feeds in one transactional call (the settings
// batch-unsubscribe) rather than one IPC round-trip per feed; articles and
// enclosures cascade like DeleteFeed.
func (s *FeedService) DeleteFeeds(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return db.DeleteFeeds(bg(), s.app.DB, ids)
}

// DeleteFeed removes a feed; its articles and enclosures cascade.
func (s *FeedService) DeleteFeed(id int64) error {
	return db.DeleteFeed(bg(), s.app.DB.W, id)
}

// MoveFeed reassigns a feed to a folder (null = unfiled).
func (s *FeedService) MoveFeed(id int64, folderID *int64) error {
	return db.MoveFeed(bg(), s.app.DB.W, id, folderID)
}

// RenameFeed sets a feed's display title (rejecting an empty value) and marks
// it user-named so a later refresh does not revert it.
func (s *FeedService) RenameFeed(id int64, title string) error {
	return db.RenameFeed(bg(), s.app.DB.W, id, title)
}

// SetFeedURL repoints a feed at a new URL — for when a site moves its feed —
// keeping the feed's articles, folder and per-feed settings. Rejects an empty
// URL, and a URL another feed already uses (feed_url is UNIQUE).
func (s *FeedService) SetFeedURL(id int64, url string) error {
	return db.SetFeedURL(bg(), s.app.DB.W, id, url)
}

// SetFeedRefreshInterval sets a feed's per-feed refresh interval in minutes.
// null follows the global setting; 525600 opts the feed out of auto-refresh.
func (s *FeedService) SetFeedRefreshInterval(id int64, minutes *int64) error {
	return db.SetFeedRefreshInterval(bg(), s.app.DB.W, id, minutes)
}

// SetFeedAutoTranslate sets a feed's auto-translate override. null follows the
// global translate_auto setting; 0 forces off, 1 forces on.
func (s *FeedService) SetFeedAutoTranslate(id int64, mode *int64) error {
	return db.SetFeedAutoTranslate(bg(), s.app.DB.W, id, mode)
}

// SetFeedOpenMode sets a feed's per-feed open mode — how its articles open in
// the reader. null reverts the feed to the global default-open-mode
// preference.
func (s *FeedService) SetFeedOpenMode(id int64, mode *string) error {
	if mode != nil {
		switch *mode {
		case "reader", "extracted", "web":
		default:
			return apperr.Code("unknownOpenMode")
		}
	}
	return db.SetFeedOpenMode(bg(), s.app.DB.W, id, mode)
}

// RefreshFeeds refreshes every feed, streaming per-feed progress over the event
// "refresh:<streamID>" (started → feedDone* → finished). Returns the total
// number of new articles. The frontend subscribes to that event before calling.
// RefreshFeeds refreshes feeds, streaming progress over "refresh:<streamID>".
// With nil feedID and folderID it refreshes the whole library (the toolbar
// button); a non-nil feedID targets one feed, a non-nil folderID one folder. A
// feedID wins over a folderID when both are passed.
func (s *FeedService) RefreshFeeds(ctx context.Context, feedID *int64, folderID *int64, streamID string) (int, error) {
	// Sequenced (see events.Sequencer) so reordered Wails delivery can't shuffle
	// progress or let "finished" land before the last feedDone.
	seq := events.NewSequencer("refresh:" + streamID)
	emit := func(p ingestion.RefreshProgress) { seq.Emit(p) }
	scope := ingestion.ScopeAll
	var target int64
	switch {
	case feedID != nil:
		scope, target = ingestion.ScopeFeed, *feedID
	case folderID != nil:
		scope, target = ingestion.ScopeFolder, *folderID
	}
	return ingestion.RefreshAll(ctx, s.app, emit, false, scope, target)
}
