// go/internal/agent/windows.go
package agent

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var winIDRe = regexp.MustCompile(`^@[0-9]{1,9}$`)

// runTmuxControl runs a client-requested tmux command directly (no keystroke
// injection, no prefix/Enter fragility). exec with explicit args (no shell); the
// action is allow-listed, window targets are validated @ids, session targets are
// validated, and new names are sanitized — so an authenticated client can manage
// windows AND sessions safely. clientPid is the agent's PTY child PID, used to
// find OUR tmux client (see tmuxClient) so cross-session switches target the
// right client.
func runTmuxControl(clientPid int, payload []byte) {
	var c struct {
		A string `json:"a"` // action
		S string `json:"s"` // session (switch target / which session a window action belongs to)
		T string `json:"t"` // target: window_id (@N) for window actions, session name for session actions
		N string `json:"n"` // new name (rename / new-session)
	}
	if json.Unmarshal(payload, &c) != nil {
		return
	}
	idOK := winIDRe.MatchString(c.T)
	switch c.A {
	case "select-window":
		if !idOK {
			return
		}
		tty, cur := tmuxClient(clientPid)
		if tty != "" && c.S != "" && c.S != cur && validSessTarget(c.S) {
			// Window lives in a session we're not viewing: move our client there,
			// then select the window. window_id (@N) is server-global, so the
			// select needs no session qualifier and can't hit the wrong window.
			_ = exec.Command("tmux", "switch-client", "-c", tty, "-t", "="+c.S).Run()
		}
		_ = exec.Command("tmux", "select-window", "-t", c.T).Run()
	case "kill-window":
		if idOK {
			_ = exec.Command("tmux", "kill-window", "-t", c.T).Run()
		}
	case "rename-window":
		if idOK {
			_ = exec.Command("tmux", "rename-window", "-t", c.T, safeName(c.N)).Run()
		}
	case "new-window":
		tty, cur := tmuxClient(clientPid)
		target := c.S
		if target == "" {
			target = cur
		}
		if !validSessTarget(target) {
			return
		}
		// Create in the target session and learn where it landed; if that session
		// isn't the one we're viewing, jump our client to the new window. The
		// trailing ":" makes "=name:" a session target (a bare "=name" would
		// exact-match a *window* named name, since new-window's -t is a window).
		out, err := exec.Command("tmux", "new-window", "-t", "="+target+":", "-P", "-F", "#{session_name}").Output()
		if err == nil && tty != "" {
			// new-window selects the new window in its session; if that session
			// isn't the one we're viewing, jump our client there to land on it.
			if created := strings.TrimSpace(string(out)); created != cur && validSessTarget(created) {
				_ = exec.Command("tmux", "switch-client", "-c", tty, "-t", "="+created).Run()
			}
		}
	case "next-window":
		if _, cur := tmuxClient(clientPid); validSessTarget(cur) {
			_ = exec.Command("tmux", "next-window", "-t", "="+cur).Run()
		}
	case "previous-window":
		if _, cur := tmuxClient(clientPid); validSessTarget(cur) {
			_ = exec.Command("tmux", "previous-window", "-t", "="+cur).Run()
		}
	case "switch-session":
		if tty, cur := tmuxClient(clientPid); tty != "" && c.T != cur && validSessTarget(c.T) {
			_ = exec.Command("tmux", "switch-client", "-c", tty, "-t", "="+c.T).Run()
		}
	case "new-session":
		// Detached so we don't fight the existing client; -P -F learns the name
		// (tmux auto-numbers when none is given), then switch our client to it.
		name := safeName(c.N)
		args := []string{"new-session", "-d", "-P", "-F", "#{session_name}"}
		if name != "" {
			args = append(args, "-s", name)
		}
		if out, err := exec.Command("tmux", args...).Output(); err == nil {
			created := strings.TrimSpace(string(out))
			if tty, _ := tmuxClient(clientPid); tty != "" && validSessTarget(created) {
				_ = exec.Command("tmux", "switch-client", "-c", tty, "-t", "="+created).Run()
			}
		}
	case "rename-session":
		if n := safeName(c.N); n != "" && validSessTarget(c.T) {
			_ = exec.Command("tmux", "rename-session", "-t", "="+c.T, n).Run()
		}
	case "kill-session":
		// Guard: never kill the session our client is viewing — that detaches the
		// client and tears down the whole attach. (The UI also hides this action
		// for the active session; this is defense in depth.)
		if _, cur := tmuxClient(clientPid); validSessTarget(c.T) && c.T != cur {
			_ = exec.Command("tmux", "kill-session", "-t", "="+c.T).Run()
		}
	}
}

// safeName sanitizes a user-supplied window/session name to a small alphanumeric
// set (plus space/-/_/.), capped at 32 — safe to pass as a tmux argument.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ' ' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return -1
	}, s)
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}

