// hive — a local-first RSS reader. Wails3 application entry point: resolves the
// data directory, opens the database, wires the shared state, registers the
// bound services, and creates the main window.
//
// Ported from papr's src-tauri/src/lib.rs (the Tauri builder). Streaming /
// platform wiring (refresh scheduler, tray, deep links) is added in later phases.
package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"hive/internal/appstate"
	"hive/internal/db"
	"hive/internal/discovery"
	"hive/internal/events"
	"hive/internal/fonts"
	"hive/internal/ingestion"
	"hive/internal/models"
	"hive/services"
)

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	// Frontend events the backend emits. Registering them lets the binding
	// generator produce a strongly typed TS API. Per-request streaming events
	// (AI tokens, translation/refresh progress) are emitted under dynamic names
	// and are not registered here.
	application.RegisterEvent[any]("feeds-updated")
	application.RegisterEvent[string]("deep-link-subscribe")
	application.RegisterEvent[any]("tray-open-settings")
	application.RegisterEvent[any]("page-view-ready")
	application.RegisterEvent[any]("directory-updated")
}

// startupFatalDialog reports an unrecoverable startup failure (most commonly
// a database that cannot be opened) in a native error dialog, then exits.
// No window is created — the app never got far enough to need one.
func startupFatalDialog(err error) {
	app := application.New(application.Options{Name: "Hive"})
	app.Dialog.Error().
		SetTitle("Hive").
		SetMessage(fmt.Sprintf(
			"Hive could not open its database. Your data has not been reset. If you used a newer build, install a compatible version.\n\n%v",
			err,
		)).
		Show()
	os.Exit(1)
}

// dataDir resolves (and creates) the per-user application data directory. On
// macOS this is ~/Library/Application Support/Hive.
func dataDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base, _ = os.UserHomeDir()
	}
	dir := filepath.Join(base, "Hive")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func main() {
	state, err := appstate.New(dataDir())
	if err != nil {
		// papr's v0.16.1 startup-failure fix (#141): an unopenable database is
		// reported in a native dialog instead of dying in the terminal — a
		// double-clicked app has no visible stdout, so a bare exit reads as
		// "the app is broken". The dialog says the data is untouched. The
		// macOS message dialog runs its own modal loop, so it works before
		// app.Run() starts the main event loop.
		startupFatalDialog(err)
	}
	defer state.Close()

	// Swap in the last downloaded feed directory — official + user sources
	// (falls back to the embedded copy). Refreshes happen lazily when the
	// discovery UI is opened.
	services.InitDirectory(state)

	svc := services.New(state)

	// handleDeepLink parses a hive://subscribe?url=… link, buffers it for the
	// frontend's cold-start drain, and emits the live event the Add-feed dialog
	// listens for. mainWindow is captured below (assigned after creation).
	var mainWindow *application.WebviewWindow
	handleDeepLink := func(args []string) {
		for _, a := range args {
			if !strings.HasPrefix(a, "hive://") {
				continue
			}
			if url := discovery.ParseDeepLink(a); url != nil {
				state.SetPendingDeepLink(*url)
				events.Emit("deep-link-subscribe", *url)
				if mainWindow != nil {
					mainWindow.Show()
					mainWindow.Focus()
				}
			}
		}
	}

	app := application.New(application.Options{
		Name:        "Hive",
		Description: "A fast, native RSS reader for the desktop.",
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "ai.hive.reader",
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				handleDeepLink(data.Args)
			},
		},
		Services: []application.Service{
			application.NewService(svc.Folder),
			application.NewService(svc.Feed),
			application.NewService(svc.Article),
			application.NewService(svc.Tag),
			application.NewService(svc.Rule),
			application.NewService(svc.Setting),
			application.NewService(svc.Highlight),
			application.NewService(svc.Storage),
			application.NewService(svc.System),
			application.NewService(svc.AI),
			application.NewService(svc.Sync),
			application.NewService(svc.Opml),
			application.NewService(svc.Newsletter),
			application.NewService(svc.PageView),
			application.NewService(svc.Font),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
			// Serve downloaded fonts from <dataDir>/fonts at /userfonts/* (the
			// font-switching feature); all other requests fall through to the
			// embedded dist handler.
			Middleware: fonts.AssetMiddleware(state.DataDir()),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	// Build the HTTP client from persisted network settings, then start the
	// background refresh scheduler (it looks for due feeds on each tick).
	_ = svc.Setting.ApplyNetworkSettings()
	ingestion.SpawnScheduler(state)

	mainWindow = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Hive",
		Width:  1200,
		Height: 800,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: startupBackground(state),
		URL:              "/",
	})

	// The in-app original-page view (services/pageview.go) floats a child window
	// over the reader pane; it needs the main window to align to it.
	services.SetPageView(mainWindow)
	// Let the frontend repaint the native backing when the theme changes.
	services.SetMainWindow(mainWindow)

	// ── macOS menu-bar tray (keeps the app resident for background refreshes) ──
	tray := app.SystemTray.New()
	tray.SetLabel("🐝")
	tray.SetTooltip("Hive")
	tray.SetMenu(buildTrayMenu(app, state, mainWindow))
	// Re-localize the native tray when the UI language changes: the frontend
	// calls SystemService.RefreshTray right after persisting "language", which
	// invokes this to rebuild the menu in the new locale. (SetMenu dispatches to
	// the main thread itself, so calling it from the service goroutine is safe.)
	services.SetTrayRefresh(func() {
		tray.SetMenu(buildTrayMenu(app, state, mainWindow))
	})

	// A cold-start launch carrying a hive:// link (Linux/Windows pass it via
	// argv; the frontend also drains the buffer on mount via TakePendingDeepLink).
	handleDeepLink(os.Args[1:])

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// startupBackground picks the native window backing for the first frame from
// the mirrored appearance settings ("theme" is the *resolved* light/dark;
// "dark_shade" its dark sub-level), so a dark-theme launch never flashes the
// light paper before the webview paints. Colours mirror styles.css --paper
// (light #F0EEE9; dark #12110F / #0C0B0A / #000000). After launch the
// frontend keeps the backing in sync via SystemService.SetWindowBackground.
func startupBackground(state *appstate.State) application.RGBA {
	ctx := context.Background()
	if v, err := db.GetSetting(ctx, state.DB.R, "theme"); err == nil && v != nil && *v == "dark" {
		shade, _ := db.GetSetting(ctx, state.DB.R, "dark_shade")
		switch {
		case shade != nil && *shade == "black":
			return application.NewRGB(0, 0, 0)
		case shade != nil && *shade == "dimmer":
			return application.NewRGB(12, 11, 10)
		default:
			return application.NewRGB(18, 17, 15)
		}
	}
	return application.NewRGB(240, 238, 233)
}

