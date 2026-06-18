// Package syncer synchronises read/starred/subscription state over the Google
// Reader compatible API.
//
// Supports any GReader-compatible backend; today FreshRSS and Miniflux.
// Protocol is identical (ClientLogin, reader/api/0/edit-tag,
// stream/contents/..., com.google/* tags) — only the API root path differs per
// provider, so the provider mapping centralises that.
//
// Flow: ClientLogin for an auth token, push any queued local read/starred
// changes via edit-tag, pull the subscription list (to subscribe locally to new
// server feeds) and push any local-only feeds back to the server (so the two
// subscription lists converge rather than drifting), then pull the recent
// reading-list (to reconcile read/starred state, matched to local articles by
// URL).
//
// Ported from papr's src-tauri/src/sync.rs. The Rust code threaded a Tauri
// AppHandle to reach the shared HTTP client and database; here the caller (the
// service layer) passes them in directly, so this package stays Wails-agnostic.
//
// The package is named `syncer`, not `sync`, so it does not shadow the standard
// library's sync package.
package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"hive/internal/apperr"
	"hive/internal/db"
	"hive/internal/models"
)

// GReader stream tags. These are protocol constants shared by every
// GReader-compatible backend.
const (
	readTag     = "user/-/state/com.google/read"
	starredTag  = "user/-/state/com.google/starred"
	readingList = "user/-/state/com.google/reading-list"
)

// Settings keys. Still named freshrss_* for backwards compatibility with
// installs that predate multi-provider support — the values are
// provider-agnostic. (Mirrors the Rust comment on `creds`.)
const (
	keyURL      = "freshrss_url"
	keyUser     = "freshrss_user"
	keyAuth     = "freshrss_auth"
	keyPass     = "freshrss_pass" // legacy plaintext password, awaiting migration
	keyProvider = "freshrss_provider"
)

// provider identifies which GReader-compatible backend the user is connected
// to. The wire protocol is identical; only where the API root sits under the
// server URL differs (FreshRSS mounts it at /api/greader.php, Miniflux serves
// it at the server root).
type provider int

const (
	providerFreshRSS provider = iota
	providerMiniflux
)

// pathSuffix is the path segment to append to the user-supplied server URL to
// reach the GReader API root. Miniflux serves /accounts/ClientLogin and
// /reader/api/0/... straight off the server root, so its suffix is empty.
func (p provider) pathSuffix() string {
	switch p {
	case providerMiniflux:
		return ""
	default:
		return "/api/greader.php"
	}
}

// String is the persisted/serialized name of the provider.
func (p provider) String() string {
	switch p {
	case providerMiniflux:
		return "miniflux"
	default:
		return "freshrss"
	}
}

// providerFromSetting parses the persisted setting. Missing / unknown →
// FreshRSS, so older installs (where this setting didn't exist) keep working
// unchanged.
func providerFromSetting(s *string) provider {
	v := ""
	if s != nil {
		v = strings.TrimSpace(*s)
	}
	if v == "miniflux" {
		return providerMiniflux
	}
	return providerFreshRSS
}

// greaderBase normalises a user-supplied server URL to its GReader API root for
// the chosen provider. Idempotent: if the user already typed the full path,
// don't append it again.
func greaderBase(rawURL string, p provider) string {
	t := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	suffix := p.pathSuffix()
	if suffix == "" {
		return t
	}
	if strings.HasSuffix(t, suffix) || strings.Contains(t, suffix+"/") {
		return t
	}
	return t + suffix
}

// Status mirrors types.ts FreshRssStatus: the connection state surfaced to the
// frontend. Provider is always present (defaults to "freshrss") so the UI never
// has to guess for older installs.
type Status struct {
	Connected bool    `json:"connected"`
	URL       *string `json:"url"`
	Provider  string  `json:"provider"`
}

// session is an authenticated GReader session: the API root, the long-lived
// auth token, and a fresh write (edit-tag) token.
type session struct {
	base  string
	auth  string
	token string
}

