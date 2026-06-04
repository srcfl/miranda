// go/internal/client/mux_regression_test.go
package client

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// newTestMux builds a Mux with n live sessions backed by real Noise sessions over
// in-memory pipes. The peer side never sends data, so each readSession's Recv
// blocks until ctx is canceled — exactly the "idle" condition the tests need.
// Returns the mux and a cancel that tears down the underlying responder goroutines.
func newTestMux(t *testing.T, n int, out io.Writer) (*Mux, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	sessions := make([]*MuxSession, n)
	for i := 0; i < n; i++ {
		aPriv, aPub, _ := noise.GenerateStatic()
		cPriv, cPub, _ := noise.GenerateStatic()
		clientMC, agentMC := peer.Pipe()
		go func() { _, _ = peer.RunResponder(ctx, agentMC, aPriv, cPub) }()
		sess, err := peer.RunInitiator(ctx, clientMC, cPriv, aPub)
		if err != nil {
			cancel()
			t.Fatalf("handshake %d: %v", i, err)
		}
		sessions[i] = &MuxSession{Name: string(rune('a' + i)), MC: clientMC, Sess: sess}
	}
	return NewMux(sessions, out, DefaultPrefix, Size{Cols: 80, Rows: 24}), cancel
}

// TestFocusNeverStrandedOnDeadSession reproduces the race where two concurrent
// onSessionEnd calls (focused session 0 and another session 1) can leave focus on
// a dead session while a live session remains. We drive the two onSessionEnd calls
// concurrently many times; with the bug, focus can end up stranded on a dead
// session while session 2 is still live. The interleaving is reliably exercised
// under the race detector (the orchestrator runs -race on this package).
func TestFocusNeverStrandedOnDeadSession(t *testing.T) {
	for trial := 0; trial < 300; trial++ {
		out := &syncWriter{}
		m, cancel := newTestMux(t, 3, out)
		// focus = 0; sessions 0 and 1 disconnect concurrently; session 2 stays live.
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); m.onSessionEnd(0) }()
		go func() { defer wg.Done(); m.onSessionEnd(1) }()
		wg.Wait()

		m.mu.Lock()
		focus := m.focus
		focusDead := m.dead[focus]
		live2 := !m.dead[2]
		m.mu.Unlock()
		cancel()

		if live2 && focusDead {
			t.Fatalf("trial %d: focus=%d is dead while session 2 is live", trial, focus)
		}
	}
}

// TestFocusAdvancesWhenFocusedAndOtherDieSequentially locks in the single-threaded
// semantics behind the concurrency fix: if the focused session and another session
// die (in the order that exposed the race), focus must always end on the remaining
// live session — never stranded on a dead one.
func TestFocusAdvancesWhenFocusedAndOtherDieSequentially(t *testing.T) {
	out := &syncWriter{}
	m, cancel := newTestMux(t, 3, out)
	defer cancel()

	// focus = 0. Kill the focused session 0 (focus should advance to 1), then kill
	// session 1 (focus should advance to 2, the only remaining live session).
	m.onSessionEnd(0)
	m.onSessionEnd(1)

	m.mu.Lock()
	focus := m.focus
	focusDead := m.dead[focus]
	m.mu.Unlock()

	if focusDead {
		t.Fatalf("focus=%d ended on a dead session", focus)
	}
	if focus != 2 {
		t.Fatalf("focus=%d, want 2 (the only remaining live session)", focus)
	}
}

// TestMuxRunReturnsOnCtxCancelWhileStdinIdle: when ctx is canceled (e.g. SIGTERM)
// while stdin has no pending input, Run must return promptly so the terminal can be
// restored — it must not wait for the next keystroke.
func TestMuxRunReturnsOnCtxCancelWhileStdinIdle(t *testing.T) {
	out := &syncWriter{}
	m, teardown := newTestMux(t, 1, out)
	defer teardown()
	in := newBlockingReader() // never fed -> Read blocks forever

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx, in, make(chan Size)) }()

	// Let Run get going and block in stdin Read.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// returned promptly
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel while stdin idle")
	}
}

// TestMuxRunReturnsOnAllDisconnectWhileStdinIdle: when every session disconnects
// (closing m.quit) while stdin is idle, Run must return promptly.
func TestMuxRunReturnsOnAllDisconnectWhileStdinIdle(t *testing.T) {
	out := &syncWriter{}
	m, teardown := newTestMux(t, 1, out)
	defer teardown()
	in := newBlockingReader() // never fed -> Read blocks forever

	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx, in, make(chan Size)) }()

	time.Sleep(50 * time.Millisecond)
	// Simulate all sessions disconnecting.
	m.onSessionEnd(0)

	select {
	case <-done:
		// returned promptly
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after all-sessions-disconnect while stdin idle")
	}
}
