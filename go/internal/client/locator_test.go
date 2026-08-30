// go/internal/client/locator_test.go
package client

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// fakeConn is a no-op MsgConn used to identify the dialed connection.
type fakeConn struct{ tag string }

func (fakeConn) Send(b []byte) error                      { return nil }
func (fakeConn) Recv(ctx context.Context) ([]byte, error) { return nil, nil }

// stubLocator returns a canned (conn, cleanup, err).
type stubLocator struct {
	conn    peer.MsgConn
	cleanup func()
	err     error
}

func (s stubLocator) Dial(ctx context.Context, m Machine, id *Identity, ice []peer.ICEServer) (peer.MsgConn, func(), error) {
	return s.conn, s.cleanup, s.err
}

// A transport win with nil auth passes straight through (unit-test mode).
func TestDialLocatorTransportOnly(t *testing.T) {
	want := fakeConn{tag: "relay"}
	mc, sess, _, err := dialLocator(context.Background(), stubLocator{conn: want}, Machine{Name: "box"}, &Identity{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mc != want || sess != nil {
		t.Fatalf("got (%#v, %v), want the conn and no session", mc, sess)
	}
}

// The locator's real error surfaces untouched.
func TestDialLocatorSurfacesDialError(t *testing.T) {
	boom := errors.New("signaling: machine offline")
	mc, _, _, err := dialLocator(context.Background(), stubLocator{err: boom}, Machine{Name: "box"}, &Identity{}, nil, nil)
	if mc != nil {
		t.Fatal("expected no conn on dial failure")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("expected the real dial error, got: %v", err)
	}
}

// A locator that opens a socket but fails auth is not a win: the conn is
// cleaned up and the result is ErrUnreachable.
func TestDialLocatorAuthFailureCleansUp(t *testing.T) {
	cleaned := false
	auth := func(context.Context, peer.MsgConn, Machine, *Identity) (*noise.Session, error) {
		return nil, fmt.Errorf("%w: handshake stall", ErrUnreachable)
	}
	mc, _, _, err := dialLocator(context.Background(), stubLocator{conn: fakeConn{tag: "relay"}, cleanup: func() { cleaned = true }}, Machine{Name: "box"}, &Identity{}, nil, auth)
	if mc != nil {
		t.Fatal("auth failure must not yield a conn")
	}
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("want ErrUnreachable, got: %v", err)
	}
	if !cleaned {
		t.Fatal("the conn that failed auth must be cleaned up")
	}
}

// kkPipeLocator returns one end of a Pipe whose other end is a Noise-KK
// responder with the given static keys. Drives the shipped kkAuth policy
// through dialLocator without reimplementing it.
type kkPipeLocator struct {
	hostPriv, peerPub []byte
}

func (k kkPipeLocator) Dial(ctx context.Context, _ Machine, _ *Identity, _ []peer.ICEServer) (peer.MsgConn, func(), error) {
	clientMC, agentMC := peer.Pipe()
	go func() { _, _ = peer.RunResponder(ctx, agentMC, k.hostPriv, k.peerPub) }()
	return clientMC, func() {}, nil
}

// The full path: KK against the pinned host key completes and yields a session.
func TestDialLocatorKKSuccess(t *testing.T) {
	hostPriv, hostPub, err := noise.GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	ownerPriv, ownerPub, err := noise.GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	id := &Identity{OwnerPrivHex: fmt.Sprintf("%x", ownerPriv)}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, sess, cleanup, err := dialLocator(ctx, kkPipeLocator{hostPriv: hostPriv, peerPub: ownerPub}, Machine{Name: "box"}, id, nil, kkAuth(hostPub))
	if err != nil {
		t.Fatalf("KK against the pinned key should succeed: %v", err)
	}
	if cleanup != nil {
		cleanup()
	}
	if sess == nil {
		t.Fatal("the win must be a completed KK session, not transport-only")
	}
}

// The wrong host key is unreachable, never a win.
func TestKKAuthWrongHostKeyIsUnreachable(t *testing.T) {
	hostPriv, _, err := noise.GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	ownerPriv, ownerPub, err := noise.GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	_, wrongHost, err := noise.GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}

	clientMC, agentMC := peer.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	go func() {
		_, _ = peer.RunResponder(ctx, agentMC, hostPriv, ownerPub)
	}()

	id := &Identity{OwnerPrivHex: fmt.Sprintf("%x", ownerPriv)}
	_, err = kkAuth(wrongHost)(ctx, clientMC, Machine{Name: "box"}, id)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("kkAuth with the wrong host key: %v, want ErrUnreachable", err)
	}
}