// authHeader is the GoogleLogin Authorization header value for this session.
func (s *session) authHeader() string {
	return "GoogleLogin auth=" + s.auth
}

// apiURL builds a full reader/api/0/<path> URL under the session's base.
func (s *session) apiURL(path string) string {
	return fmt.Sprintf("%s/reader/api/0/%s", s.base, path)
}

// creds is a stored GReader connection. We persist the long-lived auth token
// rather than the password — a leaked token is revocable server-side and can't
// be replayed against the user's other accounts. legacyPass holds a plaintext
// password from an older install, awaiting one-time migration.
type creds struct {
	url        string
	user       string
	auth       *string
	legacyPass *string
	provider   provider
}

// httpStatusOK reports whether a status code is in the 2xx success range,
// mirroring reqwest's error_for_status().
func httpStatusOK(code int) bool { return code >= 200 && code < 300 }

// doForm POSTs an application/x-www-form-urlencoded body with the session's
// Authorization header and reports whether the server returned a 2xx. The
// response body is drained and closed so the connection can be reused.
// Transport errors and non-2xx statuses both report false.
func doForm(ctx context.Context, client *http.Client, sess *session, path string, form url.Values) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sess.apiURL(path), strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", sess.authHeader())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return httpStatusOK(resp.StatusCode)
}

// getJSON GETs an authenticated reader/api/0/<path> endpoint and decodes the
// JSON body into out. A non-2xx status maps to the supplied error code.
func getJSON(ctx context.Context, client *http.Client, sess *session, path, failCode string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sess.apiURL(path), nil)
	if err != nil {
		return apperr.Wrap(failCode, err)
	}
	req.Header.Set("Authorization", sess.authHeader())
	resp, err := client.Do(req)
	if err != nil {
		return apperr.Wrap(failCode, err)
	}
	defer resp.Body.Close()
	if !httpStatusOK(resp.StatusCode) {
		return apperr.Code(failCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return apperr.Wrap(failCode, err)
	}
	return nil
}

