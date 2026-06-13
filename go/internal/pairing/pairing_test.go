// go/internal/pairing/pairing_test.go
package pairing

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/sas"
)

// testWallet mints a real prf-rooted wallet for pairing tests. The prf seed is
// derived from b so each call with a distinct byte yields a distinct wallet.
func testWallet(t *testing.T, b byte) *identity.Wallet {
	t.Helper()
	prf := make([]byte, 32)
	for i := range prf {
		prf[i] = b ^ byte(i)
	}
	w, err := identity.DeriveWallet(prf)
	if err != nil {
		t.Fatalf("DeriveWallet: %v", err)
	}
	return w
}

func TestPairingExchangesAndPinsKeys(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	token := NewToken()
	wallet := testWallet(t, 0x11)           // client wallet
	_, hostPub, _ := noise.GenerateStatic() // agent host key

	clientMC, agentMC := peer.Pipe()
	info := AgentInfo{HostPubHex: hex.EncodeToString(hostPub), MachineID: "m123", Name: "box"}

	type respResult struct {
		wallet  string
		binding []byte
	}
	gotResp := make(chan respResult, 1)
	go func() {
		wal, b, err := RunResponder(ctx, agentMC, token, info)
		if err != nil {
			return
		}
		gotResp <- respResult{wallet: wal, binding: b}
	}()

	got, initBinding, err := RunInitiator(ctx, clientMC, token, wallet)
	if err != nil {
		t.Fatalf("initiator: %v", err)
	}
	if got.HostPubHex != info.HostPubHex || got.MachineID != "m123" || got.Name != "box" {
		t.Fatalf("client got wrong agent info: %+v", got)
	}
	select {
	case r := <-gotResp:
		// The responder returns the base58 wallet from the claim, proven by auth.
		if r.wallet != wallet.Address {
			t.Fatalf("agent pinned the wrong wallet: got %s want %s", r.wallet, wallet.Address)
		}
		// No MITM => both ends derive the same channel binding => same safety number.
		if sas.FromBinding(initBinding) != sas.FromBinding(r.binding) {
			t.Fatal("safety numbers differ; channel bindings must match without a MITM")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent never received wallet claim")
	}
}

func TestPairingFailsWithWrongToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	wallet := testWallet(t, 0x22)
	_, hostPub, _ := noise.GenerateStatic()
	clientMC, agentMC := peer.Pipe()
	info := AgentInfo{HostPubHex: hex.EncodeToString(hostPub), MachineID: "m", Name: "n"}

	go func() { _, _, _ = RunResponder(ctx, agentMC, NewToken(), info) }() // different token

	if _, _, err := RunInitiator(ctx, clientMC, NewToken(), wallet); err == nil {
		t.Fatal("expected pairing to fail with mismatched tokens")
	}
}

// TestPairingRejectsBadWalletAuth proves the agent pins a wallet ONLY when the
// initiator can sign the channel binding with that wallet's key. We drive the
// Noise handshake manually so we can ship a msg3 signature over the WRONG
// challenge — exactly what a MITM (who relayed msg1's claim but cannot sign)
// would be forced to do.
func TestPairingRejectsBadWalletAuth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	token := NewToken()
	wallet := testWallet(t, 0x33)
	_, hostPub, _ := noise.GenerateStatic()
	clientMC, agentMC := peer.Pipe()
	info := AgentInfo{HostPubHex: hex.EncodeToString(hostPub), MachineID: "m", Name: "n"}

	gotErr := make(chan error, 1)
	go func() {
		_, _, err := RunResponder(ctx, agentMC, token, info)
		gotErr <- err
	}()

	// Hand-rolled initiator that signs a DIFFERENT challenge for msg3.
	hs, err := newHandshake(true, token)
	if err != nil {
		t.Fatal(err)
	}
	claim, _ := json.Marshal(PairClaim{Wallet: wallet.Address})
	msg1, _, _, err := hs.WriteMessage(nil, claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := clientMC.Send(msg1); err != nil {
		t.Fatal(err)
	}
	msg2, err := clientMC.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := hs.ReadMessage(nil, msg2); err != nil {
		t.Fatal(err)
	}
	// Sign the wrong challenge -> auth must fail on the responder.
	if err := clientMC.Send(wallet.SignAuth([]byte("not the binding"))); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-gotErr:
		if err == nil {
			t.Fatal("responder accepted a wallet that did not sign the binding")
		}
		if !strings.Contains(err.Error(), "wallet auth failed") {
			t.Fatalf("expected wallet auth failure, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("responder never returned")
	}
}
