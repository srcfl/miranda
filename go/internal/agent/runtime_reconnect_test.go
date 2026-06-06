package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The agent must survive a dropped signaling connection (Cloudflare idle timeout,
// relay restart, network blip) by reconnecting — not exit. This server accepts
// then immediately drops every connection; a correct agent keeps coming back.
func TestUpReconnectsAfterDrop(t *testing.T) {
	var conns int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		atomic.AddInt32(&conns, 1)
		c.Close(websocket.StatusNormalClosure, "drop")
	}))
	defer srv.Close()

	cfg := &Config{
		SignalURL:    srv.URL,
		MachineID:    "m1",
		PairedOwners: []string{"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"},
	}
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	rt.baseBackoff = 15 * time.Millisecond
	rt.maxBackoff = 15 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_ = rt.Up(ctx)

	if n := atomic.LoadInt32(&conns); n < 3 {
		t.Fatalf("expected the agent to reconnect repeatedly, got %d connection(s)", n)
	}
}

// Up returns cleanly (nil) when the context is cancelled, not an error.
func TestUpReturnsNilOnCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := websocket.Accept(w, r, nil); err == nil {
			c.Close(websocket.StatusNormalClosure, "drop")
		}
	}))
	defer srv.Close()
	cfg := &Config{SignalURL: srv.URL, MachineID: "m", PairedOwners: []string{"ab"}}
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	rt.baseBackoff, rt.maxBackoff = 10*time.Millisecond, 10*time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := rt.Up(ctx); err != nil {
		t.Fatalf("Up should return nil on cancel, got %v", err)
	}
}