// clientLogin exchanges username + password for a long-lived auth token.
func clientLogin(ctx context.Context, client *http.Client, base, user, pass string) (string, error) {
	form := url.Values{"Email": {user}, "Passwd": {pass}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/accounts/ClientLogin", strings.NewReader(form.Encode()))
	if err != nil {
		return "", apperr.Wrap("freshrssLoginFailed", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", apperr.Wrap("freshrssLoginFailed", err)
	}
	defer resp.Body.Close()
	if !httpStatusOK(resp.StatusCode) {
		return "", apperr.Code("freshrssLoginFailed")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", apperr.Wrap("freshrssLoginFailed", err)
	}
	// The body is a small key=value document; find the Auth= line.
	for _, line := range strings.Split(string(body), "\n") {
		if rest, ok := strings.CutPrefix(line, "Auth="); ok {
			token := strings.TrimSpace(rest)
			if token != "" {
				return token, nil
			}
		}
	}
	return "", apperr.Code("freshrssNoToken")
}

// sessionWithToken builds a session from an existing auth token by fetching a
// fresh write (edit-tag) token. Fails fast if the auth token is no longer valid.
func sessionWithToken(ctx context.Context, client *http.Client, base, auth string) (*session, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/reader/api/0/token", nil)
	if err != nil {
		return nil, apperr.Wrap("freshrssLoginFailed", err)
	}
	req.Header.Set("Authorization", "GoogleLogin auth="+auth)
	resp, err := client.Do(req)
	if err != nil {
		return nil, apperr.Wrap("freshrssLoginFailed", err)
	}
	defer resp.Body.Close()
	if !httpStatusOK(resp.StatusCode) {
		return nil, apperr.Code("freshrssLoginFailed")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, apperr.Wrap("freshrssLoginFailed", err)
	}
	return &session{base: base, auth: auth, token: strings.TrimSpace(string(body))}, nil
}

// login logs in with username + password and obtains a full session.
func login(ctx context.Context, client *http.Client, base, user, pass string) (*session, error) {
	auth, err := clientLogin(ctx, client, base, user, pass)
	if err != nil {
		return nil, err
	}
	return sessionWithToken(ctx, client, base, auth)
}

// subList is the subscription/list response.
type subList struct {
	Subscriptions []sub `json:"subscriptions"`
}

type sub struct {
	URL        *string  `json:"url"`
	Title      *string  `json:"title"`
	Categories []subCat `json:"categories"`
}

// subCat is a GReader category ("label") a subscription belongs to.
// FreshRSS/Miniflux folders surface here; we map the first named one onto a
// local folder.
type subCat struct {
	ID    string  `json:"id"`
	Label *string `json:"label"`
}

// folderName returns the human folder name for this category, or nil to skip
// it. Prefer the explicit label, otherwise derive it from the
// user/-/label/NAME id. nil for an unnamed category, so it is skipped rather
// than creating a blank folder.
//
// FreshRSS files every feed the user hasn't categorised under a built-in
// "Uncategorized" label. That isn't a real folder — mapping it onto a local one
// buries every top-level feed in a junk folder that doesn't match the server's
// own presentation — so it is treated as no folder.
func (c subCat) folderName() *string {
	var name string
	if c.Label != nil {
		name = strings.TrimSpace(*c.Label)
	}
	if name == "" {
		// Derive from the `user/-/label/NAME` id.
		if _, after, ok := strings.Cut(c.ID, "/label/"); ok {
			name = strings.TrimSpace(after)
		}
	}
	if name == "" {
		return nil
	}
	if strings.EqualFold(name, "Uncategorized") {
		return nil
	}
	return &name
}

// contents is the stream/contents response.
type contents struct {
	Items []item `json:"items"`
	// Present when the stream has more pages; fed back as `c=` to page on.
	Continuation string `json:"continuation"`
}

type item struct {
	ID        string `json:"id"`
	Canonical []href `json:"canonical"`
	Alternate []href `json:"alternate"`
}

type href struct {
	Href string `json:"href"`
}

// remoteItem is a single article the server flags as unread or starred: its
// item id and the canonical URL we match local articles on.
type remoteItem struct {
	id  string
	url string
}

// fetchStreamItems fetches every item of a GReader stream, following
// `continuation` pages. xt, when non-empty, is an "exclude tag" filter (e.g.
// exclude read to get only unread).
//
// Paging is capped: the sets we fetch this way (unread, starred) are bounded by
// what the user hasn't yet read / has starred — normally a few hundred items —
// so maxPages pages of n=1000 (tens of thousands of items) is a generous
// ceiling that also stops a pathological server from looping us forever.
func fetchStreamItems(ctx context.Context, client *http.Client, sess *session, stream, xt string) ([]remoteItem, error) {
	const pageSize = "1000"
	const maxPages = 30
	var out []remoteItem
	cont := ""
	for page := 0; page < maxPages; page++ {
		params := url.Values{"output": {"json"}, "n": {pageSize}}
		if xt != "" {
			params.Set("xt", xt)
		}
		if cont != "" {
			params.Set("c", cont)
		}
		var body contents
		if err := getJSON(ctx, client, sess,
			"stream/contents/"+stream+"?"+params.Encode(), "freshrssLoginFailed", &body); err != nil {
			return nil, err
		}
		for _, it := range body.Items {
			// Prefer the canonical link, falling back to the first alternate.
			u := ""
			if len(it.Canonical) > 0 {
				u = it.Canonical[0].Href
			} else if len(it.Alternate) > 0 {
				u = it.Alternate[0].Href
			}
			if u != "" {
				out = append(out, remoteItem{id: it.ID, url: u})
			}
		}
		if body.Continuation == "" {
			break
		}
		cont = body.Continuation
	}
	return out, nil
}

// loadCreds reads the stored GReader credentials, returning nil when no server
// is configured. The setting keys are named freshrss_* for backwards
// compatibility (see the constant block) — the values are provider-agnostic.
func loadCreds(ctx context.Context, q db.Querier) (*creds, error) {
	url, err := getSettingOrEmpty(ctx, q, keyURL)
	if err != nil {
		return nil, err
	}
	user, err := getSettingOrEmpty(ctx, q, keyUser)
	if err != nil {
		return nil, err
	}
	auth, err := nonemptySetting(ctx, q, keyAuth)
	if err != nil {
		return nil, err
	}
	legacyPass, err := nonemptySetting(ctx, q, keyPass)
	if err != nil {
		return nil, err
	}
	provSetting, err := db.GetSetting(ctx, q, keyProvider)
	if err != nil {
		return nil, err
	}
	prov := providerFromSetting(provSetting)

	if strings.TrimSpace(url) == "" || user == "" || (auth == nil && legacyPass == nil) {
		return nil, nil
	}
	return &creds{
		url:        url,
		user:       user,
		auth:       auth,
		legacyPass: legacyPass,
		provider:   prov,
	}, nil
}

// getSettingOrEmpty reads a setting, treating a missing key as the empty string
// (mirrors Rust's `.unwrap_or_default()`).
func getSettingOrEmpty(ctx context.Context, q db.Querier, key string) (string, error) {
	v, err := db.GetSetting(ctx, q, key)
	if err != nil {
		return "", err
	}
	if v == nil {
		return "", nil
	}
	return *v, nil
}

// nonemptySetting reads a setting and filters out the empty string, mirroring
// Rust's `db::get_setting(..).map(|v| v.filter(|s| !s.is_empty()))`.
func nonemptySetting(ctx context.Context, q db.Querier, key string) (*string, error) {
	v, err := db.GetSetting(ctx, q, key)
	if err != nil {
		return nil, err
	}
	if v == nil || *v == "" {
		return nil, nil
	}
	return v, nil
}

// persistSession persists a verified connection, storing the auth token and
// never the password (any legacy stored password is also cleared).
func persistSession(ctx context.Context, q db.Querier, rawURL, user, auth string, p provider) error {
	if err := db.SetSetting(ctx, q, keyURL, strings.TrimSpace(rawURL)); err != nil {
		return err
	}
	if err := db.SetSetting(ctx, q, keyUser, user); err != nil {
		return err
	}
	if err := db.SetSetting(ctx, q, keyAuth, auth); err != nil {
		return err
	}
	if err := db.SetSetting(ctx, q, keyPass, ""); err != nil {
		return err
	}
	return db.SetSetting(ctx, q, keyProvider, p.String())
}

// Connect verifies credentials against the server via ClientLogin and, on
// success, persists them (storing the auth token, never the password). provider
// is "freshrss" or "miniflux"; an unknown value defaults to FreshRSS.
func Connect(ctx context.Context, client *http.Client, database *db.DB, rawURL, username, password, providerName string) error {
	prov := providerFromSetting(&providerName)
	base := greaderBase(rawURL, prov)
	sess, err := login(ctx, client, base, username, password) // verifies credentials
	if err != nil {
		return err
	}
	return persistSession(ctx, database.W, rawURL, username, sess.auth, prov)
}

// Disconnect forgets the stored GReader credentials.
func Disconnect(ctx context.Context, q db.Querier) error {
	for _, key := range []string{keyURL, keyUser, keyAuth, keyPass, keyProvider} {
		if err := db.SetSetting(ctx, q, key, ""); err != nil {
			return err
		}
	}
	return nil
}

// GetStatus returns the current connection status: whether a server is
// configured, its URL, and the provider (always present, defaulting to
// "freshrss").
func GetStatus(ctx context.Context, q db.Querier) (Status, error) {
	c, err := loadCreds(ctx, q)
	if err != nil {
		return Status{}, err
	}
	if c == nil {
		return Status{Connected: false, URL: nil, Provider: providerFreshRSS.String()}, nil
	}
	u := c.url
	return Status{Connected: true, URL: &u, Provider: c.provider.String()}, nil
}

// feedsToPush returns the local feed URLs the server doesn't already carry, so
// each can be subscribed remotely. Pure set difference, factored out of Sync so
// the selection is unit-testable without a live server.
func feedsToPush(local []string, server map[string]struct{}) []string {
	out := make([]string, 0)
	for _, u := range local {
		if _, ok := server[u]; !ok {
			out = append(out, u)
		}
	}
	return out
}

// Sync runs a full sync: push queued changes, then pull subscriptions and
// read/starred state. Returns the number of local articles whose state was
// reconciled. Requires a connected server (returns freshrssNotConnected
// otherwise).
func Sync(ctx context.Context, client *http.Client, database *db.DB) (int, error) {
	c, err := loadCreds(ctx, database.R)
	if err != nil {
		return 0, err
	}
	if c == nil {
		return 0, apperr.Code("freshrssNotConnected")
	}
	base := greaderBase(c.url, c.provider)

	var sess *session
	if c.auth != nil {
		sess, err = sessionWithToken(ctx, client, base, *c.auth)
		if err != nil {
			return 0, err
		}
	} else {
		// Legacy install: exchange the plaintext password for a token, then
		// migrate so the password is no longer kept on disk.
		pass := ""
		if c.legacyPass != nil {
			pass = *c.legacyPass
		}
		sess, err = login(ctx, client, base, c.user, pass)
		if err != nil {
			return 0, err
		}
		if err := persistSession(ctx, database.W, c.url, c.user, sess.auth, c.provider); err != nil {
			return 0, err
		}
	}

	// 1 ── push: flush queued local read/starred changes. TakeSyncQueue removes
	// the rows up front, so any push that fails must be re-queued — otherwise a
	// network blip silently drops the user's change forever.
	queue, err := db.TakeSyncQueue(ctx, database)
	if err != nil {
		return 0, err
	}
	var failed []db.SyncEntry
	for _, entry := range queue {
		tag := readTag
		if entry.Field == "starred" {
			tag = starredTag
		}
		// "a" adds the tag (set), "r" removes it (clear).
		action := "r"
		if entry.Value {
			action = "a"
		}
		form := url.Values{
			"i":    {entry.RemoteID},
			action: {tag},
			"T":    {sess.token},
		}
		if !doForm(ctx, client, sess, "edit-tag", form) {
			failed = append(failed, entry)
		}
	}
	if len(failed) > 0 {
		// Re-queue failed pushes so the change survives to the next sync.
		for _, entry := range failed {
			_ = db.RequeueSync(ctx, database.W, entry.ArticleID, entry.Field, entry.Value)
		}
	}

	// 2 ── pull subscriptions: subscribe locally to any feed we don't have.
	var subs subList
	if err := getJSON(ctx, client, sess, "subscription/list?output=json", "freshrssLoginFailed", &subs); err != nil {
		return 0, err
	}
	serverURLs := make(map[string]struct{}, len(subs.Subscriptions))
	for _, s := range subs.Subscriptions {
		if s.URL != nil && *s.URL != "" {
			serverURLs[*s.URL] = struct{}{}
		}
	}
	for _, s := range subs.Subscriptions {
		// Resolve the server-side folder (GReader "label") and map it onto a
		// local folder by name.
		var folderID *int64
		for _, cat := range s.Categories {
			if name := cat.folderName(); name != nil {
				id, err := db.FolderIDByName(ctx, database.W, *name)
				if err != nil {
					return 0, err
				}
				folderID = &id
				break
			}
		}
		if s.URL == nil || *s.URL == "" {
			continue
		}
		feedURL := *s.URL
		existing, err := db.FindFeedByURL(ctx, database.W, feedURL)
		if err != nil {
			return 0, err
		}
		if existing == nil {
			title := feedURL
			if s.Title != nil && *s.Title != "" {
				title = *s.Title
			}
			// detect_source_type is not yet ported to hive; insert as the
			// generic "rss" kind. A later refresh's RefineFeedSourceType
			// promotes it once the real kind is known (and its WHERE guard makes
			// that strictly a promotion). See deviations in the port report.
			_, _ = db.InsertFeed(ctx, database.W, feedURL, nil, title, nil, models.SourceRSS, folderID)
		} else if folderID != nil {
			// Reconcile the folder for a feed we already track, but only when it
			// isn't filed locally yet — don't yank a feed the user has organised
			// by hand back into the server's folder.
			current, err := db.FeedFolderID(ctx, database.W, *existing)
			if err != nil {
				return 0, err
			}
			if current == nil {
				if err := db.MoveFeed(ctx, database.W, *existing, folderID); err != nil {
					return 0, err
				}
			}
		}
	}

	// 2b ── push subscriptions: subscribe the server to any local feed it
	// doesn't have yet, so adding a feed in the app propagates to the server
	// instead of leaving the two sides to drift. Best-effort and idempotent —
	// re-subscribing a feed the server already has is a no-op there.
	localFeeds, err := db.FeedURLsForSync(ctx, database.R)
	if err != nil {
		return 0, err
	}
	for _, u := range feedsToPush(localFeeds, serverURLs) {
		form := url.Values{
			"ac": {"subscribe"},
			"s":  {"feed/" + u},
			"T":  {sess.token},
		}
		// Best-effort: a failure here is logged in Rust and otherwise ignored;
		// we likewise do not abort the sync.
		_ = doForm(ctx, client, sess, "subscription/edit", form)
	}

	// 3 ── reconcile read/starred state, treating the server as the source of
	// truth. Hive polls each subscribed feed's raw RSS independently and marks
	// every fetched item unread, so local unread counts drift far above the
	// server's. Rather than pull the (unbounded) full history, fetch just the
	// server's *unread* and *starred* sets — both small, paged via continuation
	// — then mark every other synced article read. This is what a pure GReader
	// client effectively does, and it fixes the runaway unread counts the old
	// "reconcile the most recent 1000 items" pass caused by never reaching the
	// long tail of already-read articles.
	unread, err := fetchStreamItems(ctx, client, sess, readingList, readTag)
	if err != nil {
		return 0, err
	}
	starred, err := fetchStreamItems(ctx, client, sess, starredTag, "")
	if err != nil {
		return 0, err
	}

	unreadURLs := make(map[string]struct{}, len(unread))
	for _, it := range unread {
		unreadURLs[it.url] = struct{}{}
	}
	starredURLs := make(map[string]struct{}, len(starred))
	for _, it := range starred {
		starredURLs[it.url] = struct{}{}
	}

	// Assign remote ids for the items we did fetch, so a later local edit on
	// one of them has an id to push. (Read articles in the long tail carry no
	// id until they next appear in an unread/starred response — pushing a
	// change on those waits, exactly as before this change.)
	for _, it := range append(append([]remoteItem{}, unread...), starred...) {
		aid, err := db.ArticleIDByURL(ctx, database.W, it.url)
		if err != nil {
			return 0, err
		}
		if aid == nil {
			continue
		}
		if err := db.SetRemoteID(ctx, database.W, *aid, it.id); err != nil {
			return 0, err
		}
	}

	// Scope the read/starred sweep to feeds the server actually knows about,
	// so a local-only feed not yet mirrored server-side isn't wrongly marked
	// all-read. Articles with an un-pushed local edit are left untouched.
	serverFeedIDs, err := db.FeedIDsByURLs(ctx, database.R, serverURLs)
	if err != nil {
		return 0, err
	}
	return db.ReconcileSyncState(ctx, database, serverFeedIDs, unreadURLs, starredURLs)
}
