package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

// The agent must register under EVERY paired owner so any of your devices
// (laptop CLI, phone, ...) can reach the machine — not just the first-paired one.
func TestUpRegistersAllOwners(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Query().Get("owner_id")] = true
		mu.Unlock()
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_, _, _ = c.Read(r.Context()) // hold the registration open until the test ends
	}))
	defer srv.Close()

	cfg := &Config{
		SignalURL:    srv.URL,
		MachineID:    "m1",
		PairedOwners: []string{"aaaaaaaa", "bbbbbbbb"}, // two devices
	}
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	rt.baseBackoff, rt.maxBackoff = 10*time.Millisecond, 10*time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	go func() { _ = rt.Up(ctx) }()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		ok := seen["aaaaaaaa"] && seen["bbbbbbbb"]
		mu.Unlock()
		if ok {
			return // both owners registered — pass
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("agent did not register under both owners; saw %v", seen)
}

func TestUpSendsRegistrationSecret(t *testing.T) {
	const secret = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	seen := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get(signal.AgentRegistrationSecretHeader)
		if c, err := websocket.Accept(w, r, nil); err == nil {
			_, _, _ = c.Read(r.Context())
		}
	}))
	defer srv.Close()

	cfg := &Config{
		SignalURL:          srv.URL,
		MachineID:          "m1",
		RegistrationSecret: secret,
		PairedOwners:       []string{"aaaaaaaa"},
	}
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	rt.baseBackoff, rt.maxBackoff = 10*time.Millisecond, 10*time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	go func() { _ = rt.Up(ctx) }()

	select {
	case got := <-seen:
		if got != secret {
			t.Fatalf("registration secret header = %q, want %q", got, secret)
		}
	case <-ctx.Done():
		t.Fatal("agent never registered")
	}
}

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

// Pairing a new device/identity must take effect WITHOUT restarting the agent:
// `mir-agent up` should pick up an owner added to config.json at runtime.
func TestUpHotReloadsNewlyPairedOwner(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Query().Get("owner_id")] = true
		mu.Unlock()
		if c, err := websocket.Accept(w, r, nil); err == nil {
			_, _, _ = c.Read(r.Context())
		}
	}))
	defer srv.Close()
	saw := func(owner string) bool { mu.Lock(); defer mu.Unlock(); return seen[owner] }
	waitFor := func(owner string, d time.Duration) bool {
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			if saw(owner) {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}

	dir := t.TempDir()
	if _, err := LoadOrInit(dir, "m", srv.URL); err != nil {
		t.Fatal(err)
	}
	if err := PinOwner(dir, "aaaaaaaa"); err != nil { // first device
		t.Fatal(err)
	}
	cfg, err := LoadOrInit(dir, "m", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	rt.baseBackoff, rt.maxBackoff, rt.reloadInterval = 10*time.Millisecond, 10*time.Millisecond, 30*time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = rt.Up(ctx) }()

	if !waitFor("aaaaaaaa", 700*time.Millisecond) {
		t.Fatal("agent never registered the initial owner")
	}
	if err := PinOwner(dir, "bbbbbbbb"); err != nil { // pair a NEW device at runtime
		t.Fatal(err)
	}
	if !waitFor("bbbbbbbb", 1*time.Second) {
		t.Fatal("agent did not hot-reload the newly-paired owner")
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
