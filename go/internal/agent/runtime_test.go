// go/internal/agent/runtime_test.go
package agent

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

// TestRuntimeReclaimsAttachOnDisconnect proves that when a browser handshakes,
// runs a shell, then disconnects (closes its PeerConnection) while the agent's
// top-level ctx is still alive, the agent reclaims the per-attach
// PeerConnection, PTY/shell process, and goroutines — rather than leaking them
// for the agent's lifetime (the dominant steady-state path).
func TestRuntimeReclaimsAttachOnDisconnect(t *testing.T) {
	srv := httptest.NewServer(signal.New().Handler())
	defer srv.Close()

	ownerPriv, ownerPub, _ := noise.GenerateStatic()
	dir := t.TempDir()
	if _, err := LoadOrInit(dir, "leak-machine", srv.URL); err != nil {
		t.Fatal(err)
	}
	if err := PinOwner(dir, hex.EncodeToString(ownerPub)); err != nil {
		t.Fatal(err)
	}
	cfg, _ := LoadOrInit(dir, "leak-machine", srv.URL)

	// Long-lived agent ctx — stays alive across the whole test (it must NOT be
	// what frees the attach).
	agentCtx, agentCancel := context.WithCancel(context.Background())
	defer agentCancel()

	rt := NewRuntime(cfg, []string{"sh"}, nil)
	go func() { _ = rt.Up(agentCtx) }()
	time.Sleep(300 * time.Millisecond)

	// Browser-stand-in: attach, complete Noise, run a command.
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer dialCancel()

	bws := "ws" + strings.TrimPrefix(srv.URL, "http") +
		"/attach?owner_id=" + url.QueryEscape(hex.EncodeToString(ownerPub)) +
		"&machine_id=" + url.QueryEscape(cfg.MachineID)
	bc, _, err := websocket.Dial(dialCtx, bws, nil)
	if err != nil {
		t.Fatal(err)
	}
	off, opened, err := peer.NewOfferer(nil)
	if err != nil {
		t.Fatal(err)
	}

	offerSDP, err := peer.CreateOffer(off)
	if err != nil {
		t.Fatal(err)
	}
	offerMsg, _ := json.Marshal(signal.SignalMsg{Type: signal.TypeOffer, SDP: offerSDP})
	if err := bc.Write(dialCtx, websocket.MessageText, offerMsg); err != nil {
		t.Fatal(err)
	}
	_, data, err := bc.Read(dialCtx)
	if err != nil {
		t.Fatal(err)
	}
	var ans signal.SignalMsg
	if json.Unmarshal(data, &ans) != nil || ans.Type != signal.TypeAnswer {
		t.Fatalf("expected answer, got %s", string(data))
	}
	if err := peer.AcceptAnswer(off, ans.SDP); err != nil {
		t.Fatal(err)
	}

	var dc *peer.DataChannel
	select {
	case dc = <-opened:
	case <-dialCtx.Done():
		t.Fatal("DataChannel never opened")
	}

	sess, err := peer.RunInitiator(dialCtx, dc, ownerPriv, cfg.HostPub())
	if err != nil {
		t.Fatalf("initiator handshake: %v", err)
	}

	// Drain HELLO and run a real command so we know the session is fully live
	// (agent-side goroutines are parked in DataChannel.Recv / pty.Read).
	hello := recvFrame(t, dialCtx, dc, sess)
	if typ, _, _ := noise.DecodeFrame(hello); typ != noise.FrameHello {
		t.Fatalf("expected HELLO, got %d", typ)
	}
	sendData(t, dialCtx, dc, sess, []byte("echo LEAK_TEST_OK\n"))
	deadline := time.Now().Add(10 * time.Second)
	var acc bytes.Buffer
	live := false
	for time.Now().Before(deadline) {
		frame := recvFrame(t, dialCtx, dc, sess)
		typ, payload, derr := noise.DecodeFrame(frame)
		if derr != nil || typ != noise.FrameData {
			continue
		}
		acc.Write(payload)
		if bytes.Contains(acc.Bytes(), []byte("LEAK_TEST_OK")) {
			live = true
			break
		}
	}
	if !live {
		t.Fatalf("session never became live; got:\n%s", acc.String())
	}

	// Sanity: the agent-side session goroutine exists right now.
	if countSessionGoroutines() == 0 {
		t.Fatal("expected an active agent.RunAgentSession goroutine while attached")
	}

	// Browser disconnects: close the PeerConnection and the signaling socket.
	_ = off.Close()
	_ = bc.CloseNow()

	// The agent must reclaim the attach (goroutines parked in RunAgentSession go
	// to zero) WITHOUT cancelling the long-lived agent ctx.
	reclaimDeadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(reclaimDeadline) {
		if countSessionGoroutines() == 0 {
			return // reclaimed
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Dump remaining stacks to aid debugging on failure.
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	t.Fatalf("agent did not reclaim attach on disconnect: %d agent.RunAgentSession goroutine(s) still parked.\n%s",
		countSessionGoroutines(), string(buf[:n]))
}
