// go/internal/signal/pair_test.go
package signal

import (
	"context"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestPairBridgeForwardsBothWays(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	ctx := context.Background()
	dial := func() *websocket.Conn {
		c, _, err := websocket.Dial(ctx, wsURL(srv.URL, "/pair", map[string]string{"room": "abc"}), nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	a := dial()
	b := dial()

	if err := a.Write(ctx, websocket.MessageBinary, []byte("a->b")); err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, got, err := b.Read(rctx)
	if err != nil || string(got) != "a->b" {
		t.Fatalf("b got %q err %v", got, err)
	}

	if err := b.Write(ctx, websocket.MessageBinary, []byte("b->a")); err != nil {
		t.Fatal(err)
	}
	_, got, err = a.Read(rctx)
	if err != nil || string(got) != "b->a" {
		t.Fatalf("a got %q err %v", got, err)
	}
}

func TestPairRoomCapacityRejectsNewRooms(t *testing.T) {
	p := newPairRooms()
	p.waiting["full"] = &pairWaiter{partner: make(chan *websocket.Conn, 1), done: make(chan struct{})}

	_, _, _, err := p.rendezvous("other", nil, 1)
	if err != errPairCapacity {
		t.Fatalf("expected pair capacity error, got %v", err)
	}
	if len(p.waiting) != 1 {
		t.Fatalf("capacity rejection should not grow waiting rooms: %d", len(p.waiting))
	}

	other, done, drive, err := p.rendezvous("full", nil, 1)
	if err != nil {
		t.Fatalf("existing room should still pair at capacity: %v", err)
	}
	if other != nil || done == nil || drive {
		t.Fatalf("unexpected existing-room rendezvous result: other=%v done=%v drive=%v", other, done, drive)
	}
}

// countHandlePair returns how many goroutines currently have a handlePair frame
// on their stack.
func countHandlePair() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "(*Server).handlePair")
}

// TestPairBridgeNoGoroutineLeak proves that after BOTH parties close their
// pairing sockets cleanly (the normal end of a successful pairing), no
// handlePair goroutine — and therefore no hijacked socket FD — survives. The
// non-driving handler must not be gated on r.Context() after a websocket hijack,
// or it leaks indefinitely on the shared blind signaling server.
func TestPairBridgeNoGoroutineLeak(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	ctx := context.Background()
	dial := func() *websocket.Conn {
		c, _, err := websocket.Dial(ctx, wsURL(srv.URL, "/pair", map[string]string{"room": "leak"}), nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	before := countHandlePair()

	a := dial()
	b := dial()

	// Exchange one frame each way so both handlers are fully engaged (the driver
	// is bridging, the non-driver is parked).
	if err := a.Write(ctx, websocket.MessageBinary, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, _, err := b.Read(rctx); err != nil {
		t.Fatal(err)
	}

	// Both parties close cleanly — the normal end of a successful pairing.
	_ = a.Close(websocket.StatusNormalClosure, "")
	_ = b.Close(websocket.StatusNormalClosure, "")

	// Poll until both handlePair goroutines have returned. If the non-driving
	// handler leaks, this never converges and the test fails on timeout.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if countHandlePair() <= before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("handlePair goroutine(s) leaked: before=%d now=%d", before, countHandlePair())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
