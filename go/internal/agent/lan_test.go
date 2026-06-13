// go/internal/agent/lan_test.go
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/quicmsg"
)

// TestLANAttachRealShell drives the LAN-direct path end to end: a QUIC client
// dials the agent's listener, sends a wallet-signed binding as frame 0 (in place
// of the relay offer's binding), runs the Noise-KK initiator handshake, and
// round-trips a real shell command. It is the LAN twin of
// TestEndToEndRealShellOverP2P, swapping the WebRTC DataChannel for quicmsg.
func TestLANAttachRealShell(t *testing.T) {
	// Owner is a real wallet: owner_id is the base58 address, the Noise pin is
	// recovered from a wallet-signed binding carried as frame 0 (B1.4.1).
	ownerPriv, _, ownerID, bindingJSON := ownerBinding(t, bytes.Repeat([]byte{0x22}, 32), "owner-device-lan")
	dir := t.TempDir()
	cfg, err := LoadOrInit(dir, "lan-machine", "http://unused")
	if err != nil {
		t.Fatal(err)
	}
	if err := PinOwner(dir, ownerID); err != nil {
		t.Fatal(err)
	}
	cfg, _ = LoadOrInit(dir, "lan-machine", "http://unused") // reload with the pinned owner

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	rt := NewRuntime(cfg, []string{"sh"}, nil)
	addr, stop, err := rt.startLAN(ctx)
	if err != nil {
		t.Fatalf("startLAN: %v", err)
	}
	defer stop()

	// Browser-stand-in over QUIC: dial, send binding frame 0, Noise init.
	conn, err := quicmsg.Dial(ctx, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := conn.Send([]byte(bindingJSON)); err != nil {
		t.Fatalf("send binding: %v", err)
	}

	sess, err := peer.RunInitiator(ctx, conn, ownerPriv, cfg.HostPub())
	if err != nil {
		t.Fatalf("initiator handshake: %v", err)
	}

	// First frame must be HELLO with the machine name.
	hello := recvFrame(t, ctx, conn, sess)
	htype, hpayload, _ := noise.DecodeFrame(hello)
	if htype != noise.FrameHello {
		t.Fatalf("expected HELLO, got %d", htype)
	}
	var meta map[string]string
	_ = json.Unmarshal(hpayload, &meta)
	if meta["name"] != "lan-machine" {
		t.Fatalf("HELLO name = %q", meta["name"])
	}

	// Run a real command over the encrypted LAN-direct channel.
	sendData(t, ctx, conn, sess, []byte("echo LAN_OK\n"))
	deadline := time.Now().Add(10 * time.Second)
	var acc bytes.Buffer
	for time.Now().Before(deadline) {
		frame := recvFrame(t, ctx, conn, sess)
		typ, payload, derr := noise.DecodeFrame(frame)
		if derr != nil || typ != noise.FrameData {
			continue
		}
		acc.Write(payload)
		if bytes.Contains(acc.Bytes(), []byte("LAN_OK")) {
			return // SUCCESS: real shell, over LAN-direct QUIC
		}
	}
	t.Fatalf("never saw command output; got:\n%s", acc.String())
}

// TestLANRejectsUnpinnedBinding proves the agent refuses a binding for a wallet it
// has not pinned: it closes the stream before the Noise handshake, so the client's
// initiator handshake fails and no session starts.
func TestLANRejectsUnpinnedBinding(t *testing.T) {
	// The agent pins owner A.
	_, _, ownerID, _ := ownerBinding(t, bytes.Repeat([]byte{0x33}, 32), "owner-device-pinned")
	dir := t.TempDir()
	cfg, err := LoadOrInit(dir, "lan-machine", "http://unused")
	if err != nil {
		t.Fatal(err)
	}
	if err := PinOwner(dir, ownerID); err != nil {
		t.Fatal(err)
	}
	cfg, _ = LoadOrInit(dir, "lan-machine", "http://unused")

	// The attacker is owner B (a valid wallet, validly self-signed binding) that the
	// agent has NOT pinned.
	attackerPriv, _, _, attackerBinding := ownerBinding(t, bytes.Repeat([]byte{0x99}, 32), "owner-device-attacker")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	rt := NewRuntime(cfg, []string{"sh"}, nil)
	addr, stop, err := rt.startLAN(ctx)
	if err != nil {
		t.Fatalf("startLAN: %v", err)
	}
	defer stop()

	conn, err := quicmsg.Dial(ctx, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := conn.Send([]byte(attackerBinding)); err != nil {
		t.Fatalf("send binding: %v", err)
	}

	// The agent closes the stream pre-Noise; the initiator handshake must fail.
	hctx, hcancel := context.WithTimeout(ctx, 5*time.Second)
	defer hcancel()
	if _, err := peer.RunInitiator(hctx, conn, attackerPriv, cfg.HostPub()); err == nil {
		t.Fatal("expected handshake failure for unpinned binding, got success")
	}
}