// trayStrings holds the menu-bar tray labels per UI language. The tray is a
// native menu built in Go, so the webview's vue-i18n never reaches it — these
// mirror the frontend locale wording. Unknown languages fall back to English.
var trayStrings = map[string]struct {
	unread   string // printf template with a single %d
	refresh  string
	markRead string
	settings string
	quit     string
}{
	"en": {"%d unread", "Refresh now", "Mark all read", "Settings…", "Quit Hive"},
	"zh": {"%d 未读", "立即刷新", "全部标为已读", "设置…", "退出 Hive"},
	"ja": {"未読 %d 件", "今すぐ更新", "すべて既読にする", "設定…", "Hive を終了"},
}

// trayLang resolves the tray language: the primary subtag of the persisted
// "language" setting (zh / en / ja), defaulting to English.
func trayLang(state *appstate.State) string {
	v, _ := db.GetSetting(context.Background(), state.DB.R, "language")
	if v == nil {
		return "en"
	}
	lang := strings.ToLower(strings.TrimSpace(*v))
	if i := strings.IndexAny(lang, "-_"); i >= 0 {
		lang = lang[:i]
	}
	if _, ok := trayStrings[lang]; !ok {
		return "en"
	}
	return lang
}

// buildTrayMenu constructs the menu-bar tray menu: a live unread-count status
// line plus quick actions (refresh, mark all read, open settings, quit),
// localized to the current UI language. Mirrors papr's src-tauri/src/tray.rs.
func buildTrayMenu(app *application.App, state *appstate.State, win *application.WebviewWindow) *application.Menu {
	tr := trayStrings[trayLang(state)]
	m := app.NewMenu()
	unread, _ := db.CountUnread(context.Background(), state.DB.R)
	m.Add(fmt.Sprintf(tr.unread, unread)).SetEnabled(false)
	m.AddSeparator()
	m.Add(tr.refresh).OnClick(func(*application.Context) {
		go ingestion.RefreshAll(context.Background(), state, nil, false, ingestion.ScopeAll, 0)
	})
	m.Add(tr.markRead).OnClick(func(*application.Context) {
		ctx := context.Background()
		_, _ = db.MarkAllRead(ctx, state.DB, models.ArticleQuery{Kind: models.QueryAll}, db.IsFreshrssConnected(ctx, state.DB.R))
		events.Emit("feeds-updated", 0)
	})
	m.Add(tr.settings).OnClick(func(*application.Context) {
		if win != nil {
			win.Show()
			win.Focus()
		}
		// The payload is REQUIRED even though the frontend ignores it: a
		// RegisterEvent[any] event emitted with nil data trips a nil-pointer
		// panic in Wails' validateCustomEvent (reflect.TypeOf(nil).Implements),
		// which events.Emit silently recovers — so the event is dropped and
		// Settings never opens. Any non-nil value sidesteps the bug.
		events.Emit("tray-open-settings", true)
	})
	m.AddSeparator()
	m.Add(tr.quit).OnClick(func(*application.Context) {
		app.Quit()
	})
	return m
}
