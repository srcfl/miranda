// go/internal/agent/session_test.go
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

func TestSessionBridgeRunsRealShellOverNoise(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	agentPriv, agentPub, _ := noise.GenerateStatic()
	browserPriv, browserPub, _ := noise.GenerateStatic()

	browserMC, agentMC := peer.Pipe()

	// Establish Noise KK over the pipe (browser=initiator, agent=responder).
	var browserSess *noise.Session
	done := make(chan error, 1)
	go func() {
		s, err := peer.RunResponder(ctx, agentMC, agentPriv, browserPub)
		if err != nil {
			done <- err
			return
		}
		p, err := StartPTY(ctx, []string{"sh"})
		if err != nil {
			done <- err
			return
		}
		defer p.Close()
		done <- RunAgentSession(ctx, agentMC, s, p, "test-machine")
	}()
	bs, err := peer.RunInitiator(ctx, browserMC, browserPriv, agentPub)
	if err != nil {
		t.Fatal(err)
	}
	browserSess = bs

	// Browser receives HELLO first.
	hello := recvFrame(t, ctx, browserMC, browserSess)
	htype, hpayload, _ := noise.DecodeFrame(hello)
	if htype != noise.FrameHello {
		t.Fatalf("expected HELLO, got type %d", htype)
	}
	var meta map[string]string
	_ = json.Unmarshal(hpayload, &meta)
	if meta["name"] != "test-machine" {
		t.Fatalf("HELLO name = %q", meta["name"])
	}

	// Browser sends a command; expects to see it echoed by the real shell.
	sendData(t, ctx, browserMC, browserSess, []byte("echo bridge_works\n"))

	deadline := time.Now().Add(8 * time.Second)
	var acc bytes.Buffer
	for time.Now().Before(deadline) {
		frame := recvFrame(t, ctx, browserMC, browserSess)
		typ, payload, err := noise.DecodeFrame(frame)
		if err != nil || typ != noise.FrameData {
			continue
		}
		acc.Write(payload)
		if bytes.Contains(acc.Bytes(), []byte("bridge_works")) {
			return // success
		}
	}
	t.Fatalf("never saw command output; got:\n%s", acc.String())
}

// exitingShell is a Shell whose Read returns an error immediately (simulating
// the shell/PTY dying first, e.g. the user typing `exit`). Write/Resize are
// no-ops.
type exitingShell struct{}

func (exitingShell) Read([]byte) (int, error)    { return 0, errors.New("shell exited") }
func (exitingShell) Write(b []byte) (int, error) { return len(b), nil }
func (exitingShell) Resize(uint16, uint16) error { return nil }
func (exitingShell) Close() error                { return nil }

// countSessionGoroutines counts goroutines parked inside RunAgentSession's
// peer->shell loop (mc.Recv). On the leak this stays >0 after the function
// returns.
func countSessionGoroutines() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	stacks := string(buf[:n])
	count := 0
	for _, g := range strings.Split(stacks, "\n\n") {
		if strings.Contains(g, "agent.RunAgentSession") {
			count++
		}
	}
	return count
}

// TestSessionBridgeNoGoroutineLeakWhenShellExitsFirst proves that when the shell
// ends first, RunAgentSession returns AND the peer->shell goroutine (blocked in
// mc.Recv) is unblocked rather than leaking for the process lifetime.
func TestSessionBridgeNoGoroutineLeakWhenShellExitsFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	agentPriv, agentPub, _ := noise.GenerateStatic()
	browserPriv, browserPub, _ := noise.GenerateStatic()
	browserMC, agentMC := peer.Pipe()

	done := make(chan error, 1)
	go func() {
		s, err := peer.RunResponder(ctx, agentMC, agentPriv, browserPub)
		if err != nil {
			done <- err
			return
		}
		// Shell errors immediately => shell->peer goroutine returns first.
		done <- RunAgentSession(ctx, agentMC, s, exitingShell{}, "test-machine")
	}()

	if _, err := peer.RunInitiator(ctx, browserMC, browserPriv, agentPub); err != nil {
		t.Fatal(err)
	}

	// RunAgentSession must return (it does today via the shell->peer error)...
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("RunAgentSession did not return after shell exited")
	}

	// ...AND the peer->shell goroutine must be gone. Poll briefly to avoid
	// flakiness from scheduler timing after cancel().
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if countSessionGoroutines() == 0 {
			return // no leak
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("peer->shell goroutine leaked: %d still parked in RunAgentSession after it returned", countSessionGoroutines())
}

func sendData(t *testing.T, ctx context.Context, mc peer.MsgConn, sess *noise.Session, data []byte) {
	t.Helper()
	ct, err := sess.Encrypt(noise.EncodeData(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := mc.Send(ct); err != nil {
		t.Fatal(err)
	}
}

func recvFrame(t *testing.T, ctx context.Context, mc peer.MsgConn, sess *noise.Session) []byte {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ct, err := mc.Recv(rctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	pt, err := sess.Decrypt(ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	return pt
}
