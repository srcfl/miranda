package agent

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

func TestAuthorizeOfferFollowsLivePinSet(t *testing.T) {
	secret := bytes.Repeat([]byte{0x33}, 32)
	_, _, ownerID, bindingJSON := ownerBinding(t, secret, "pinset-device")
	dir := t.TempDir()
	cfg, err := LoadOrInit(dir, "box", "http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt.reconcileOwners(ctx, nil)

	offer := func(session string) signal.SignalMsg {
		sdp := "v=0"
		return signal.SignalMsg{
			Session: session,
			SDP:     sdp,
			Binding: bindingJSON,
			Auth:    ownerAttachAuth(t, secret, session, cfg.MachineID, sdp),
		}
	}
	if _, err := rt.authorizeOffer(ownerID, offer("s1")); err == nil {
		t.Fatal("unpinned owner must be rejected")
	}

	if err := PinOwner(dir, ownerID); err != nil {
		t.Fatal(err)
	}
	owners, err := ReloadOwners(dir)
	if err != nil {
		t.Fatal(err)
	}
	rt.reconcileOwners(ctx, owners)
	if _, err := rt.authorizeOffer(ownerID, offer("s2")); err != nil {
		t.Fatalf("disk-pinned owner must authorize without reconstructing Runtime: %v", err)
	}

	rt.reconcileOwners(ctx, nil)
	if _, err := rt.authorizeOffer(ownerID, offer("s3")); err == nil {
		t.Fatal("removed owner must be rejected")
	}
}

func TestHandshakeSlotFreedWhileSessionActive(t *testing.T) {
	hostPriv, hostPub, err := noise.GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	ownerPriv, ownerPub, err := noise.GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(&Config{HostPrivHex: hex.EncodeToString(hostPriv)}, []string{"sh"}, nil)
	rt.sem = make(chan struct{}, 1)
	if !rt.admit() {
		t.Fatal("fresh runtime should admit")
	}
	held := true
	released := make(chan struct{})
	releaseHS := func() {
		if held {
			held = false
			rt.release()
			close(released)
		}
	}

	clientMC, agentMC := peer.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- rt.serveAuthenticated(ctx, agentMC, "owner-test", &offerAuth{pub: ownerPub}, releaseHS)
	}()
	if _, err := peer.RunInitiator(ctx, clientMC, ownerPriv, hostPub); err != nil {
		t.Fatalf("initiator KK: %v", err)
	}
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("serveAuthenticated must call handshakeDone after KK success")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && rt.ActiveSessions() != 1 {
		time.Sleep(10 * time.Millisecond)
	}
	if rt.ActiveSessions() != 1 {
		t.Fatalf("active sessions = %d, want 1 while the PTY session remains", rt.ActiveSessions())
	}
	if !rt.admit() {
		t.Fatal("handshake slot must be free while an authenticated session is live")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serveAuthenticated did not return after cancel")
	}
}
