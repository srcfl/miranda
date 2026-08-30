// go/internal/agent/grouped.go
//
// Grouped per-attach tmux sessions (spec D4). The default launch used to run
// `tmux new -A -s main` for every attach: all clients shared one session and
// fought over the current window. Now each attach gets its own session in
// main's group — same windows, but an independent current window per viewer —
// and per-attach session options become possible (a future guest's read-only
// view lives there). The base session always exists and always anchors the
// group, so removing a mir-* member never destroys windows.
package agent

import (
	"crypto/rand"
	"encoding/hex"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

// defaultLaunch is the only launch grouping applies to. A custom --shell —
// plain sh, or a hand-rolled tmux command — attaches verbatim, exactly as
// before.
var defaultLaunch = []string{"tmux", "new", "-A", "-s", "main"}

func isDefaultTmuxLaunch(launch []string) bool {
	if len(launch) != len(defaultLaunch) {
		return false
	}
	for i := range launch {
		if launch[i] != defaultLaunch[i] {
			return false
		}
	}
	return true
}

// groupedNameRe matches the session names this agent mints: an owner attach's
// mir-<8 hex> and a read-write guest's guest-<8 hex> (spec G1c). The kill guard,
// startup sweep, and snapshot filter act ONLY on names of these shapes, so a
// guest session is cleaned up and hidden from other viewers exactly like a
// mir-* one, and neither can ever name the base.
var groupedNameRe = regexp.MustCompile(`^(?:mir|guest)-[0-9a-f]{8}$`)

// newAttachSessionName mints a fresh grouped-session name (mir-<8 hex>).
func newAttachSessionName() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "mir-" + hex.EncodeToString(b)
}

// guestSessionName is a read-write guest's grouped session, derived from the
// grant id so detach cleanup and the orphan sweep can find it deterministically.
func guestSessionName(gid string) string {
	return "guest-" + gid[:8]
}

var groupedBaseMu sync.Mutex

// ensureGroupedBase makes sure the base session exists BEFORE a grouped member
// is created. Order matters: `new-session -t base` with no session named base
// mints a group that has no base — and the machine owner's own later
// `tmux new -A -s base` would then land OUTSIDE the group, splitting windows.
func ensureGroupedBase(base string) error {
	groupedBaseMu.Lock()
	defer groupedBaseMu.Unlock()
	if exec.Command("tmux", "has-session", "-t", "="+base).Run() == nil {
		return nil
	}
	err := exec.Command("tmux", "new-session", "-d", "-s", base).Run()
	if err != nil && exec.Command("tmux", "has-session", "-t", "="+base).Run() == nil {
		return nil // lost a benign create race to a concurrent attach
	}
	return err
}

// groupedLaunch is the per-attach PTY command: attach a fresh session in
// base's group. -A tolerates an (unlikely) name reuse.
func groupedLaunch(base, name string) []string {
	return []string{"tmux", "new-session", "-A", "-t", base, "-s", name}
}

// killGroupedSession removes one attach's grouped session once its client is
// gone. Best-effort, and only ever a name we mint — never the base.
func killGroupedSession(name string) {
	if groupedNameRe.MatchString(name) {
		_ = exec.Command("tmux", "kill-session", "-t", "="+name).Run()
	}
}

// sweepOrphanGroupedSessions removes mir-* sessions a previous agent left
// behind (crash, SIGKILL): our name shape, no attached client, AND sharing a
// group with at least one other session. The last member of a group holds the
// group's windows, so the sweep never kills it — work survives even when the
// user has removed the base by hand.
func sweepOrphanGroupedSessions() {
	out, err := exec.Command("tmux", "list-sessions", "-F",
		"#{session_name}|#{session_attached}|#{session_group}").Output()
	if err != nil {
		return // no tmux server running — nothing to sweep
	}
	type sess struct {
		name, group string
		attached    bool
	}
	var all []sess
	members := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "|")
		if len(f) < 3 {
			continue
		}
		s := sess{name: f[0], attached: f[1] != "" && f[1] != "0", group: f[2]}
		all = append(all, s)
		if s.group != "" {
			members[s.group]++
		}
	}
	for _, s := range all {
		if groupedNameRe.MatchString(s.name) && !s.attached && s.group != "" && members[s.group] > 1 {
			_ = exec.Command("tmux", "kill-session", "-t", "="+s.name).Run()
			members[s.group]--
		}
	}
}
