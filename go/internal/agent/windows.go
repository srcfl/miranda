// go/internal/agent/windows.go
package agent

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
)

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

type winSnapshot struct {
	V      int       `json:"v"`
	Active string    `json:"active"` // window_id of the active window
	Win    []winInfo `json:"win"`
}

// tmuxWindowsJSON returns a JSON snapshot of the session's windows, or nil if
// tmux/list-windows fails (or there are none). Pure read — runs no mutation.
func tmuxWindowsJSON(session string) []byte {
	out, err := exec.Command("tmux", "list-windows", "-t", session, "-F",
		"#{window_id}|#{window_index}|#{window_name}|#{window_active}|#{window_activity_flag}|#{window_bell_flag}|#{pane_current_command}|#{window_panes}").Output()
	if err != nil {
		return nil
	}
	snap := winSnapshot{V: 1}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "|")
		if len(f) < 8 {
			continue
		}
		idx, _ := strconv.Atoi(f[1])
		panes, _ := strconv.Atoi(f[7])
		snap.Win = append(snap.Win, winInfo{ID: f[0], I: idx, N: f[2], Cmd: f[6], P: panes, A: f[4] == "1", B: f[5] == "1"})
		if f[3] == "1" {
			snap.Active = f[0]
		}
	}
	if len(snap.Win) == 0 {
		return nil
	}
	b, _ := json.Marshal(snap)
	return b
}

// sessionFromLaunch extracts the tmux session name from a launch command like
// {"tmux","new","-A","-s","main"}; "" if not a tmux launch with -s.
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
