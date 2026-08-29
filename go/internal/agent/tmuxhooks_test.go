// go/internal/agent/tmuxhooks_test.go
package agent

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// tmuxTestServer starts a private tmux server for one test and returns a runner
// for it. TMUX_TMPDIR moves the socket into a temp dir and an empty TMUX stops
// tmux from talking to the server of a developer running `go test` inside tmux,
// so the test drives the same plain `tmux` calls the agent makes without ever
// touching a real session.
func tmuxTestServer(t *testing.T, session string) func(args ...string) (string, error) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping live tmux test under -short")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir, err := os.MkdirTemp("", "mirtmux") // short: the socket path has a length limit
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")

	run := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if out, err := run("new-session", "-d", "-s", session, "sleep 60"); err != nil {
		t.Skipf("cannot start a tmux server here: %v (%s)", err, out)
	}
	t.Cleanup(func() {
		_, _ = run("kill-server")
		_ = os.RemoveAll(dir)
	})
	return run
}

// TestTmuxHooksDeliverWindowChangeFast is the acceptance test for the slice: with
// the poll an hour away, a new tmux window still reaches the push path — so the
// tmux hooks, not the poll, carried it, and they carried it in well under the
// second the poll would have cost.
func TestTmuxHooksDeliverWindowChangeFast(t *testing.T) {
	tmux := tmuxTestServer(t, "striptest")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Install synchronously so the test knows the hooks are live before it acts;
	// the notify loop below takes a second reference and installs nothing.
	if !acquireTmuxHooks(ctx) {
		t.Skip("this tmux refuses indexed hook slots")
	}
	defer releaseTmuxHooks()

	trigger := make(chan struct{}, 1)
	notifyDone := make(chan struct{})
	go func() {
		defer close(notifyDone)
		tmuxHookNotify(ctx, trigger)
	}()

	r := newPushRecorder()
	pushDone := make(chan struct{})
	go func() {
		defer close(pushDone)
		pushWindowSnapshots(ctx, func() []byte { return tmuxSessionsJSON(0) }, r.push, trigger, time.Hour, windowDebounce)
	}()

	r.waitPushes(t, 1, 5*time.Second) // the attach-time snapshot
	r.drain()

	start := time.Now()
	if out, err := tmux("new-window", "-d", "-t", "striptest"); err != nil {
		t.Fatalf("new-window: %v (%s)", err, out)
	}
	select {
	case <-r.got:
	case <-time.After(5 * time.Second):
		t.Fatal("no snapshot pushed after the new window (hooks did not fire)")
	}
	latency := time.Since(start)
	t.Logf("tmux event to pushed snapshot: %v", latency)
	if latency > 500*time.Millisecond {
		t.Errorf("event to push took %v, want well under the 1s poll", latency)
	}

	r.mu.Lock()
	last := string(r.pushed[len(r.pushed)-1])
	r.mu.Unlock()
	if strings.Count(last, `"id":`) != 2 {
		t.Errorf("pushed snapshot should hold both windows, got %s", last)
	}

	cancel()
	<-notifyDone
	<-pushDone
}

