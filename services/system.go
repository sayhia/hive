package services

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"hive/internal/apperr"
	"hive/internal/appstate"
)

// SystemService exposes app-level commands not tied to a single domain (deep
// links, tray refresh, image fetching).
type SystemService struct {
	app *appstate.State
}

// TakePendingDeepLink drains a hive://subscribe URL delivered before the webview
// could receive the deep-link event (a cold-start launch). Returns null when
// none is buffered.
func (s *SystemService) TakePendingDeepLink() (*string, error) {
	url := s.app.TakePendingDeepLink()
	if url == "" {
		return nil, nil
	}
	return &url, nil
}

// trayRefresh rebuilds the native menu-bar tray. main.go owns the tray and the
// menu builder, so it registers this callback via SetTrayRefresh; RefreshTray
// invokes it. The frontend calls RefreshTray right after persisting a new
// "language" setting, so the tray's native labels re-localize without a restart.
var trayRefresh func()

// SetTrayRefresh registers the tray-rebuild callback. Called once from main.go.
func SetTrayRefresh(fn func()) { trayRefresh = fn }

// mainWin is the app's main window; main.go registers it via SetMainWindow so
// SetWindowBackground can repaint the native backing at runtime.
var mainWin *application.WebviewWindow

// SetMainWindow registers the main window handle. Called once from main.go.
func SetMainWindow(w *application.WebviewWindow) { mainWin = w }

// SetWindowBackground paints the native window backing the given colour. The
// frontend mirrors the current theme's paper colour here on every appearance
// change, so the strip a fast macOS resize exposes beyond the webview's last
// frame matches the theme instead of flashing the (formerly hardcoded) light
// paper in dark mode.
func (s *SystemService) SetWindowBackground(r, g, b uint8) {
	if mainWin != nil {
		mainWin.SetBackgroundColour(application.NewRGB(r, g, b))
	}
}

// RefreshTray rebuilds the menu-bar tray, re-reading the UI language so its
// labels match the current locale. No-op until the tray is wired in main.go.
func (s *SystemService) RefreshTray() error {
	if trayRefresh != nil {
		trayRefresh()
	}
	return nil
}

// ToggleFullscreen flips the current window between fullscreen and normal
// state. Real system-level fullscreen: on macOS the traffic-light bar and
// menu bar hide via native NSWindow; on Windows/Linux the frame collapses
// via Wails' fullscreen implementation. Returns the resulting state so the
// UI can sync (a mac user pressing the green traffic-light directly still
// fires this same state, so we treat the API as authoritative).
func (s *SystemService) ToggleFullscreen() (bool, error) {
	w := application.Get().Window.Current()
	if w == nil {
		return false, nil
	}
	if w.IsFullscreen() {
		w.UnFullscreen()
		return false, nil
	}
	w.Fullscreen()
	return true, nil
}

// IsFullscreen reports the current window's fullscreen state so the UI
// can seed itself on mount (or after a native-side toggle).
func (s *SystemService) IsFullscreen() (bool, error) {
	w := application.Get().Window.Current()
	if w == nil {
		return false, nil
	}
	return w.IsFullscreen(), nil
}

// imageUA is a mainstream browser User-Agent. Some image hosts gate on it in
// addition to the Referer; sending a browser UA mirrors what the webview does.
const imageUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"

// maxImageBytes is an upper bound on a saved image, well above any real image.
const maxImageBytes = 25 * 1024 * 1024

// FetchImage fetches a feed image's bytes — as the retry path for images the
// webview itself failed to load. It walks the Referer candidates (none → image
// origin → article URL) until a host serves the image, covering both
// blacklist- and whitelist-style hotlink protection. Transport errors abort the
// chain; only HTTP status errors advance it.
func (s *SystemService) FetchImage(rawURL string, pageURL *string) ([]byte, error) {
	if !(strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://")) {
		return nil, apperr.Code("badImageUrl")
	}
	return fetchImageBytes(s.app.HTTP(), rawURL, pageURL)
}

// fetchImageBytes walks the Referer candidates for `rawURL` and returns the
// first successful response body. See FetchImage for the hotlink-protection
// rationale.
func fetchImageBytes(client *http.Client, rawURL string, pageURL *string) ([]byte, error) {
	var lastErr error = apperr.Code("badImageUrl")
	for _, referer := range refererCandidates(rawURL, pageURL) {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, apperr.Wrap("network", err)
		}
		req.Header.Set("User-Agent", imageUA)
		if referer != nil {
			req.Header.Set("Referer", *referer)
		}
		resp, err := client.Do(req)
		if err != nil {
			// A transport error can't be fixed by a different Referer — abort.
			return nil, apperr.Wrap("network", err)
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			lastErr = apperr.Codef("network", "HTTP %d", resp.StatusCode)
			continue
		}
		if resp.ContentLength > maxImageBytes {
			resp.Body.Close()
			return nil, apperr.Code("imageTooLarge")
		}
		buf, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
		resp.Body.Close()
		if err != nil {
			return nil, apperr.Wrap("network", err)
		}
		if int64(len(buf)) > maxImageBytes {
			return nil, apperr.Code("imageTooLarge")
		}
		return buf, nil
	}
	return nil, lastErr
}

// SaveImage is the reader's "Save image" action. Fetches the bytes through the
// same hotlink-protection walk as FetchImage, then hands them to a native save
// dialog and writes the chosen file — all in Go, so a large image never
// round-trips the JS bridge as a base64 blob.
//
// Returns false when the user cancels the dialog; the frontend shows a success
// toast only when a file was actually written.
func (s *SystemService) SaveImage(rawURL string, pageURL *string) (bool, error) {
	// Fetch first: a failed or unsupported URL errors out without ever showing
	// a dangling save dialog.
	bytes, err := s.FetchImage(rawURL, pageURL)
	if err != nil {
		return false, err
	}
	path, err := application.Get().Dialog.SaveFile().
		SetFilename(imageFilename(rawURL)).
		AddFilter("Image", "*.png;*.jpg;*.jpeg;*.gif;*.webp;*.svg;*.avif;*.bmp;*.ico").
		CanCreateDirectories(true).
		PromptForSingleSelection()
	if err != nil {
		return false, apperr.Wrap("imageSave", err)
	}
	if path == "" {
		return false, nil // user cancelled — not an error
	}
	if err := os.WriteFile(path, bytes, 0o644); err != nil {
		return false, apperr.Wrap("imageSave", err)
	}
	return true, nil
}

// imageFilename is a sensible default save filename for an image URL: its last
// path segment (query and fragment stripped), falling back to "image".
func imageFilename(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "image"
	}
	segs := strings.Split(u.Path, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(segs[i]); s != "" {
			return s
		}
	}
	return "image"
}

// refererCandidates returns the Referer values to try, in order: none, the
// image's own origin, then the article URL.
func refererCandidates(imageURL string, pageURL *string) []*string {
	out := []*string{nil}
	if u, err := url.Parse(imageURL); err == nil && u.Scheme != "" && u.Host != "" {
		origin := u.Scheme + "://" + u.Host + "/"
		out = append(out, &origin)
	}
	if pageURL != nil && (strings.HasPrefix(*pageURL, "http://") || strings.HasPrefix(*pageURL, "https://")) {
		if _, err := url.Parse(*pageURL); err == nil {
			p := *pageURL
			dup := false
			for _, c := range out {
				if c != nil && *c == p {
					dup = true
					break
				}
			}
			if !dup {
				out = append(out, &p)
			}
		}
	}
	return out
}
