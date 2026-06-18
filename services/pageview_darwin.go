// In-app original-page view — macOS implementation.
//
// The page renders in a WKWebView embedded directly into the main window's
// content view, as a sibling above the app's own webview. Riding inside the
// window makes the whole child-window problem class structurally impossible:
// it moves/minimizes/hides/restores with the window (no parking hooks), never
// needs re-pinning on focus (no z-order shuffle → no focus flicker), and never
// steals key-window status (toolbar clicks fire first time). What remains is
// the same IPC contract as the fallback — open / set-bounds / close / suspend
// plus the "page-view-ready" reveal event — and the alpha-0 → load → fade
// choreography that masks the WebView's first-composite flash.
//
// The one behavior the window version didn't have: in-webview DOM overlays
// (command palette, settings…) still can't paint over a native subview, so the
// frontend keeps suspending the view around modals via SetPageViewSuspended.

//go:build darwin

package services

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <stdlib.h>

// Implemented in pageview_darwin_export.go (cgo //export). Called on the main
// thread whenever a page-view navigation ends, success or failure.
extern void hivePageViewDidFinish(void);

@interface HivePVNavDelegate : NSObject <WKNavigationDelegate>
@end
@implementation HivePVNavDelegate
- (void)webView:(WKWebView *)wv didFinishNavigation:(WKNavigation *)nav {
	hivePageViewDidFinish();
}
// Failures also end the navigation: reveal the error page promptly instead of
// leaving the spinner up until the Go-side 8s timeout.
- (void)webView:(WKWebView *)wv didFailNavigation:(WKNavigation *)nav withError:(NSError *)err {
	hivePageViewDidFinish();
}
- (void)webView:(WKWebView *)wv didFailProvisionalNavigation:(WKNavigation *)nav withError:(NSError *)err {
	hivePageViewDidFinish();
}
@end

static WKWebView *hivePV = nil;
static HivePVNavDelegate *hivePVDelegate = nil;

// Clear the AppKit focus ring on the whole view tree: with the webview first
// responder, AppKit otherwise draws a ring around it on click-in.
static void hivePVKillFocusRing(NSView *v) {
	if (v == nil) { return; }
	[v setFocusRingType:NSFocusRingTypeNone];
	for (NSView *sub in [v subviews]) { hivePVKillFocusRing(sub); }
}

// Create the WKWebView on first use and (re-)attach it to the main window's
// content view. Held at alpha 0 until the reveal fade. Main thread only.
static void hivePVEnsure(void *mainWindow) {
	if (mainWindow == NULL) { return; }
	NSView *cv = [(__bridge NSWindow *)mainWindow contentView];
	if (cv == nil) { return; }
	if (hivePV == nil) {
		WKWebViewConfiguration *cfg = [[WKWebViewConfiguration alloc] init];
		hivePV = [[WKWebView alloc] initWithFrame:NSMakeRect(0, 0, 100, 100)
		                            configuration:cfg];
		hivePVDelegate = [[HivePVNavDelegate alloc] init];
		hivePV.navigationDelegate = hivePVDelegate;
		hivePV.alphaValue = 0.0;
		// The reader host is flush to the window's right & bottom edges, so pin
		// the view there through live window resizes (flexible left/top margins;
		// AppKit y grows upward, so "bottom" = min-Y). The frontend then follows
		// up with SetPageViewBounds when the host's size actually changes.
		hivePV.autoresizingMask = NSViewMinXMargin | NSViewMaxYMargin;
		hivePVKillFocusRing(hivePV);
	}
	if ([hivePV superview] != cv) { [cv addSubview:hivePV]; }
}

// Size the view and anchor it to the content view's bottom-right corner —
// same anchoring as the window fallback: right/bottom flush beats a top-left
// seam from the small window-origin vs content-origin offset. Main thread only.
static void hivePVSetFrame(void *mainWindow, double w, double h) {
	if (mainWindow == NULL || hivePV == nil) { return; }
	NSView *cv = [(__bridge NSWindow *)mainWindow contentView];
	if (cv == nil) { return; }
	NSRect b = [cv bounds];
	double fw = MIN(ceil(w), b.size.width);
	double fh = MIN(ceil(h), b.size.height);
	[hivePV setFrame:NSMakeRect(b.size.width - fw, 0, fw, fh)];
}

static void hivePVLoad(const char *url) {
	if (hivePV == nil || url == NULL) { return; }
	NSURL *u = [NSURL URLWithString:[NSString stringWithUTF8String:url]];
	if (u == nil) { return; }
	[hivePV loadRequest:[NSURLRequest requestWithURL:u]];
}

static void hivePVSetAlpha(double a) {
	if (hivePV == nil) { return; }
	hivePV.alphaValue = a;
}

