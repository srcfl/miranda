// go/internal/client/lan_locator_test.go
package client

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/quicmsg"
)

// staticResolver returns a fixed address (or a fixed error), so the QUIC/Noise
// path runs in tests without touching mDNS multicast (flaky in CI).
type staticResolver struct {
	addr string
	err  error
}

func (s staticResolver) resolve(ctx context.Context, machineID string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.addr, nil
}

// TestLANLocatorDialSendsBinding stands up a real quicmsg listener, dials it via
// lanLocator, and asserts the wallet binding is delivered as frame 0.
func TestLANLocatorDialSendsBinding(t *testing.T) {
	ln, err := quicmsg.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	gotFrame := make(chan []byte, 1)
	gotErr := make(chan error, 1)
	go func() {
		actx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, err := ln.Accept(actx)
		if err != nil {
			gotErr <- err
			return
		}
		frame, err := conn.Recv(actx)
		if err != nil {
			gotErr <- err
			return
		}
		gotFrame <- frame
	}()

	id := &Identity{}
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	if err := id.SetFromSecret(secret); err != nil {
		t.Fatalf("set from secret: %v", err)
	}
	if !id.HasWallet() {
		t.Fatal("expected identity to have a wallet")
	}

	m := Machine{Name: "box", MachineID: "machine-xyz"}
	res := staticResolver{addr: ln.Addr().String()}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, cleanup, err := lanLocator{res: res}.Dial(ctx, m, id, nil)
	if err != nil {
		t.Fatalf("Dial: unexpected error: %v", err)
	}
	if conn == nil {
		t.Fatal("Dial: nil MsgConn")
	}
	if cleanup == nil {
		t.Fatal("Dial: nil cleanup")
	}
	defer cleanup()

	select {
	case err := <-gotErr:
		t.Fatalf("stub agent error: %v", err)
	case frame := <-gotFrame:
		if !bytes.Equal(frame, []byte(id.BindingJSON)) {
			t.Fatalf("frame 0 mismatch:\n got=%q\nwant=%q", frame, id.BindingJSON)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stub agent to receive frame 0")
	}
}

// TestLANLocatorUnreachableOnResolveMiss: a resolver that can't find the machine
// makes Dial return ErrUnreachable so Attach falls through to the relay.
func TestLANLocatorUnreachableOnResolveMiss(t *testing.T) {
	id := &Identity{}
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 7)
	}
	if err := id.SetFromSecret(secret); err != nil {
		t.Fatalf("set from secret: %v", err)
	}

	res := staticResolver{err: ErrUnreachable}
	m := Machine{Name: "box", MachineID: "machine-missing"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, cleanup, err := lanLocator{res: res}.Dial(ctx, m, id, nil)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("expected ErrUnreachable, got %v", err)
	}
	if conn != nil || cleanup != nil {
		t.Fatal("expected nil conn/cleanup on miss")
	}
}

// TestLANLocatorUnreachableWithoutWallet: a legacy (no-wallet) identity can't do
// LAN attach (which requires a binding), so Dial returns ErrUnreachable before
// even touching the resolver.
func TestLANLocatorUnreachableWithoutWallet(t *testing.T) {
	id := &Identity{} // no SecretHex/BindingJSON => HasWallet()==false
	if id.HasWallet() {
		t.Fatal("expected identity to have no wallet")
	}

	// Resolver that would panic if consulted — proves Dial short-circuits.
	res := staticResolver{addr: "should.not.dial:1"}
	m := Machine{Name: "box", MachineID: "machine-legacy"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, cleanup, err := lanLocator{res: res}.Dial(ctx, m, id, nil)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("expected ErrUnreachable, got %v", err)
	}
	if conn != nil || cleanup != nil {
		t.Fatal("expected nil conn/cleanup without a wallet")
	}
}
