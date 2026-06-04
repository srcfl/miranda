// go/internal/client/e2e_test.go
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

func TestEndToEndTrClientDrivesRealShell(t *testing.T) {
	srv := httptest.NewServer(signal.New().Handler())
	defer srv.Close()

	// Client identity.
	clientDir := t.TempDir()
	id, err := LoadOrCreateIdentity(clientDir)
	if err != nil {
		t.Fatal(err)
	}

	// Agent: keystore in its own dir, pin the client owner, run the runtime (sh).
	agentDir := t.TempDir()
	acfg, err := agent.LoadOrInit(agentDir, "e2e-box", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.PinOwner(agentDir, id.OwnerPubHex); err != nil {
		t.Fatal(err)
	}
	acfg, _ = agent.LoadOrInit(agentDir, "e2e-box", srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rt := agent.NewRuntime(acfg, []string{"sh"}, nil)
	go func() { _ = rt.Up(ctx) }()
	time.Sleep(300 * time.Millisecond)

	// Register the machine in the client (as `tr add-machine` would).
	m := Machine{Name: "box", MachineID: acfg.MachineID, HostPubHex: acfg.HostPubHex, SignalURL: srv.URL}

	mc, sess, cleanup, err := Attach(ctx, m, id, nil)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer cleanup()

	// Drive the bridge with scripted I/O (no TTY): feed a command, capture output.
	in := newBlockingReader()
	out := &syncWriter{}
	resizes := make(chan Size, 1)
	go func() { _ = ClientBridge(ctx, in, out, resizes, Size{Cols: 80, Rows: 24}, mc, sess) }()

	in.feed([]byte("echo TR_CLIENT_OK\n"))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains([]byte(out.String()), []byte("TR_CLIENT_OK")) {
			return // SUCCESS: tr client -> tr-signal -> real sh over P2P
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("never saw command output; got:\n%s", out.String())
}
