// go/internal/agent/guest_serve_test.go — the guest serve paths against a real
// tmux server: read-only is a pane mirror that shows output and drops input;
// read-write gets a grouped session whose keystrokes land; expiry and revocation
// each tear a live guest down. Skipped under -short or without tmux.
package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// guestTmux starts a private tmux server with session `main` running scopeCmd,
// and returns a runner for direct tmux calls. Same isolation as the hook tests.
func guestTmux(t *testing.T, scopeCmd string) func(args ...string) (string, error) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping live tmux test under -short")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir, err := os.MkdirTemp("", "mirguest")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	run := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	args := []string{"new-session", "-d", "-s", "main"}
	if scopeCmd != "" {
		args = append(args, scopeCmd)
	}
	if out, err := run(args...); err != nil {
		t.Skipf("cannot start a tmux server here: %v (%s)", err, out)
	}
	t.Cleanup(func() { _, _ = run("kill-server") })
	return run
}

// guestClient is the guest end of a Noise session driving rt.serveGuest.
type guestClient struct {
	mc   peer.MsgConn
	sess *noise.Session
}

// startGuestServe wires a Noise-KK pipe, runs rt.serveGuest(grant) on the agent
// side, and returns the guest client plus a channel with serveGuest's return.
func startGuestServe(t *testing.T, ctx context.Context, rt *Runtime, grant *identity.SignedGrant) (*guestClient, <-chan error) {
	t.Helper()
	agentPriv, agentPub, _ := noise.GenerateStatic()
	guestPriv, guestPub, _ := noise.GenerateStatic()
	clientMC, agentMC := peer.Pipe()

	done := make(chan error, 1)
	go func() {
		s, err := peer.RunResponder(ctx, agentMC, agentPriv, guestPub)
		if err != nil {
			done <- err
			return
		}
		done <- rt.serveGuest(ctx, agentMC, s, grant)
	}()
	cs, err := peer.RunInitiator(ctx, clientMC, guestPriv, agentPub)
	if err != nil {
		t.Fatalf("guest KK: %v", err)
	}
	return &guestClient{mc: clientMC, sess: cs}, done
}

func (g *guestClient) send(t *testing.T, ctx context.Context, framed []byte) {
	t.Helper()
	ct, err := g.sess.Encrypt(framed)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.mc.Send(ct); err != nil {
		t.Fatal(err)
	}
}

// readUntil accumulates decoded DATA frames until want appears or the deadline
// passes, returning everything seen (so a test can also assert an absence).
func (g *guestClient) readUntil(t *testing.T, want string, within time.Duration) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	var buf strings.Builder
	for {
		ct, err := g.mc.Recv(ctx)
		if err != nil {
			return buf.String(), false
		}
		pt, err := g.sess.Decrypt(ct)
		if err != nil {
			return buf.String(), false
		}
		if typ, payload, derr := noise.DecodeFrame(pt); derr == nil && typ == noise.FrameData {
			buf.Write(payload)
			if strings.Contains(buf.String(), want) {
				return buf.String(), true
			}
		}
	}
}

func guestTestRuntime(t *testing.T, owner *identity.Signer) *Runtime {
	t.Helper()
	dir := t.TempDir()
	cfg, err := LoadOrInit(dir, "box", "https://relay.example")
	if err != nil {
		t.Fatal(err)
	}
	cfg.PairedOwners = []string{owner.Address}
	return NewRuntime(cfg, defaultLaunch, nil)
}

