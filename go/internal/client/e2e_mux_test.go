// go/internal/client/e2e_mux_test.go
package client

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

// startAgent spins a real mir-agent (sh) registered to the signaling server and
// trusting the given owner; returns its machine descriptor.
func startAgent(t *testing.T, ctx context.Context, srvURL, name string, id *Identity) Machine {
	t.Helper()
	dir := t.TempDir()
	cfg, err := agent.LoadOrInit(dir, name, srvURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.PinOwner(dir, id.WalletAddress); err != nil {
		t.Fatal(err)
	}
	cfg, _ = agent.LoadOrInit(dir, name, srvURL)
	rt := agent.NewRuntime(cfg, []string{"sh"}, nil)
	go func() { _ = rt.Up(ctx) }()
	return Machine{Name: name, MachineID: cfg.MachineID, HostPubHex: cfg.HostPubHex, SignalURL: srvURL}
}

func TestEndToEndMuxSwitchesBetweenTwoMachines(t *testing.T) {
	srv := httptest.NewServer(signal.New().Handler())
	defer srv.Close()

	clientDir := t.TempDir()
	id, err := LoadOrCreateIdentity(clientDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m0 := startAgent(t, ctx, srv.URL, "box0", id)
	m1 := startAgent(t, ctx, srv.URL, "box1", id)
	time.Sleep(400 * time.Millisecond)

	s0mc, s0sess, c0, err := Attach(ctx, m0, id, nil, false)
	if err != nil {
		t.Fatalf("attach box0: %v", err)
	}
	defer c0()
	s1mc, s1sess, c1, err := Attach(ctx, m1, id, nil, false)
	if err != nil {
		t.Fatalf("attach box1: %v", err)
	}
	defer c1()

	sessions := []*MuxSession{
		{Name: "box0", MC: s0mc, Sess: s0sess},
		{Name: "box1", MC: s1mc, Sess: s1sess},
	}
	out := &syncWriter{}
	in := newBlockingReader()
	mux := NewMux(sessions, out, DefaultPrefix, Size{Cols: 80, Rows: 24})
	go func() { _ = mux.Run(ctx, in, make(chan Size)) }()

	// Focus box0: run a command, see its marker.
	in.feed([]byte("echo MARKER_BOX0\n"))
	waitFor(t, out, "MARKER_BOX0")

	// Switch to box1 (Ctrl-] then '2'): run a command, see its marker.
	in.feed([]byte{DefaultPrefix, '2'})
	in.feed([]byte("echo MARKER_BOX1\n"))
	waitFor(t, out, "MARKER_BOX1")

	// Sanity: both markers are present overall; they came from two different shells.
	if !bytes.Contains([]byte(out.String()), []byte("MARKER_BOX0")) {
		t.Fatal("lost box0 output")
	}
}
