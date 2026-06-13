// go/internal/agent/e2e_test.go
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

func TestEndToEndRealShellOverP2P(t *testing.T) {
	srv := httptest.NewServer(signal.New().Handler())
	defer srv.Close()

	// Owner (browser) identity + agent keystore with that owner pinned. The owner
	// is a real wallet: owner_id is the base58 wallet address, and the Noise pin is
	// recovered from a wallet-signed binding carried on the offer (B1.4.1).
	ownerPriv, _, ownerID, bindingJSON := ownerBinding(t, bytes.Repeat([]byte{0x11}, 32), "owner-device-e2e")
	dir := t.TempDir()
	cfg, err := LoadOrInit(dir, "e2e-machine", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := PinOwner(dir, ownerID); err != nil {
		t.Fatal(err)
	}
	cfg, _ = LoadOrInit(dir, "e2e-machine", srv.URL) // reload with the pinned owner

	// Start the agent runtime (sh, not tmux; nil STUN = localhost host candidates).
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	go func() { _ = rt.Up(ctx) }()
	time.Sleep(300 * time.Millisecond) // let the agent register

	// Browser-stand-in: attach, offer, await answer, open DataChannel, Noise init.
	bws := "ws" + strings.TrimPrefix(srv.URL, "http") +
		"/attach?owner_id=" + url.QueryEscape(ownerID) +
		"&machine_id=" + url.QueryEscape(cfg.MachineID)
	bc, _, err := websocket.Dial(ctx, bws, nil)
	if err != nil {
		t.Fatal(err)
	}
	off, opened, err := peer.NewOfferer(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer off.Close()

	offerSDP, err := peer.CreateOffer(off)
	if err != nil {
		t.Fatal(err)
	}
	offerMsg, _ := json.Marshal(signal.SignalMsg{Type: signal.TypeOffer, SDP: offerSDP, Binding: bindingJSON})
	if err := bc.Write(ctx, websocket.MessageText, offerMsg); err != nil {
		t.Fatal(err)
	}
	_, data, err := bc.Read(ctx)
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
	case <-ctx.Done():
		t.Fatal("DataChannel never opened")
	}

	agentHostPub := cfg.HostPub()
	sess, err := peer.RunInitiator(ctx, dc, ownerPriv, agentHostPub)
	if err != nil {
		t.Fatalf("initiator handshake: %v", err)
	}

	// First frame must be HELLO with the machine name.
	hello := recvFrame(t, ctx, dc, sess)
	htype, hpayload, _ := noise.DecodeFrame(hello)
	if htype != noise.FrameHello {
		t.Fatalf("expected HELLO, got %d", htype)
	}
	var meta map[string]string
	_ = json.Unmarshal(hpayload, &meta)
	if meta["name"] != "e2e-machine" {
		t.Fatalf("HELLO name = %q", meta["name"])
	}

	// Run a real command over the encrypted P2P channel.
	sendData(t, ctx, dc, sess, []byte("echo E2E_P2P_OK\n"))
	deadline := time.Now().Add(10 * time.Second)
	var acc bytes.Buffer
	for time.Now().Before(deadline) {
		frame := recvFrame(t, ctx, dc, sess)
		typ, payload, err := noise.DecodeFrame(frame)
		if err != nil || typ != noise.FrameData {
			continue
		}
		acc.Write(payload)
		if bytes.Contains(acc.Bytes(), []byte("E2E_P2P_OK")) {
			return // SUCCESS: real shell, over P2P, through the signaling server
		}
	}
	t.Fatalf("never saw command output; got:\n%s", acc.String())
}
