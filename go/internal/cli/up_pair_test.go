package cli

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/identity"
	"github.com/srcful/terminal-relay/go/internal/pairing"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

// waitFor polls out until substr appears or the deadline passes, failing fast if
// errCh yields first (the command under test ended early).
func waitFor(t *testing.T, out *safeBuf, errCh <-chan error, substr string, deadline time.Time) {
	t.Helper()
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), substr) {
			return
		}
		if err, ended := tryEnded(errCh); ended {
			t.Fatalf("command ended before %q: %v\n%s", substr, err, out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("never saw %q in output:\n%s", substr, out.String())
}

// extractPairCode pulls the pairing code from the printed "mir pair <code>" line.
func extractPairCode(t *testing.T, out *safeBuf, errCh <-chan error, deadline time.Time) string {
	t.Helper()
	for time.Now().Before(deadline) {
		if err, ended := tryEnded(errCh); ended {
			t.Fatalf("command ended before a code: %v\n%s", err, out.String())
		}
		s := out.String()
		if i := strings.Index(s, "mir pair "); i >= 0 {
			if fields := strings.Fields(s[i+len("mir pair "):]); len(fields) > 0 {
				return fields[0]
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no pairing code in output:\n%s", out.String())
	return ""
}

// pairAsInitiator drives the client side of the pairing against the printed code.
func pairAsInitiator(t *testing.T, code string, confirm bool) {
	t.Helper()
	signalURL, token, err := pairing.DecodeCode(code)
	if err != nil {
		t.Fatalf("DecodeCode(%q): %v", code, err)
	}
	signer, err := identity.DeriveSigner(bytes.Repeat([]byte{0x44}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	mc, closeConn, err := pairing.DialPair(ctx, signalURL, pairing.RoomID(token))
	if err != nil {
		t.Fatalf("initiator DialPair: %v", err)
	}
	defer closeConn()
	started, err := pairing.StartInitiator(ctx, mc, token, signer)
	if err != nil {
		t.Fatalf("StartInitiator: %v", err)
	}
	if !confirm {
		return
	}
	if err := started.Finish(func(*pairing.AgentInfo) (string, error) { return "opaque", nil }); err != nil {
		t.Fatalf("initiator Finish: %v", err)
	}
}

// TestUpPairsInlineOnFirstRun is U1's acceptance at the CLI level: a fresh
// `mir up` prints a pairing code itself, an owner pairs against it, the owner is
// pinned, and the same process moves on to serving — no second command.
func TestUpPairsInlineOnFirstRun(t *testing.T) {
	srv := httptest.NewServer(signal.New().Handler())
	defer srv.Close()

	dir := t.TempDir()
	out := &safeBuf{}
	a := &app{binary: "mir", out: out, errOut: io.Discard}
	errCh := make(chan error, 1)
	go func() {
		// --yes: go test has no TTY to answer the safety prompt on. --shell sh +
		// --no-lan keep the serve phase free of tmux and network listeners.
		errCh <- a.cmdUp([]string{"--dir", dir, "--signal", srv.URL, "--web", "http://127.0.0.1",
			"--shell", "sh", "--no-lan", "--yes"})
	}()
	deadline := time.Now().Add(8 * time.Second)

	code := extractPairCode(t, out, errCh, deadline)
	pairAsInitiator(t, code, true)
	waitFor(t, out, errCh, "✓ paired", deadline)
	waitFor(t, out, errCh, "mir up: machine", deadline) // pairing flowed into serving

	owners, err := agent.ReloadOwners(dir)
	if err != nil || len(owners) != 1 {
		t.Fatalf("expected exactly one pinned owner, got %v (err %v)", owners, err)
	}

	// cmdUp registered its signal context before pairing, so a SIGINT now is the
	// normal shutdown path and must end the run cleanly.
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("cmdUp after SIGINT: %v\n%s", err, out.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("cmdUp did not stop on SIGINT:\n%s", out.String())
	}
}

// TestUpRefusesMissingTmuxNonInteractiveBeforePairing wires the tmux bootstrap
// (U2) through the real `cmdUp` entry point rather than testing ensure() in
// isolation: with tmux genuinely absent from PATH and no TTY (as `go test`
// runs), `mir up` must refuse — fail closed, same as before this slice — and
// must never reach the pairing step. Emptying PATH also hides any package
// manager, so the refusal falls through to the honest "none found" message;
// TestEnsureNonTTYRefusesWithExactCommand covers the exact-command branch at
// the unit level.
func TestUpRefusesMissingTmuxNonInteractiveBeforePairing(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no tmux, no brew/apt/dnf/pacman either
	out := &safeBuf{}
	a := &app{binary: "mir", out: out, errOut: io.Discard, in: strings.NewReader("")}
	err := a.cmdUp([]string{"--dir", t.TempDir(), "--signal", "http://127.0.0.1:1", "--no-lan"})
	if err == nil || !strings.Contains(err.Error(), "tmux is not installed") {
		t.Fatalf("expected the tmux refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "--shell sh") {
		t.Fatalf("refusal must still mention the --shell sh escape hatch, got: %v", err)
	}
	if strings.Contains(out.String(), "First run") || strings.Contains(out.String(), "safety number") {
		t.Fatalf("tmux bootstrap must run before pairing; pairing output leaked through:\n%s", out.String())
	}
}

// TestUpNoPairFailsClosed pins the opt-out: --no-pair on a fresh machine keeps
// today's behavior — refuse to serve with no owner instead of pairing inline.
func TestUpNoPairFailsClosed(t *testing.T) {
	out := &safeBuf{}
	a := &app{binary: "mir", out: out, errOut: io.Discard}
	err := a.cmdUp([]string{"--dir", t.TempDir(), "--signal", "http://127.0.0.1:1",
		"--shell", "sh", "--no-lan", "--no-pair"})
	if err == nil || !strings.Contains(err.Error(), "no paired owner") {
		t.Fatalf("expected the no-paired-owner refusal, got %v", err)
	}
}

// TestPairOnFirstRunRefusesNonInteractive: with no TTY and no --confirm-sas/--yes
// the safety number could never be confirmed, so first-run pairing must refuse up
// front (fail closed) rather than print a code that leads to a dead end.
func TestPairOnFirstRunRefusesNonInteractive(t *testing.T) {
	a := &app{binary: "mir", out: io.Discard, errOut: io.Discard}
	err := a.pairOnFirstRun(context.Background(), t.TempDir(), "box", "http://127.0.0.1:1", "http://127.0.0.1", sasGate{})
	if err == nil || !strings.Contains(err.Error(), "non-interactive") {
		t.Fatalf("expected the non-interactive refusal, got %v", err)
	}
}

// TestPairOnFirstRunAbortsOnRefusal: a mismatched --confirm-sas is a deliberate
// trust refusal. The retry loop exists for closed windows and transport errors —
// it must not reopen a window past a refusal, and nothing may be pinned.
func TestPairOnFirstRunAbortsOnRefusal(t *testing.T) {
	srv := httptest.NewServer(signal.New().Handler())
	defer srv.Close()

	dir := t.TempDir()
	out := &safeBuf{}
	a := &app{binary: "mir", out: out, errOut: io.Discard}
	errCh := make(chan error, 1)
	go func() {
		errCh <- a.pairOnFirstRun(context.Background(), dir, "box", srv.URL, "http://127.0.0.1",
			sasGate{confirmSAS: "0000-0000-0000-0000"})
	}()
	deadline := time.Now().Add(8 * time.Second)

	code := extractPairCode(t, out, errCh, deadline)
	pairAsInitiator(t, code, true)

	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "pairing cancelled") {
			t.Fatalf("expected pairing cancelled, got %v\n%s", err, out.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("pairOnFirstRun kept running past a refusal:\n%s", out.String())
	}
	if owners, _ := agent.ReloadOwners(dir); len(owners) != 0 {
		t.Fatalf("refused pairing must pin nothing, got %v", owners)
	}
}