// Animate alpha → 1. The view loads at alpha 0 and only fades up once the page
// has painted, so the black/white first-composite happens while invisible and
// the user sees the finished page fade in, never the flash.
static void hivePVFadeIn(double dur) {
	if (hivePV == nil) { return; }
	hivePVKillFocusRing(hivePV);
	[NSAnimationContext runAnimationGroup:^(NSAnimationContext *ctx) {
		ctx.duration = dur;
		[[hivePV animator] setAlphaValue:1.0];
	}];
}

// Suspend toggle for DOM modals. A hidden layer keeps its contents, so
// un-hiding doesn't re-composite from scratch the way a window Show() did.
static void hivePVSetHidden(int hidden) {
	if (hivePV == nil) { return; }
	hivePV.hidden = hidden ? YES : NO;
}

static void hivePVClose(void) {
	if (hivePV == nil) { return; }
	[hivePV stopLoading];
	hivePV.navigationDelegate = nil;
	[hivePV removeFromSuperview];
	hivePV = nil;        // ARC releases
	hivePVDelegate = nil;
}
*/
import "C"

import (
	"sync"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// pvMain is the main window, injected from main.go after it is created. Kept as
// a package-level value set by a plain function (not a service method) so the
// non-serialisable *WebviewWindow never reaches the TS binding generator.
var pvMain *application.WebviewWindow

// pvSvc lets the exported navigation callback (pageview_darwin_export.go) reach
// the service instance. Set on first OpenPageView; there is only ever one.
var pvSvc *PageViewService

// SetPageView wires the main window for PageViewService. Called once from main.go.
func SetPageView(main *application.WebviewWindow) { pvMain = main }

// PageViewService is the bound IPC surface for the in-app original-page view.
type PageViewService struct {
	mu         sync.Mutex
	open       bool
	suspended  bool       // temporarily hidden for an open DOM modal, still "open"
	rw, rh     float64    // last reader-rect size (logical px; anchored bottom-right)
	revealOnce *sync.Once // guards the current navigation's one-shot reveal
}

// armReveal sets up a fresh one-shot reveal for the navigation now starting,
// plus an 8s timeout fallback for a page that hangs and never fires any
// navigation-end delegate callback. Caller holds s.mu.
func (s *PageViewService) armReveal() {
	once := &sync.Once{}
	s.revealOnce = once
	go func() {
		time.Sleep(8 * time.Second)
		once.Do(s.revealPageView)
	}()
}

// OpenPageView shows the original page at url over the reader rectangle
// (x, y, width, height — logical px relative to the main window content; the
// view anchors to the bottom-right so only the size is used). The WKWebView is
// created on first use and reused (navigate + resize) so switching articles
// doesn't flash a teardown/rebuild: it drops to alpha 0 (hiding the stale
// page), navigates, and fades back in once the new page has loaded.
func (s *PageViewService) OpenPageView(url string, x, y, width, height float64) error {
	if application.Get() == nil || pvMain == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pvSvc = s
	s.rw, s.rh = width, height
	s.open = true
	s.suspended = false
	s.armReveal()
	cu := C.CString(url)
	defer C.free(unsafe.Pointer(cu))
	application.InvokeSync(func() {
		C.hivePVEnsure(pvMain.NativeWindow())
		C.hivePVSetAlpha(0)
		C.hivePVSetHidden(0)
		C.hivePVSetFrame(pvMain.NativeWindow(), C.double(s.rw), C.double(s.rh))
		C.hivePVLoad(cu)
	})
	return nil
}

// revealPageView fades the loaded page in and notifies the frontend
// ("page-view-ready") so it can drop the loading spinner. Called from the
// navigation-end callback or the timeout goroutine; the caller guards it with
// a sync.Once.
func (s *PageViewService) revealPageView() {
	s.mu.Lock()
	ok := s.open && !s.suspended
	s.mu.Unlock()
	if !ok {
		return
	}
	// Give the WebView a beat to composite the finished page at alpha 0, then
	// fade it in. The DOM spinner is held up until the fade is done, so the
	// load is masked end-to-end — the user sees spinner → page, never the flash.
	go func() {
		time.Sleep(110 * time.Millisecond)
		application.InvokeAsync(func() { C.hivePVFadeIn(0.16) })
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
	s.rw, s.rh = width, height
	if pvMain == nil {
		return nil
	}
	application.InvokeSync(func() {
		C.hivePVSetFrame(pvMain.NativeWindow(), C.double(s.rw), C.double(s.rh))
	})
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
	application.InvokeSync(func() { C.hivePVClose() })
	return nil
}

// SetPageViewSuspended hides (suspended=true) or re-shows (false) the page view
// without ending the session. The frontend calls this when a DOM modal
// (command palette, settings, explore, …) opens/closes: the native subview
// otherwise sits above those in-webview overlays and covers them.
func (s *PageViewService) SetPageViewSuspended(suspended bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.suspended == suspended {
		return nil
	}
	s.suspended = suspended
	if !s.open {
		return nil
	}
	h := C.int(0)
	if suspended {
		h = 1
	}
	application.InvokeSync(func() { C.hivePVSetHidden(h) })
	return nil
}
