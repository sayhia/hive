//go:build darwin

package services

import "C"

// hivePageViewDidFinish receives the WKNavigationDelegate's navigation-end
// callbacks from pageview_darwin.go. Lives in its own file because cgo forbids
// //export in a file whose preamble contains definitions (the ObjC delegate).
//
// The delegate calls this on the main thread, so the body defers to a
// goroutine: OpenPageView/SetPageViewBounds hold s.mu while waiting on the
// main thread (InvokeSync), and blocking here on s.mu would deadlock.
//
//export hivePageViewDidFinish
func hivePageViewDidFinish() {
	s := pvSvc
	if s == nil {
		return
	}
	go func() {
		s.mu.Lock()
		once := s.revealOnce
		s.mu.Unlock()
		if once != nil {
			once.Do(s.revealPageView)
		}
	}()
}
