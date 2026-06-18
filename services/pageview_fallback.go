// In-app original-page view — non-darwin fallback.
//
// macOS embeds a WKWebView subview directly into the main window's content
// view (pageview_darwin.go); Wails3 exposes no cross-platform embedded child
// webview, so other platforms keep this approximation: a frameless window
// created on demand and kept aligned to the reader rectangle the frontend
// reports (logical CSS px, relative to the main window content top-left).
// Same IPC contract on every platform: open / set-bounds / close / suspend,
// plus the "page-view-ready" reveal event.

//go:build !darwin

package services

import (
	"math"
	"sync"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// pvMain is the main window, injected from main.go after it is created. Kept as
// a package-level value set by a plain function (not a service method) so the
// non-serialisable *WebviewWindow never reaches the TS binding generator.
var pvMain *application.WebviewWindow

// pvChild is the lazily-created original-page window (nil when closed).
var pvChild *application.WebviewWindow

// SetPageView wires the main window for PageViewService. Called once from main.go.
func SetPageView(main *application.WebviewWindow) { pvMain = main }

// PageViewService is the bound IPC surface for the in-app original-page view.
type PageViewService struct {
	mu        sync.Mutex
	hookOnce  sync.Once
	open      bool
	suspended bool // temporarily hidden for an open DOM modal, still "open"
	// minimized mirrors the main window's minimized / app-hidden state. AppKit
	// detaches child windows when their parent miniaturizes, so without this
	// the page view would stay floating on screen after the main window is
	// gone — and its orphaned presence stops a Dock click from deminiaturizing
	// the main window ("the app won't come back"). Tracked separately from
	// `suspended` because the two hide reasons clear independently.
	minimized      bool
	rx, ry, rw, rh float64    // last reader rect (main-window content px, fractional)
	revealOnce     *sync.Once // guards the current navigation's one-shot reveal
}

// armReveal sets up a fresh one-shot reveal for the navigation now starting,
// plus an 8s timeout fallback for a page that errors/hangs and never fires
// WebViewDidFinishNavigation. The window's didFinish handler (bound once, in
// OpenPageView) reads s.revealOnce so it always fires the current navigation's
// reveal — including on a reused window. Caller holds s.mu.
func (s *PageViewService) armReveal(child *application.WebviewWindow) {
	once := &sync.Once{}
	s.revealOnce = once
	go func() {
		time.Sleep(8 * time.Second)
		once.Do(func() { s.revealPageView(child) })
	}()
}

// OpenPageView shows the original page at url over the reader rectangle
// (x, y, width, height — logical px relative to the main window content). The
// window is created on first use and reused (navigate + move) so switching
// articles doesn't flash a teardown/rebuild.
func (s *PageViewService) OpenPageView(url string, x, y, width, height float64) error {
	app := application.Get()
	if app == nil || pvMain == nil {
		return nil
	}
	s.installHooks()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rx, s.ry, s.rw, s.rh = x, y, width, height
	s.open = true
	s.suspended = false

	if pvChild != nil {
		// Reused window: drop it to alpha 0 (hiding the stale page), navigate,
		// and re-arm the reveal so the new page fades in once loaded — same
		// flash-free handoff as a first open, rather than showing the old page
		// then white-flashing through the load.
		child := pvChild
		s.armReveal(child)
		application.InvokeSync(func() {
			setWindowAlpha(child.NativeWindow(), 0)
		})
		child.SetURL(url)
		s.applyLocked()
		return nil
	}

	// First open: create the window HIDDEN and held at alpha 0, and don't reveal
	// it yet. A freshly ordered child window flashes black→white before its
	// WebView paints, so it loads off-view (the frontend shows a DOM loading
	// spinner over the reserved host area) and is faded in only once the page has
	// finished loading (or a timeout fires) — see revealPageView.
	pvChild = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "Hive — page view",
		Width:            int(s.rw),
		Height:           int(s.rh),
		Frameless:        true,
		Hidden:           true,
		URL:              url,
		BackgroundColour: application.NewRGB(255, 255, 255),
		Mac: application.MacWindow{
			DisableShadow: true, // no drop shadow → no card-like border
		},
	})
	// Make it a true child of the main window (macOS addChildWindow), square the
	// corners Wails rounds on frameless windows, and hold it invisible (alpha 0)
	// so even an early Show can't flash; reach the native NSWindows via the
	// handles Wails exposes since v3 has no parent-child window API.
	child := pvChild
	s.armReveal(child)
	application.InvokeSync(func() {
		attachChildWindow(pvMain.NativeWindow(), child.NativeWindow())
		squareWindowCorners(child.NativeWindow())
		disableFocusRing(child.NativeWindow())
		setWindowAlpha(child.NativeWindow(), 0)
	})
	// Reveal on the first finished navigation. The handler reads s.revealOnce (not
	// a captured one) so it fires the current navigation's reveal even after the
	// window is reused for a later page.
	child.OnWindowEvent(events.Mac.WebViewDidFinishNavigation, func(*application.WindowEvent) {
		s.mu.Lock()
		once := s.revealOnce
		s.mu.Unlock()
		if once != nil {
			once.Do(func() { s.revealPageView(child) })
		}
	})
	return nil
}

