//go:build nowails

// Package events is the no-op twin of events.go, selected by `-tags nowails`.
//
// The headless hive-cli links ingestion, which emits "feeds-updated" through
// this package. The real implementation imports Wails v3 whose Linux backend
// needs cgo (GTK/WebKit) — compiling it with CGO_ENABLED=0 fails on
// `undefined: pointer` — so the CLI build sets `nowails` to swap in these
// stubs and keep a dependency-closed, cgo-free binary. Runtime semantics are
// unchanged: without a running Wails app the real Emit never reaches a
// listener either.
package events

import "sync/atomic"

// Emit discards the event: there is no frontend to hear it.
func Emit(name string, data ...any) {}

// Sequencer stamps sequence numbers exactly like the real one, but nothing is
// published. Keeping the type available (instead of stubbing it away) lets
// callers compile unchanged under both tags.
type Sequencer struct {
	name string
	seq  atomic.Uint64
}

// NewSequencer returns a Sequencer that would publish on the given event name.
func NewSequencer(name string) *Sequencer {
	return &Sequencer{name: name}
}

// Emit drops the stamped payload.
func (s *Sequencer) Emit(data any) {}

// Seq mirrors the envelope shape so callers can still construct it.
type Seq struct {
	N    uint64 `json:"seq"`
	Data any    `json:"data"`
}