// TestGuestROMirrorsOutputAndDropsInput is the spec's differential: a read-only
// guest sees the pane's real output but its own keystrokes never reach the pane.
// The scope runs a shell, which echoes whatever reaches its tty — so anything
// the guest could inject would come back, and anything sent with send-keys does.
func TestGuestROMirrorsOutputAndDropsInput(t *testing.T) {
	run := guestTmux(t, "") // default shell in the pane
	owner, guest := offerSigner(t, 0x11), offerSigner(t, 0x22)
	rt := guestTestRuntime(t, owner)
	sg, err := identity.MintGrant(owner, rt.cfg.MachineID, guest.Address, "main", "ro", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gc, done := startGuestServe(t, ctx, rt, sg)

	// The guest tries to inject; a ro guest's input must be dropped, so the
	// shell never echoes or runs it.
	gc.send(t, ctx, noise.EncodeData([]byte("echo INJECTED\n")))
	time.Sleep(300 * time.Millisecond)
	// Real pane input, echoed and run by the shell.
	if out, err := run("send-keys", "-t", "main", "echo REALDATA", "Enter"); err != nil {
		t.Fatalf("send-keys: %v (%s)", err, out)
	}

	seen, ok := gc.readUntil(t, "REALDATA", 6*time.Second)
	if !ok {
		t.Fatalf("guest never saw the real pane output:\n%q", seen)
	}
	if strings.Contains(seen, "INJECTED") {
		t.Fatalf("read-only guest input reached the pane:\n%q", seen)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("serveGuest did not return after cancel")
	}
}

// TestGuestRWKeystrokesLand: a read-write guest gets a grouped session on the
// shared window, so what it types runs in the scope's shell.
func TestGuestRWKeystrokesLand(t *testing.T) {
	run := guestTmux(t, "") // default shell in the pane
	owner, guest := offerSigner(t, 0x11), offerSigner(t, 0x22)
	rt := guestTestRuntime(t, owner)
	sg, err := identity.MintGrant(owner, rt.cfg.MachineID, guest.Address, "main", "rw", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gc, done := startGuestServe(t, ctx, rt, sg)

	// A real client always drains the terminal; a grouped tmux client redraws
	// enough to fill the pipe, so keep reading in the background until ctx is
	// cancelled or the agent's send blocks (a test artifact, not a product
	// concern).
	go func() {
		for {
			if _, err := gc.mc.Recv(ctx); err != nil {
				return
			}
		}
	}()

	marker := filepath.Join(t.TempDir(), "rw_landed")
	time.Sleep(500 * time.Millisecond) // let the grouped client attach
	gc.send(t, ctx, noise.EncodeData([]byte("touch "+marker+"\n")))

	landed := false
	for i := 0; i < 60; i++ {
		if _, err := os.Stat(marker); err == nil {
			landed = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !landed {
		t.Fatal("read-write guest keystrokes never reached the shell")
	}
	// The grouped guest session is hidden from a snapshot and cleaned up on end.
	if out, _ := run("list-sessions", "-F", "#{session_name}"); !strings.Contains(out, "guest-"+sg.GID[:8]) {
		t.Fatalf("expected a guest-%s session while serving, saw:\n%s", sg.GID[:8], out)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("serveGuest did not return after cancel")
	}
	// killGroupedSession ran on detach.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if out, _ := run("list-sessions", "-F", "#{session_name}"); !strings.Contains(out, "guest-"+sg.GID[:8]) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("guest grouped session outlived the attach")
}

// TestGuestExpiryDropsLiveSession: a live guest is torn down within ~1s of na.
func TestGuestExpiryDropsLiveSession(t *testing.T) {
	guestTmux(t, "")
	owner, guest := offerSigner(t, 0x11), offerSigner(t, 0x22)
	rt := guestTestRuntime(t, owner)
	now := time.Now()
	sg, err := owner.SignGrant(identity.Grant{
		V: 1, Owner: owner.Address, Machine: rt.cfg.MachineID, Guest: guest.Address,
		Scope: "main", Mode: "ro", NB: now.Add(-identity.GrantSkew).Unix(), NA: now.Add(time.Second).Unix(),
		GID: "eeee1111eeee2222",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gc, done := startGuestServe(t, ctx, rt, sg)

	seen, _ := gc.readUntil(t, "this share has ended", 4*time.Second)
	if !strings.Contains(seen, "this share has ended") {
		t.Fatalf("guest was not told the share ended:\n%q", seen)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("serveGuest did not return at expiry")
	}
}

// TestGuestRevokeDropsAndBars: revoke-grant tombstones the gid, drops the live
// session at once, and a later attach with that grant refuses.
func TestGuestRevokeDropsAndBars(t *testing.T) {
	guestTmux(t, "")
	owner, guest := offerSigner(t, 0x11), offerSigner(t, 0x22)
	rt := guestTestRuntime(t, owner)
	sg, err := identity.MintGrant(owner, rt.cfg.MachineID, guest.Address, "main", "ro", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := AddGrant(rt.cfg.Dir, sg); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gc, done := startGuestServe(t, ctx, rt, sg)

	// Wait until the session has registered, then revoke it from the owner side.
	if _, ok := gc.readUntil(t, "read-only", 3*time.Second); !ok {
		// the HELLO name carries "(shared, read-only)"; if missed, proceed anyway
	}
	revoke := mustJSON(map[string]string{"a": "revoke-grant", "gid": sg.GID})
	handled, ack := rt.revokeGrantHandler(owner.Address)(revoke)
	if !handled || ack["ack"] != "revoke-grant:"+sg.GID {
		t.Fatalf("revoke handler = (%v, %v)", handled, ack)
	}

	seen, _ := gc.readUntil(t, "ended by the owner", 4*time.Second)
	if !strings.Contains(seen, "ended by the owner") {
		t.Fatalf("revoked guest not told:\n%q", seen)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("serveGuest did not return after revoke")
	}
	// A fresh attach with the same (now tombstoned) grant must refuse.
	if g := findValidGuestGrant(rt.cfg.Dir, owner.Address, rt.cfg.MachineID, guest.Address, time.Now()); g != nil {
		t.Fatal("revoked grant still authorizes an attach")
	}
}
