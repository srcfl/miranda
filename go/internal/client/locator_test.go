// go/internal/client/locator_test.go
package client

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

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
	mc, cleanup, err := dialStaggered(context.Background(), locators, 200*time.Millisecond, Machine{Name: "box"}, &Identity{}, nil)
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
	mc, _, err := dialStaggered(context.Background(), locators, 10*time.Millisecond, Machine{Name: "box"}, &Identity{}, nil)
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
	mc, _, err := dialStaggered(context.Background(), locators, 5*time.Millisecond, Machine{Name: "box"}, &Identity{}, nil)
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
	mc, _, err := dialStaggered(context.Background(), locators, 5*time.Millisecond, Machine{Name: "box"}, &Identity{}, nil)
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
	mc, _, err := dialStaggered(context.Background(), []Locator{stubLocator{conn: want, called: &called}}, 50*time.Millisecond, Machine{Name: "box"}, &Identity{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mc != want || atomic.LoadInt32(&called) != 1 {
		t.Fatal("single locator should be dialed directly")
	}
}
