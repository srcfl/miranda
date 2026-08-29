// go/internal/agent/tmuxhooks.go
package agent

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// Instant window strip. Every tmux hook that can change the snapshot is set to
// run the in-server command `wait-for -S <channel>`; the agent parks a
// `tmux wait-for <channel>` process on that channel and rebuilds the snapshot
// the moment it returns. Event to push is a few milliseconds plus the coalescing
// window, against up to a second for the poll.
//
// Why hooks and not a control-mode client (`tmux -C`): a control client is a
// real client. It joins `list-clients` (which is how we resolve OUR client),
// it takes part in window sizing, and it streams every pane's output to us —
// the plaintext we otherwise never touch. A hook costs one parked `tmux
// wait-for` process that is attached to no session, so nothing the user can
// see about their tmux changes.
//
// The hook body is a tmux command, not a shell command: `wait-for -S` runs
// inside the server, forks nothing, and injects no keystrokes.
//
// `wait-for` remembers a signal raised while nobody is waiting, so a burst that
// lands between two waits still wakes us exactly once — the coalescing the
// window pusher wants, for free.
const (
	// hookChannel is the tmux wait-for channel our hooks signal. It is fixed
	// (not per-session) so concurrent attaches share one set of hooks; a signal
	// wakes every waiter.
	hookChannel = "mir-window-strip"

	// hookSlot is the array index our command takes in each hook. tmux hooks are
	// arrays: the user's own entries start at 0, so a high fixed slot leaves
	// them alone, makes install idempotent, and lets us remove exactly ours.
	hookSlot = "[999]"

	// hookRearm bounds one `tmux wait-for`, so a wait orphaned by a killed agent
	// cannot hold an otherwise empty tmux server open for long.
	hookRearm = time.Minute

	// hookRetry is the pause before another install attempt (the tmux server may
	// still be starting) or another wait after tmux refused one.
	hookRetry = 500 * time.Millisecond

	// hookInstallTries bounds install attempts before we settle for the poll.
	hookInstallTries = 20

	// hookFloor is the minimum gap between two waits, so a storm of tmux events
	// cannot become a storm of forks. It matches the pusher's coalescing window:
	// a change that lands inside it is already in the snapshot that window
	// rebuilds. Nothing is lost either way — tmux remembers a signal raised while
	// we are not waiting.
	hookFloor = windowDebounce
)

// hookEvents are the tmux hooks that can change the window/session snapshot:
// windows and sessions appearing, going, or being renamed; the viewed session
// or active window changing; alert flags; pane count and the active pane's
// command. Names tmux does not know are dropped at install time.
var hookEvents = []string{
	"window-linked", "window-unlinked", "window-renamed",
	"session-created", "session-closed", "session-renamed",
	"session-window-changed", "client-session-changed",
	"alert-activity", "alert-bell", "alert-silence",
	"pane-exited", "pane-died", "window-pane-changed",
	"after-select-window", "after-new-window", "after-split-window",
	"after-kill-pane", "after-rename-window", "after-rename-session",
	"after-new-session", "after-select-pane",
}

// Hooks are server-global, so they are installed once per agent process and
// reference-counted across concurrent attach sessions.
var (
	hookMu    sync.Mutex
	hookRefs  int
	hookNames []string
)

// tmuxHookNotify installs the snapshot hooks and then sends to out once per
// tmux event burst until ctx ends, removing the hooks it added on the way out.
// It returns immediately when hooks are unavailable (no tmux server, a tmux too
// old for indexed hook slots, a locked-down server) — the caller's poll is the
// safety net either way.
func tmuxHookNotify(ctx context.Context, out chan<- struct{}) {
	if !acquireTmuxHooks(ctx) {
		return
	}
	defer releaseTmuxHooks()
	for ctx.Err() == nil {
		switch waitTmuxSignal(ctx) {
		case waitSignaled:
			select {
			case out <- struct{}{}:
			default: // a rebuild is already pending; this burst folds into it
			}
			sleepCtx(ctx, hookFloor)
		case waitRearm:
			// our own deadline: nothing happened, park again
		case waitFailed:
			sleepCtx(ctx, hookRetry)
		}
	}
}

