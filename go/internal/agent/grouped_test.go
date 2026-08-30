// go/internal/agent/grouped_test.go — grouped per-attach tmux sessions (D4).
package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestIsDefaultTmuxLaunch(t *testing.T) {
	cases := []struct {
		launch []string
		want   bool
	}{
		{[]string{"tmux", "new", "-A", "-s", "main"}, true},
		{[]string{"sh"}, false},
		{[]string{"tmux", "new", "-A", "-s", "work"}, false},
		{[]string{"tmux", "new-session", "-A", "-s", "main"}, false},
		{[]string{"tmux", "new", "-A", "-s", "main", "extra"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isDefaultTmuxLaunch(c.launch); got != c.want {
			t.Errorf("isDefaultTmuxLaunch(%v) = %v, want %v", c.launch, got, c.want)
		}
	}
}

func TestNewAttachSessionNameShape(t *testing.T) {
	a, b := newAttachSessionName(), newAttachSessionName()
	if !groupedNameRe.MatchString(a) || !groupedNameRe.MatchString(b) {
		t.Fatalf("minted names %q, %q do not match %v", a, b, groupedNameRe)
	}
	if a == b {
		t.Fatalf("two minted names collided: %q", a)
	}
}

// TestCollapseGrouped is pure: agent-minted mir-* sessions fold out of the
// snapshot and the viewer's own current window lands on the base entry.
func TestCollapseGrouped(t *testing.T) {
	in := strings.Join([]string{
		"main|@0|0|zsh|1|0|0|zsh|1",
		"main|@1|1|vim|0|0|0|vim|1",
		"mir-aabbccdd|@0|0|zsh|0|0|0|zsh|1",
		"mir-aabbccdd|@1|1|vim|1|0|0|vim|1",
		"mir-11223344|@0|0|zsh|1|0|0|zsh|1",
		"mir-11223344|@1|1|vim|0|0|0|vim|1",
		"scratch|@5|0|logs|1|0|0|tail|1",
		"mir-notgrouped|@6|0|keep|1|0|0|zsh|1", // user session; name misses the shape
	}, "\n")

	snap := parseSessionsSnap(in, "mir-aabbccdd")
	collapseGrouped(snap, "mir-aabbccdd", "main")

	var names []string
	for _, s := range snap.Sess {
		names = append(names, s.N)
	}
	if got := strings.Join(names, ","); got != "main,scratch,mir-notgrouped" {
		t.Fatalf("collapsed sessions = %s, want main,scratch,mir-notgrouped", got)
	}
	base := snap.Sess[0]
	if !base.Act {
		t.Error("the viewer's grouped session did not mark the base as viewed")
	}
	if base.AW != "@1" {
		t.Errorf("base AW = %q, want the viewer's @1 (main's own current was @0)", base.AW)
	}

	// A viewer without a grouped session (resolution failed): mir-* still fold
	// away, and the base keeps its own flags untouched.
	snap = parseSessionsSnap(in, "")
	collapseGrouped(snap, "", "main")
	if len(snap.Sess) != 3 || snap.Sess[0].Act || snap.Sess[0].AW != "@0" {
		b, _ := json.Marshal(snap)
		t.Fatalf("collapse without an active grouped session mangled the base: %s", b)
	}
}

// TestEnsureGroupedBaseAndLifecycle drives the real helpers against a private
// tmux server: the base is created once, grouped members share its windows,
// and killing a member never touches the base's windows.
func TestEnsureGroupedBaseAndLifecycle(t *testing.T) {
	tmux := tmuxTestServer(t, "anchor")

	if err := ensureGroupedBase("gmain"); err != nil {
		t.Fatalf("ensureGroupedBase: %v", err)
	}
	if err := ensureGroupedBase("gmain"); err != nil {
		t.Fatalf("ensureGroupedBase (second call): %v", err)
	}
	if out, err := tmux("new-window", "-d", "-t", "=gmain:", "-n", "two"); err != nil {
		t.Fatalf("new-window: %v (%s)", err, out)
	}

	// Two grouped members (created detached — the production path attaches the
	// same session shape through a PTY).
	for _, name := range []string{"mir-0000aaaa", "mir-0000bbbb"} {
		if out, err := tmux("new-session", "-d", "-t", "gmain", "-s", name); err != nil {
			t.Fatalf("grouped member %s: %v (%s)", name, err, out)
		}
	}
	out, _ := tmux("list-sessions", "-F", "#{session_name}|#{session_group}")
	for _, want := range []string{"gmain|gmain", "mir-0000aaaa|gmain", "mir-0000bbbb|gmain"} {
		if !strings.Contains(out, want) {
			t.Fatalf("group membership missing %q in:\n%s", want, out)
		}
	}

	// Independent current windows.
	if out, err := tmux("select-window", "-t", "=mir-0000aaaa:^"); err != nil {
		t.Fatalf("select first: %v (%s)", err, out)
	}
	if out, err := tmux("select-window", "-t", "=mir-0000bbbb:$"); err != nil {
		t.Fatalf("select last: %v (%s)", err, out)
	}
	actives, _ := tmux("list-windows", "-a", "-F", "#{session_name}|#{window_id}|#{window_active}")
	var aAW, bAW string
	for _, line := range strings.Split(actives, "\n") {
		f := strings.Split(line, "|")
		if len(f) == 3 && f[2] == "1" {
			switch f[0] {
			case "mir-0000aaaa":
				aAW = f[1]
			case "mir-0000bbbb":
				bAW = f[1]
			}
		}
	}
	if aAW == "" || bAW == "" || aAW == bAW {
		t.Fatalf("grouped members do not hold independent current windows: a=%q b=%q\n%s", aAW, bAW, actives)
	}

	// Kill one member: base windows survive, the other member survives.
	killGroupedSession("mir-0000aaaa")
	if _, err := tmux("has-session", "-t", "=mir-0000aaaa"); err == nil {
		t.Fatal("killGroupedSession left the member alive")
	}
	wins, _ := tmux("list-windows", "-t", "=gmain:", "-F", "#{window_name}")
	if !strings.Contains(wins, "two") {
		t.Fatalf("killing a grouped member lost the base's windows:\n%s", wins)
	}
	if _, err := tmux("has-session", "-t", "=mir-0000bbbb"); err != nil {
		t.Fatal("killing one member killed another")
	}

	// killGroupedSession refuses names outside our shape — the base included.
	killGroupedSession("gmain")
	if _, err := tmux("has-session", "-t", "=gmain"); err != nil {
		t.Fatal("killGroupedSession touched a non-mir session name")
	}
}

// TestSweepOrphanGroupedSessions: unattached mir-* members of a group are
// swept; the base survives; a lone mir-* holding the last copy of its group's
// windows is never killed.
func TestSweepOrphanGroupedSessions(t *testing.T) {
	tmux := tmuxTestServer(t, "anchor")

	if err := ensureGroupedBase("gmain"); err != nil {
		t.Fatalf("ensureGroupedBase: %v", err)
	}
	for _, name := range []string{"mir-1111aaaa", "mir-1111bbbb"} {
		if out, err := tmux("new-session", "-d", "-t", "gmain", "-s", name); err != nil {
			t.Fatalf("grouped member: %v (%s)", err, out)
		}
	}
	// A lone grouped session whose base is gone: the last holder of its
	// group's windows.
	if out, err := tmux("new-session", "-d", "-t", "lonely", "-s", "mir-2222cccc"); err != nil {
		t.Fatalf("lone grouped session: %v (%s)", err, out)
	}

	sweepOrphanGroupedSessions()

	ls, _ := tmux("list-sessions", "-F", "#{session_name}")
	for _, gone := range []string{"mir-1111aaaa", "mir-1111bbbb"} {
		if strings.Contains(ls, gone) {
			t.Errorf("sweep left orphan %s alive:\n%s", gone, ls)
		}
	}
	for _, kept := range []string{"gmain", "anchor", "mir-2222cccc"} {
		if !strings.Contains(ls, kept) {
			t.Errorf("sweep killed %s, which must survive:\n%s", kept, ls)
		}
	}
}

// TestGroupedAttachIndependentViews is the end-to-end acceptance: two real PTY
// attaches through the production launch get independent current windows via
// the allow-listed control path, and each viewer's snapshot highlights its OWN
// current window on the collapsed base entry.
func TestGroupedAttachIndependentViews(t *testing.T) {
	tmux := tmuxTestServer(t, "anchor")

	if err := ensureGroupedBase("gmain"); err != nil {
		t.Fatalf("ensureGroupedBase: %v", err)
	}
	if out, err := tmux("new-window", "-d", "-t", "=gmain:", "-n", "two"); err != nil {
		t.Fatalf("new-window: %v (%s)", err, out)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type viewer struct {
		name string
		pty  *PTY
		pid  int
	}
	var viewers []viewer
	for _, name := range []string{"mir-3333aaaa", "mir-3333bbbb"} {
		pty, err := StartPTY(ctx, groupedLaunch("gmain", name))
		if err != nil {
			t.Fatalf("StartPTY(%s): %v", name, err)
		}
		defer pty.Close()
		go func() { // drain the PTY so tmux can draw
			buf := make([]byte, 4096)
			for {
				if _, err := pty.Read(buf); err != nil {
					return
				}
			}
		}()
		viewers = append(viewers, viewer{name: name, pty: pty, pid: pty.Pid()})
	}
	for _, v := range viewers {
		attached := false
		for i := 0; i < 150 && !attached; i++ {
			if _, sess := tmuxClient(v.pid); sess == v.name {
				attached = true
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !attached {
			t.Skip("a tmux client never attached here")
		}
	}

	// Window ids of the base's two windows, in index order.
	wout, _ := tmux("list-windows", "-t", "=gmain:", "-F", "#{window_id}")
	wins := strings.Fields(wout)
	if len(wins) < 2 {
		t.Fatalf("expected 2 base windows, got %v", wins)
	}

	// Each viewer selects a different window through the allow-listed control
	// path (select-window is session-qualified now — a bare @N would be
	// ambiguous across the group).
	sel := func(pid int, winID string) {
		payload, _ := json.Marshal(map[string]string{"a": "select-window", "t": winID})
		runTmuxControl(pid, payload)
	}
	sel(viewers[0].pid, wins[0])
	sel(viewers[1].pid, wins[1])

	deadline := time.Now().Add(3 * time.Second)
	for {
		_, s0 := tmuxClient(viewers[0].pid)
		_, s1 := tmuxClient(viewers[1].pid)
		a0, a1 := sessionActiveWindow(t, tmux, s0), sessionActiveWindow(t, tmux, s1)
		if a0 == wins[0] && a1 == wins[1] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("independent selects did not land: viewer0=%s viewer1=%s want %s,%s", a0, a1, wins[0], wins[1])
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Each viewer's collapsed snapshot: no mir-* sessions, base viewed, base AW
	// = that viewer's own current window.
	for i, v := range viewers {
		b := tmuxSessionsJSON(v.pid, "gmain")
		if b == nil {
			t.Fatalf("no snapshot for viewer %d", i)
		}
		var snap sessSnapshot
		if err := json.Unmarshal(b, &snap); err != nil {
			t.Fatal(err)
		}
		for _, s := range snap.Sess {
			if groupedNameRe.MatchString(s.N) {
				t.Fatalf("viewer %d snapshot leaks grouped session %s", i, s.N)
			}
			if s.N == "gmain" {
				if !s.Act {
					t.Errorf("viewer %d: base not marked viewed", i)
				}
				if s.AW != wins[i] {
					t.Errorf("viewer %d: base AW = %s, want own current %s", i, s.AW, wins[i])
				}
			}
		}
	}
}

func sessionActiveWindow(t *testing.T, tmux func(...string) (string, error), session string) string {
	t.Helper()
	if session == "" {
		return ""
	}
	out, _ := tmux("list-windows", "-t", "="+session+":", "-F", "#{window_id}|#{window_active}")
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Split(line, "|"); len(f) == 2 && f[1] == "1" {
			return f[0]
		}
	}
	return ""
}
