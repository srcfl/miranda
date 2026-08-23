// go/internal/client/locator_test.go
package client

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// fakeConn is a no-op MsgConn used to identify which locator won the race.
type fakeConn struct{ tag string }

func (fakeConn) Send(b []byte) error                      { return nil }
func (fakeConn) Recv(ctx context.Context) ([]byte, error) { return nil, nil }

// stubLocator returns a canned (conn, cleanup, err). delay (if set) sleeps before
// returning, ignoring ctx — simulating a locator already in flight when the race is
// decided. called records whether Dial actually ran (a staggered locator cancelled
// during its head start never dials).
type stubLocator struct {
	conn    peer.MsgConn
	cleanup func()
	err     error
	called  *int32
	delay   time.Duration
}

func (s stubLocator) Dial(ctx context.Context, m Machine, id *Identity, ice []peer.ICEServer) (peer.MsgConn, func(), error) {
	if s.called != nil {
		atomic.StoreInt32(s.called, 1)
	}
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return s.conn, s.cleanup, s.err
}

// LAN (locator[0]) connects fast, so it wins inside the head start and the relay is
// never dialed — a successful LAN attach stays relay-free.
func TestDialStaggeredLANWinsRelayNeverDialed(t *testing.T) {
	want := fakeConn{tag: "lan"}
	var relayCalled int32
	locators := []Locator{
		stubLocator{conn: want},
		stubLocator{conn: fakeConn{tag: "relay"}, called: &relayCalled},
	}
	mc, _, cleanup, err := dialStaggered(context.Background(), locators, 200*time.Millisecond, Machine{Name: "box"}, &Identity{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mc != want {
		t.Fatalf("got %#v, want the LAN conn", mc)
	}
	cleanup()
	if atomic.LoadInt32(&relayCalled) != 0 {
		t.Fatal("relay must NOT be dialed when LAN wins within the head start")
	}
}

// No LAN answer (ErrUnreachable) -> the relay starts after the head start and wins.
func TestDialStaggeredFallsToRelay(t *testing.T) {
	want := fakeConn{tag: "relay"}
	locators := []Locator{
		stubLocator{err: ErrUnreachable},
		stubLocator{conn: want},
	}
	mc, _, _, err := dialStaggered(context.Background(), locators, 10*time.Millisecond, Machine{Name: "box"}, &Identity{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mc != want {
		t.Fatalf("got %#v, want the relay conn", mc)
	}
}

// When everything fails, surface the relay's REAL error, not the LAN ErrUnreachable.
func TestDialStaggeredAllFailPrefersRealError(t *testing.T) {
	boom := errors.New("signaling: machine offline")
	locators := []Locator{
		stubLocator{err: ErrUnreachable},
		stubLocator{err: boom},
	}
	mc, _, _, err := dialStaggered(context.Background(), locators, 5*time.Millisecond, Machine{Name: "box"}, &Identity{}, nil, nil)
	if mc != nil {
		t.Fatal("expected no conn when all locators fail")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("expected the real relay error, got: %v", err)
	}
}

// A slow loser that still connects after the winner is chosen must be cleaned up
// (its conn would otherwise leak).
func TestDialStaggeredCleansSlowLoser(t *testing.T) {
	var loserCleaned int32
	locators := []Locator{
		// LAN: slow success — it loses the race but still connects later.
		stubLocator{conn: fakeConn{tag: "lan"}, cleanup: func() { atomic.StoreInt32(&loserCleaned, 1) }, delay: 60 * time.Millisecond},
		// relay: wins shortly after its (small) head start.
		stubLocator{conn: fakeConn{tag: "relay"}},
	}
	mc, _, _, err := dialStaggered(context.Background(), locators, 5*time.Millisecond, Machine{Name: "box"}, &Identity{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mc.(fakeConn).tag != "relay" {
		t.Fatalf("expected the relay to win, got %v", mc)
	}
	// Give the slow loser time to return and be drained/cleaned.
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&loserCleaned) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&loserCleaned) == 0 {
		t.Fatal("the slow loser's conn was not cleaned up")
	}
}

// A single locator (relay-only) dials directly with no race.
func TestDialStaggeredSingleLocatorDirect(t *testing.T) {
	want := fakeConn{tag: "relay"}
	var called int32
	mc, _, _, err := dialStaggered(context.Background(), []Locator{stubLocator{conn: want, called: &called}}, 50*time.Millisecond, Machine{Name: "box"}, &Identity{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mc != want || atomic.LoadInt32(&called) != 1 {
		t.Fatal("single locator should be dialed directly")
	}
}

// A locator that opens a socket but fails KK is not a win: the race continues
// and a later locator that authenticates succeeds.
func TestDialStaggeredKKFailureFallsThrough(t *testing.T) {
	want := fakeConn{tag: "relay"}
	var lanCleaned int32
	auth := func(_ context.Context, mc peer.MsgConn, _ Machine, _ *Identity) (*noise.Session, error) {
		if mc.(fakeConn).tag == "lan" {
			return nil, ErrUnreachable
		}
		return nil, nil
	}
	locators := []Locator{
		stubLocator{conn: fakeConn{tag: "lan"}, cleanup: func() { atomic.StoreInt32(&lanCleaned, 1) }},
		stubLocator{conn: want},
	}
	mc, _, _, err := dialStaggered(context.Background(), locators, 10*time.Millisecond, Machine{Name: "box"}, &Identity{}, nil, auth)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mc != want {
		t.Fatalf("got %#v, want the relay conn after LAN KK failure", mc)
	}
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&lanCleaned) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&lanCleaned) == 0 {
		t.Fatal("LAN conn that failed KK must be cleaned up")
	}
}

// kkPipeLocator returns one end of a Pipe whose other end is a Noise-KK
// responder with the given static keys. Used to drive shipped kkAuth inside
// dialStaggered without reimplementing the auth policy.
type kkPipeLocator struct {
	hostPriv, peerPub []byte
}

func (k kkPipeLocator) Dial(ctx context.Context, _ Machine, _ *Identity, _ []peer.ICEServer) (peer.MsgConn, func(), error) {
	clientMC, agentMC := peer.Pipe()
	go func() { _, _ = peer.RunResponder(ctx, agentMC, k.hostPriv, k.peerPub) }()
	return clientMC, func() {}, nil
}

type delayLocator struct {
	delay time.Duration
	loc   Locator
}

func (d delayLocator) Dial(ctx context.Context, m Machine, id *Identity, ice []peer.ICEServer) (peer.MsgConn, func(), error) {
	t := time.NewTimer(d.delay)
	defer t.Stop()
	select {
	case <-t.C:
		return d.loc.Dial(ctx, m, id, ice)
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

func TestDialStaggeredKKAuthWrongHostFallsToRelay(t *testing.T) {
	lanPriv, _, err := noise.GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	relayPriv, relayPub, err := noise.GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	ownerPriv, ownerPub, err := noise.GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	id := &Identity{OwnerPrivHex: fmt.Sprintf("%x", ownerPriv)}
	locators := []Locator{
		kkPipeLocator{hostPriv: lanPriv, peerPub: ownerPub},
		delayLocator{delay: kkHandshakeTimeout + 50*time.Millisecond, loc: kkPipeLocator{hostPriv: relayPriv, peerPub: ownerPub}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_, sess, cleanup, err := dialStaggered(ctx, locators, time.Millisecond, Machine{Name: "box"}, id, nil, kkAuth(relayPub))
	if err != nil {
		t.Fatalf("relay KK should win after LAN wrong host: %v", err)
	}
	if cleanup != nil {
		cleanup()
	}
	if sess == nil {
		t.Fatal("winner must be a completed KK session, not transport-only")
	}
}

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

func TestDialStaggeredKKTimeoutIsUnreachable(t *testing.T) {
	want := fakeConn{tag: "relay"}
	auth := func(ctx context.Context, mc peer.MsgConn, _ Machine, _ *Identity) (*noise.Session, error) {
		if mc.(fakeConn).tag == "lan" {
			return nil, fmt.Errorf("%w: handshake stall", ErrUnreachable)
		}
		return nil, nil
	}
	locators := []Locator{
		stubLocator{conn: fakeConn{tag: "lan"}},
		stubLocator{conn: want},
	}
	mc, _, _, err := dialStaggered(context.Background(), locators, 5*time.Millisecond, Machine{Name: "box"}, &Identity{}, nil, auth)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mc != want {
		t.Fatalf("got %#v, want relay after LAN handshake stall", mc)
	}
}
