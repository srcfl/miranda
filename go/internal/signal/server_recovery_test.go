// go/internal/signal/server_recovery_test.go
package signal

import (
	"context"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// When an attached agent's socket closes mid-session, every browser bound to that
// agent must be told the machine went offline (TypeError) and its handler torn
// down — not left hanging forever. Regression for the goroutine-leak/browser-hang
// issue (server.go: browser ctx was independent of the agent's ctx).
func TestAgentDeathNotifiesAttachedBrowsers(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	agent := dialJSON(t, wsURL(srv.URL, "/agent/signal", map[string]string{"owner_id": "o", "machine_id": "m"}))
	if ready := readMsg(t, agent); ready.Type != TypeReady {
		t.Fatalf("expected ready, got %q", ready.Type)
	}

	browser := dialJSON(t, wsURL(srv.URL, "/attach", map[string]string{"owner_id": "o", "machine_id": "m"}))

	// Agent learns about the attach.
	if attach := readMsg(t, agent); attach.Type != TypeAttach || attach.Session == "" {
		t.Fatalf("expected attach with session, got %+v", attach)
	}

	// The agent disappears (crash / network blip).
	if err := agent.CloseNow(); err != nil {
		t.Fatalf("close agent: %v", err)
	}

	// The browser must receive a TypeError ("machine offline") promptly, not hang.
	m := readMsg(t, browser)
	if m.Type != TypeError {
		t.Fatalf("expected error after agent death, got %+v", m)
	}
}

// A blocked browser writer must not be able to wedge the agent's notify path.
// Many browsers attach but never read; the agent must still receive every
// TypeAttach (bare sends on ac.out replaced with non-blocking/ctx-aware sends).
func TestSlowBrowsersDoNotWedgeAgentNotify(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	agent := dialJSON(t, wsURL(srv.URL, "/agent/signal", map[string]string{"owner_id": "o", "machine_id": "m"}))
	if ready := readMsg(t, agent); ready.Type != TypeReady {
		t.Fatalf("expected ready, got %q", ready.Type)
	}

	const n = 40
	browsers := make([]*websocket.Conn, 0, n)
	for i := 0; i < n; i++ {
		// Never read from these browsers; their bc.out will fill up.
		b := dialJSON(t, wsURL(srv.URL, "/attach", map[string]string{"owner_id": "o", "machine_id": "m"}))
		browsers = append(browsers, b)
	}
	defer func() {
		for _, b := range browsers {
			_ = b.CloseNow()
		}
	}()

	// The agent must receive all n attach notifications without the broker stalling.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := 0
	for got < n {
		_, data, err := agent.Read(ctx)
		if err != nil {
			t.Fatalf("agent read after %d/%d attaches: %v", got, n, err)
		}
		if msg, _ := decodeSignal(data); msg.Type == TypeAttach {
			got++
		}
	}
}

// When a second agent registers the same owner|machine key (routine on agent
// restart), browsers attached to the OLD agent must not keep routing offers to
// the dead one. The old session is torn down (TypeError -> re-attach), the old
// agent's goroutines exit, and a fresh attach reaches the NEW live agent.
// Regression for split-brain routing (cached stale ac pointer).
func TestAgentReRegistrationTearsDownOldSessions(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	a1 := dialJSON(t, wsURL(srv.URL, "/agent/signal", map[string]string{"owner_id": "o", "machine_id": "m"}))
	if ready := readMsg(t, a1); ready.Type != TypeReady {
		t.Fatalf("a1 expected ready, got %q", ready.Type)
	}

	browser := dialJSON(t, wsURL(srv.URL, "/attach", map[string]string{"owner_id": "o", "machine_id": "m"}))
	if attach := readMsg(t, a1); attach.Type != TypeAttach {
		t.Fatalf("a1 expected attach, got %+v", attach)
	}

	// A second agent registers the same key (agent restart/reconnect).
	a2 := dialJSON(t, wsURL(srv.URL, "/agent/signal", map[string]string{"owner_id": "o", "machine_id": "m"}))
	if ready := readMsg(t, a2); ready.Type != TypeReady {
		t.Fatalf("a2 expected ready, got %q", ready.Type)
	}
	defer a2.CloseNow()

	// The browser attached to the now-stale a1 must be told to re-attach
	// (TypeError), not keep sending offers into the void.
	m := readMsg(t, browser)
	if m.Type != TypeError {
		t.Fatalf("expected error/re-attach for browser on replaced agent, got %+v", m)
	}

	// A fresh attach now reaches the live agent a2 (and not the dead a1).
	browser2 := dialJSON(t, wsURL(srv.URL, "/attach", map[string]string{"owner_id": "o", "machine_id": "m"}))
	defer browser2.CloseNow()
	attach := readMsg(t, a2)
	if attach.Type != TypeAttach || attach.Session == "" {
		t.Fatalf("a2 expected attach with session, got %+v", attach)
	}

	writeMsg(t, browser2, SignalMsg{Type: TypeOffer, SDP: "OFFER-SDP-2"})
	gotOffer := readMsg(t, a2)
	if gotOffer.Type != TypeOffer || gotOffer.SDP != "OFFER-SDP-2" || gotOffer.Session != attach.Session {
		t.Fatalf("a2 (live agent) got wrong offer: %+v", gotOffer)
	}
}

// Sanity: after an agent dies with browsers attached, goroutines do not leak
// unboundedly. We don't assert an exact count (test harness has its own
// goroutines) but that the count returns near baseline after teardown.
func TestNoGoroutineLeakAfterAgentDeath(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	base := runtime.NumGoroutine()

	agent := dialJSON(t, wsURL(srv.URL, "/agent/signal", map[string]string{"owner_id": "o", "machine_id": "m"}))
	if ready := readMsg(t, agent); ready.Type != TypeReady {
		t.Fatalf("expected ready, got %q", ready.Type)
	}

	const n = 30
	browsers := make([]*websocket.Conn, 0, n)
	for i := 0; i < n; i++ {
		b := dialJSON(t, wsURL(srv.URL, "/attach", map[string]string{"owner_id": "o", "machine_id": "m"}))
		browsers = append(browsers, b)
		// drain the agent's attach notification so the broker proceeds
		_ = readMsg(t, agent)
	}

	// Kill the agent; all bound browser handlers must unwind.
	_ = agent.CloseNow()

	// Read the TypeError on each browser, then close it.
	for _, b := range browsers {
		_ = readMsg(t, b)
		_ = b.CloseNow()
	}

	// Allow goroutines to unwind.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= base+15 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("goroutines did not return near baseline: base=%d now=%d", base, runtime.NumGoroutine())
}