// acquireTmuxHooks makes sure the hooks are installed and takes a reference.
// The tmux server may still be starting (the PTY has only just run `tmux new`),
// so it retries for a few seconds before giving up on hooks for this session.
func acquireTmuxHooks(ctx context.Context) bool {
	for try := 0; try < hookInstallTries && ctx.Err() == nil; try++ {
		hookMu.Lock()
		if hookRefs > 0 {
			hookRefs++
			hookMu.Unlock()
			return true
		}
		if names := installTmuxHooks(); len(names) > 0 {
			hookNames, hookRefs = names, 1
			hookMu.Unlock()
			return true
		}
		hookMu.Unlock()
		if !sleepCtx(ctx, hookRetry) {
			return false
		}
	}
	return false
}

// releaseTmuxHooks drops a reference and, on the last one, removes our slot from
// every hook we set. Best effort: a hook left behind by a killed agent only
// signals a channel nobody is waiting on.
func releaseTmuxHooks() {
	hookMu.Lock()
	defer hookMu.Unlock()
	if hookRefs == 0 {
		return
	}
	hookRefs--
	if hookRefs > 0 {
		return
	}
	removeTmuxHooks(hookNames)
	hookNames = nil
}

// installTmuxHooks points every snapshot-relevant hook at our wait-for channel
// and returns the hooks it set. One batched tmux call is the fast path; tmux
// aborts a command sequence at the first error, so a tmux that does not know one
// of these hook names falls back to setting them one at a time.
func installTmuxHooks() []string {
	body := "wait-for -S " + hookChannel
	// Probe with one hook first: it proves the server is up and that this tmux
	// understands indexed hook slots, so a dead server costs one fork, not 22.
	if err := exec.Command("tmux", "set-hook", "-g", hookEvents[0]+hookSlot, body).Run(); err != nil {
		return nil
	}
	rest := hookEvents[1:]
	if err := exec.Command("tmux", hookArgs("set-hook", "-g", rest, body)...).Run(); err == nil {
		return hookEvents
	}
	ok := []string{hookEvents[0]}
	for _, h := range rest {
		if exec.Command("tmux", "set-hook", "-g", h+hookSlot, body).Run() == nil {
			ok = append(ok, h)
		}
	}
	return ok
}

// removeTmuxHooks unsets our slot in each named hook, leaving the user's own
// entries in place.
func removeTmuxHooks(names []string) {
	if len(names) == 0 {
		return
	}
	if exec.Command("tmux", hookArgs("set-hook", "-gu", names, "")...).Run() == nil {
		return
	}
	for _, h := range names {
		_ = exec.Command("tmux", "set-hook", "-gu", h+hookSlot).Run()
	}
}

// hookArgs builds one tmux argument list running cmd against every hook, the
// sub-commands separated by tmux's lone ";" argument. An empty body means the
// command takes no value (the unset case).
func hookArgs(cmd, flags string, names []string, body string) []string {
	args := make([]string, 0, len(names)*5)
	for i, h := range names {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, cmd, flags, h+hookSlot)
		if body != "" {
			args = append(args, body)
		}
	}
	return args
}

type waitOutcome int

const (
	waitSignaled waitOutcome = iota // a hook fired
	waitRearm                       // our own re-arm deadline passed
	waitFailed                      // tmux refused the wait
)

// waitTmuxSignal parks on the hook channel until a hook signals it, our re-arm
// deadline passes, or ctx ends.
func waitTmuxSignal(ctx context.Context) waitOutcome {
	wctx, cancel := context.WithTimeout(ctx, hookRearm)
	defer cancel()
	err := exec.CommandContext(wctx, "tmux", "wait-for", hookChannel).Run()
	switch {
	case err == nil:
		return waitSignaled
	case ctx.Err() == nil && wctx.Err() != nil:
		return waitRearm
	default:
		return waitFailed
	}
}

// sleepCtx sleeps for d, reporting false if ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