// revealPageView positions and shows the page-view window once its page has
// finished loading, then notifies the frontend ("page-view-ready") so it can
// drop the loading spinner. Safe to call from the navigation-event callback or
// the timeout goroutine; the caller guards it with a sync.Once.
func (s *PageViewService) revealPageView(child *application.WebviewWindow) {
	s.mu.Lock()
	ok := s.open && !s.suspended && !s.minimized && pvChild == child
	if ok {
		s.applyLocked()
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	// Order the window in while still invisible (alpha 0), so its first composite
	// — the black→white flash before the WebView paints — happens off-view. Hand
	// key focus back to the main window so its toolbar (✕ / eye toggle) fires on
	// the first click, and clear the webview's focus ring now its content view exists.
	application.InvokeAsync(func() {
		setWindowAlpha(child.NativeWindow(), 0)
		child.Show()
		makeWindowKey(pvMain.NativeWindow())
		disableFocusRing(child.NativeWindow())
	})
	// Give the now-on-screen WebView a moment to paint a frame at alpha 0, then
	// fade the finished page in. The DOM spinner is held up until the fade is
	// done (page-view-ready, below), so the load is masked end-to-end — the user
	// sees spinner → page, never the flash.
	go func() {
		time.Sleep(110 * time.Millisecond)
		application.InvokeAsync(func() { fadeWindowIn(child.NativeWindow(), 0.16) })
		time.Sleep(170 * time.Millisecond)
		if app := application.Get(); app != nil {
			app.Event.Emit("page-view-ready", true)
		}
	}()
}

// SetPageViewBounds repositions/resizes the open view (reader area or window
// resized). No-op when the view is closed.
func (s *PageViewService) SetPageViewBounds(x, y, width, height float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return nil
	}
	s.rx, s.ry, s.rw, s.rh = x, y, width, height
	s.applyLocked()
	return nil
}

// ClosePageView tears the view down (left web mode, switched article, reader
// unmounted). No-op when already closed.
func (s *PageViewService) ClosePageView() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return nil
	}
	s.open = false
	s.suspended = false
	if pvChild != nil {
		pvChild.Close()
		pvChild = nil
	}
	return nil
}

// SetPageViewSuspended hides (suspended=true) or re-shows (false) the page-view
// window without ending the session. The frontend calls this when a DOM modal
// (command palette, settings, explore, …) opens/closes: the native child window
// otherwise sits above those in-webview overlays and covers them.
func (s *PageViewService) SetPageViewSuspended(suspended bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.suspended == suspended {
		return nil
	}
	s.suspended = suspended
	if !s.open || pvChild == nil {
		return nil
	}
	if suspended {
		pvChild.Hide()
	} else if !s.minimized {
		// Don't re-show while the main window is minimized / the app hidden —
		// the child would float orphaned over other apps. It comes back in
		// setParked(false) when the main window is restored.
		s.showChildLocked()
	}
	return nil
}

// showChildLocked re-shows a hidden (Hide()-parked) child with the same
// alpha-0 → order-in → fade choreography as the initial reveal: an instant
// Show() blinks twice — the WKWebView's first re-composite after Hide, then
// the pin's z-order shuffle — so both happen while the window is invisible
// and the already-painted page just fades back. Caller holds s.mu.
func (s *PageViewService) showChildLocked() {
	child := pvChild
	if child == nil || pvMain == nil {
		return
	}
	s.applyLocked()
	application.InvokeSync(func() {
		setWindowAlpha(child.NativeWindow(), 0)
		child.Show()
		pinChildAbove(pvMain.NativeWindow(), child.NativeWindow())
		makeWindowKey(pvMain.NativeWindow())
		disableFocusRing(child.NativeWindow())
	})
	// One composite pass at alpha 0 is enough (the page is already painted —
	// unlike the initial reveal there is no WebView first-paint to wait out),
	// then fade the settled window back in.
	go func() {
		time.Sleep(60 * time.Millisecond)
		application.InvokeAsync(func() { fadeWindowIn(child.NativeWindow(), 0.14) })
	}()
}

