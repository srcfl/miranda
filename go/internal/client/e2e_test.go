// go/internal/client/e2e_test.go
package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/identity"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

// provisionOwnerForTest pins the owner the way pairing does: with a signed
// registration authorization when the identity is rooted, so the relay's
// owner-auth check is exercised. Plain PinOwner (no auth) only ever worked for
// legacy identities via the relay's base58 bypass — on a machine with a working
// keychain the created identity is rooted and a bare pin gets 401.
func provisionOwnerForTest(t *testing.T, agentDir string, acfg *agent.Config, id *Identity) {
	t.Helper()
	auth := ""
	if id.HasRootedIdentity() {
		signer, err := id.Signer()
		if err != nil {
			t.Fatal(err)
		}
		commitment, err := acfg.RegistrationCommitment()
		if err != nil {
			t.Fatal(err)
		}
		auth = base64.StdEncoding.EncodeToString(signer.SignAuth(identity.RegistrationChallenge(acfg.MachineID, commitment)))
	}
	if err := agent.ProvisionOwner(agentDir, id.OwnerID, "", auth); err != nil {
		t.Fatal(err)
	}
}

func TestEndToEndTrClientDrivesRealShell(t *testing.T) {
	t.Setenv("MIR_TEST_KEYCHAIN_DIR", t.TempDir())
	srv := httptest.NewServer(signal.New().Handler())
	defer srv.Close()

	// Client identity.
	clientDir := t.TempDir()
	id, err := LoadOrCreateIdentity(clientDir)
	if err != nil {
		t.Fatal(err)
	}

	// Agent: keystore in its own dir, provision the client owner, run the runtime (sh).
	agentDir := t.TempDir()
	acfg, err := agent.LoadOrInit(agentDir, "e2e-box", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	provisionOwnerForTest(t, agentDir, acfg, id)
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

	in.feed([]byte("echo MIR_CLIENT_OK\n"))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains([]byte(out.String()), []byte("MIR_CLIENT_OK")) {
			return // SUCCESS: tr client -> mir-signal -> real sh over P2P
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("never saw command output; got:\n%s", out.String())
}