// validSessTarget reports whether s is safe to use as a tmux -t session target.
// We exec with explicit args (no shell), so the only hazards are tmux's own
// target metacharacters; ':' separates session:window, so reject it. The caller
// wraps the value with '=' for exact (non-fnmatch) matching, so glob chars in an
// existing session name are matched literally.
func validSessTarget(s string) bool {
	if s == "" || len(s) > 64 || strings.ContainsRune(s, ':') {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// tmuxClient resolves OUR attached client (the one this agent's PTY drives) by
// matching the PTY child PID against tmux's client_pid, returning the client tty
// (the target for `switch-client -c`) and the session it is currently viewing.
// If the PID isn't among the clients yet (a brief startup race) but exactly one
// client exists, use it. Returns ("","") when it can't decide.
func tmuxClient(clientPid int) (tty, session string) {
	out, err := exec.Command("tmux", "list-clients", "-F", "#{client_pid}|#{client_tty}|#{client_session}").Output()
	if err != nil {
		return "", ""
	}
	var onlyTTY, onlySess string
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "|")
		if len(f) < 3 {
			continue
		}
		n++
		onlyTTY, onlySess = f[1], f[2]
		if pid, _ := strconv.Atoi(f[0]); pid == clientPid {
			return f[1], f[2]
		}
	}
	if n == 1 {
		return onlyTTY, onlySess
	}
	return "", ""
}

// winInfo mirrors one tmux window for the client overview (short keys = small frame).
type winInfo struct {
	ID  string `json:"id"`  // tmux window_id (@7) — stable target for select/rename/kill
	I   int    `json:"i"`   // window_index (display label only; renumbers)
	N   string `json:"n"`   // window_name
	Cmd string `json:"cmd"` // pane_current_command (cheap "preview")
	P   int    `json:"p"`   // pane count
	A   bool   `json:"a"`   // activity flag
	B   bool   `json:"b"`   // bell flag
}

// sessInfo is one tmux session and its windows for the multi-session overview.
type sessInfo struct {
	N   string    `json:"n"`   // session name (target for switch/rename/kill)
	Act bool      `json:"act"` // our client is currently viewing this session
	AW  string    `json:"aw"`  // active window_id within this session
	Win []winInfo `json:"win"`
}

// sessSnapshot (v2) is the whole-server view: every session and its windows, with
// the session our client is viewing flagged. v1 (single session, flat windows) is
// retired; the web client falls back to a single-session render if it ever sees it.
type sessSnapshot struct {
	V    int        `json:"v"`
	Sess []sessInfo `json:"sess"`
}

// tmuxSessionsJSON returns a v2 JSON snapshot of every tmux session and its
// windows, marking the session our client (PTY child = clientPid) is viewing.
// Pure read — runs no mutation. nil if tmux fails or there are no windows.
func tmuxSessionsJSON(clientPid int) []byte {
	out, err := exec.Command("tmux", "list-windows", "-a", "-F",
		"#{session_name}|#{window_id}|#{window_index}|#{window_name}|#{window_active}|#{window_activity_flag}|#{window_bell_flag}|#{pane_current_command}|#{window_panes}").Output()
	if err != nil {
		return nil
	}
	_, active := tmuxClient(clientPid)
	return parseSessions(string(out), active)
}

// parseSessions builds the v2 snapshot from `tmux list-windows -a` output (one
// window per line, session_name first) and the name of the session our client is
// viewing. Sessions and windows keep tmux's output order so the change-detection
// compare in the session poller stays stable. nil when there are no windows.
func parseSessions(listAllWindows, activeSession string) []byte {
	snap := sessSnapshot{V: 2}
	pos := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(listAllWindows), "\n") {
		f := strings.Split(line, "|")
		if len(f) < 9 {
			continue
		}
		sname := f[0]
		idx, _ := strconv.Atoi(f[2])
		panes, _ := strconv.Atoi(f[8])
		w := winInfo{ID: f[1], I: idx, N: f[3], Cmd: f[7], P: panes, A: f[5] == "1", B: f[6] == "1"}
		si, ok := pos[sname]
		if !ok {
			si = len(snap.Sess)
			pos[sname] = si
			snap.Sess = append(snap.Sess, sessInfo{N: sname, Act: sname == activeSession})
		}
		snap.Sess[si].Win = append(snap.Sess[si].Win, w)
		if f[4] == "1" {
			snap.Sess[si].AW = f[1]
		}
	}
	if len(snap.Sess) == 0 {
		return nil
	}
	b, _ := json.Marshal(snap)
	return b
}

// sessionFromLaunch extracts the tmux session name from a launch command like
// {"tmux","new","-A","-s","main"}; "" if not a tmux launch with -s. Used only to
// decide whether to enable the window/session overview for this launch.
func sessionFromLaunch(launch []string) string {
	if len(launch) == 0 || launch[0] != "tmux" {
		return ""
	}
	for i, a := range launch {
		if a == "-s" && i+1 < len(launch) {
			return launch[i+1]
		}
	}
	return ""
}