// setParked hides the page view while the main window is minimized (or the app
// hidden with ⌘H) and restores it when the window comes back. Without this,
// AppKit detaches the child on miniaturize and leaves the web page floating on
// screen with no way back to the app.
func (s *PageViewService) setParked(parked bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.minimized == parked {
		return
	}
	s.minimized = parked
	if !s.open || pvChild == nil {
		return
	}
	if parked {
		pvChild.Hide()
		return
	}
	if s.suspended {
		return // a DOM modal is still open; SetPageViewSuspended(false) will re-show
	}
	s.showChildLocked()
}

// installHooks (once) makes the floating window follow the main window: the
// frontend's bounds are content-relative and don't change when the window
// itself moves, so reposition from the Go side on move/resize.
func (s *PageViewService) installHooks() {
	s.hookOnce.Do(func() {
		if pvMain == nil {
			return
		}
		pvMain.OnWindowEvent(events.Common.WindowDidMove, func(*application.WindowEvent) { s.reposition() })
		pvMain.OnWindowEvent(events.Common.WindowDidResize, func(*application.WindowEvent) { s.reposition() })
		// A click on the main window orders it in front of its addChildWindow
		// child, which then hides behind the opaque reader area and looks gone
		// for good (later clicks land on the main window, not the hidden child).
		// Re-pin the child above the main window every time it takes focus.
		pvMain.OnWindowEvent(events.Common.WindowFocus, func(*application.WindowEvent) { s.raiseChild() })
		// Park the child while the main window is minimized: AppKit detaches
		// child windows on miniaturize, so the page view would otherwise stay
		// floating on screen — and, being a visible window, it also stops a
		// Dock click from deminiaturizing the main window.
		pvMain.OnWindowEvent(events.Common.WindowMinimise, func(*application.WindowEvent) { s.setParked(true) })
		pvMain.OnWindowEvent(events.Common.WindowUnMinimise, func(*application.WindowEvent) { s.setParked(false) })
		// Same for hiding the whole app (⌘H). Mac-only events; on other
		// platforms these ids simply never fire.
		if app := application.Get(); app != nil {
			app.Event.OnApplicationEvent(events.Mac.ApplicationDidHide, func(*application.ApplicationEvent) { s.setParked(true) })
			app.Event.OnApplicationEvent(events.Mac.ApplicationDidUnhide, func(*application.ApplicationEvent) { s.setParked(false) })
		}
	})
}

func (s *PageViewService) reposition() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open {
		s.applyLocked()
	}
}

// raiseChild re-orders the page-view above the main window after a click on the
// main window would otherwise leave it stranded behind the reader area. No-op
// while closed or suspended (a suspended view is intentionally hidden).
func (s *PageViewService) raiseChild() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open || s.suspended || s.minimized || pvChild == nil || pvMain == nil {
		return
	}
	application.InvokeSync(func() { pinChildAbove(pvMain.NativeWindow(), pvChild.NativeWindow()) })
}

// applyLocked positions the child over the reader rect, converting the
// main-window content-relative bounds to a screen position via the main
// window's own position. Caller holds s.mu.
func (s *PageViewService) applyLocked() {
	if pvChild == nil || pvMain == nil {
		return
	}
	mx, my := pvMain.RelativePosition()
	mw, mh := pvMain.Size()
	// The reader host is flush to the window's right & bottom edges, so anchor the
	// child to the window's bottom-right corner and extend up-left by the host's
	// size. This sidesteps the small offset between the window origin (mx, my) and
	// the webview content origin — positioning from the top-left left a --paper
	// seam on the top/left, while the right/bottom (clamped to the window edge)
	// stayed flush.
	w := int(math.Ceil(s.rw))
	h := int(math.Ceil(s.rh))
	if w > mw {
		w = mw
	}
	if h > mh {
		h = mh
	}
	pvChild.SetRelativePosition(mx+mw-w, my+mh-h)
	pvChild.SetSize(w, h)
}

// ── no-op native helpers (the darwin polish has no equivalent here) ──

func attachChildWindow(parent, child unsafe.Pointer)      {}
func makeWindowKey(handle unsafe.Pointer)                 {}
func squareWindowCorners(handle unsafe.Pointer)           {}
func disableFocusRing(handle unsafe.Pointer)              {}
func pinChildAbove(parent, child unsafe.Pointer)          {}
func setWindowAlpha(handle unsafe.Pointer, alpha float64) {}
func fadeWindowIn(handle unsafe.Pointer, seconds float64) {}