// TestTmuxHooksWithAttachedClient runs the production shape: a PTY holding a
// real `tmux new -A` client, the hook notifier, and the snapshot builder keyed
// to that client. It proves the parked `tmux wait-for` is invisible to
// list-clients (which is how the agent finds OUR client) and that a window
// created elsewhere reaches the push path at once.
func TestTmuxHooksWithAttachedClient(t *testing.T) {
	tmux := tmuxTestServer(t, "live")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pty, err := StartPTY(ctx, []string{"tmux", "new", "-A", "-s", "live"})
	if err != nil {
		t.Skipf("cannot start a tmux PTY here: %v", err)
	}
	defer pty.Close()
	_ = pty.Resize(80, 24)
	go func() { // drain, or tmux blocks once the PTY buffer fills
		buf := make([]byte, 4096)
		for {
			if _, err := pty.Read(buf); err != nil {
				return
			}
		}
	}()

	pid := pty.Pid()
	var tty string
	for i := 0; i < 100 && tty == ""; i++ {
		tty, _ = tmuxClient(pid)
		time.Sleep(20 * time.Millisecond)
	}
	if tty == "" {
		t.Skip("the tmux client never attached here")
	}

	if !acquireTmuxHooks(ctx) {
		t.Skip("this tmux refuses indexed hook slots")
	}
	defer releaseTmuxHooks()
	trigger := make(chan struct{}, 1)
	notifyDone := make(chan struct{})
	go func() {
		defer close(notifyDone)
		tmuxHookNotify(ctx, trigger)
	}()
	// No goroutine outlives the test: the hook reference must be back before the
	// next test starts its own tmux server.
	pushDone := make(chan struct{})
	defer func() { cancel(); <-notifyDone; <-pushDone }()

	r := newPushRecorder()
	go func() {
		defer close(pushDone)
		pushWindowSnapshots(ctx, func() []byte { return tmuxSessionsJSON(pid) }, r.push, trigger, time.Hour, windowDebounce)
	}()
	r.waitPushes(t, 1, 5*time.Second)
	r.drain()

	start := time.Now()
	if out, err := tmux("new-window", "-d", "-t", "live"); err != nil {
		t.Fatalf("new-window: %v (%s)", err, out)
	}
	select {
	case <-r.got:
	case <-time.After(5 * time.Second):
		t.Fatal("no snapshot pushed after the new window")
	}
	t.Logf("tmux event to pushed snapshot (attached client): %v", time.Since(start))

	// Our parked wait-for must not look like a client, or the agent would stop
	// resolving which session the user is viewing.
	if got, sess := tmuxClient(pid); got != tty || sess != "live" {
		t.Errorf("tmuxClient = (%q, %q) while a wait-for is parked, want (%q, %q)", got, sess, tty, "live")
	}
	r.mu.Lock()
	last := string(r.pushed[len(r.pushed)-1])
	r.mu.Unlock()
	if !strings.Contains(last, `"act":true`) {
		t.Errorf("snapshot lost the viewed-session flag: %s", last)
	}
}

// TestTmuxHooksLeaveUserHooksAlone: we append into a high array slot, so a hook
// the user already set on the same event keeps running while ours is installed
// and survives our cleanup.
func TestTmuxHooksLeaveUserHooksAlone(t *testing.T) {
	tmux := tmuxTestServer(t, "hooktest")

	const userHook = "display-message mir-user-hook"
	if out, err := tmux("set-hook", "-ag", "window-linked", userHook); err != nil {
		t.Fatalf("set user hook: %v (%s)", err, out)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !acquireTmuxHooks(ctx) {
		t.Skip("this tmux refuses indexed hook slots")
	}

	hooks, _ := tmux("show-hooks", "-g")
	if !strings.Contains(hooks, userHook) {
		t.Errorf("installing our hooks dropped the user's:\n%s", hooks)
	}
	if !strings.Contains(hooks, hookSlot+" wait-for -S "+hookChannel) {
		t.Errorf("our hook is not in the global hook table:\n%s", hooks)
	}

	releaseTmuxHooks()

	hooks, _ = tmux("show-hooks", "-g")
	if strings.Contains(hooks, hookChannel) {
		t.Errorf("our hooks outlived the session:\n%s", hooks)
	}
	if !strings.Contains(hooks, userHook) {
		t.Errorf("cleanup ate the user's hook:\n%s", hooks)
	}
	if hookRefs != 0 {
		t.Errorf("hookRefs = %d after release, want 0", hookRefs)
	}
}

// TestTmuxHookNotifyGivesUpWithoutTmux: no reachable tmux server means no hooks
// and no busy loop — the caller's poll is left to do the work.
func TestTmuxHookNotifyGivesUpWithoutTmux(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live tmux test under -short")
	}
	dir, err := os.MkdirTemp("", "mirtmux")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("TMUX_TMPDIR", dir) // empty: no server on this socket
	t.Setenv("TMUX", "")
	t.Setenv("PATH", dir) // and no tmux binary either

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		tmuxHookNotify(ctx, make(chan struct{}, 1))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tmuxHookNotify did not give up when tmux was unavailable")
	}
	if hookRefs != 0 {
		t.Errorf("hookRefs = %d, want 0 (nothing was installed)", hookRefs)
	}
}
