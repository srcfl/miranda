// go/internal/agent/windowpush.go
package agent

import (
	"context"
	"time"
)

const (
	// windowPoll is the safety net: it catches what no tmux hook reports (a
	// pane's foreground command changing under `automatic-rename off`) and it is
	// the whole mechanism when hooks are unavailable.
	windowPoll = time.Second

	// windowDebounce is how long a rebuild waits after the first trigger of a
	// burst, so one user action that fires five hooks costs one snapshot. It is
	// a ceiling, not a sliding window: a steady stream of events still pushes
	// every windowDebounce rather than starving.
	windowDebounce = 50 * time.Millisecond
)

// pushWindowSnapshots pushes a window/session snapshot whenever it changes:
// straight after a tmux event (coalesced over debounce) and on the poll tick.
// snapshot is the single source of truth for the frame; this only decides when
// to ask it and whether the answer is new. It returns when ctx ends.
func pushWindowSnapshots(ctx context.Context, snapshot func() []byte, push func([]byte) error, trigger <-chan struct{}, poll, debounce time.Duration) {
	var last string
	emit := func() {
		if b := snapshot(); b != nil && string(b) != last {
			last = string(b)
			_ = push(b)
		}
	}
	emit()

	tick := time.NewTicker(poll)
	defer tick.Stop()

	var pending *time.Timer
	var pendingC <-chan time.Time
	defer func() {
		if pending != nil {
			pending.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			emit()
		case _, ok := <-trigger:
			if !ok {
				trigger = nil // a closed source must not spin the loop
				continue
			}
			if pending == nil {
				pending = time.NewTimer(debounce)
				pendingC = pending.C
			}
			// Already armed: the rest of the burst folds into that rebuild.
		case <-pendingC:
			pending, pendingC = nil, nil
			emit()
		}
	}
}
