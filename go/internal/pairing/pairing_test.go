// go/internal/pairing/pairing_test.go
package pairing

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/sas"
)

func TestPairingExchangesAndPinsKeys(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	token := NewToken()
	_, ownerPub, _ := noise.GenerateStatic() // client owner key
	_, hostPub, _ := noise.GenerateStatic()  // agent host key

	clientMC, agentMC := peer.Pipe()
	info := AgentInfo{HostPubHex: hex.EncodeToString(hostPub), MachineID: "m123", Name: "box"}

	type respResult struct{ owner, binding []byte }
	gotResp := make(chan respResult, 1)
	go func() {
		op, b, err := RunResponder(ctx, agentMC, token, info)
		if err != nil {
			return
		}
		gotResp <- respResult{owner: op, binding: b}
	}()

	got, initBinding, err := RunInitiator(ctx, clientMC, token, ownerPub)
	if err != nil {
		t.Fatalf("initiator: %v", err)
	}
	if got.HostPubHex != info.HostPubHex || got.MachineID != "m123" || got.Name != "box" {
		t.Fatalf("client got wrong agent info: %+v", got)
	}
	select {
	case r := <-gotResp:
		if hex.EncodeToString(r.owner) != hex.EncodeToString(ownerPub) {
			t.Fatal("agent pinned the wrong owner key")
		}
		// No MITM => both ends derive the same channel binding => same safety number.
		if sas.FromBinding(initBinding) != sas.FromBinding(r.binding) {
			t.Fatal("safety numbers differ; channel bindings must match without a MITM")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent never received owner key")
	}
}

func TestPairingFailsWithWrongToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	_, ownerPub, _ := noise.GenerateStatic()
	_, hostPub, _ := noise.GenerateStatic()
	clientMC, agentMC := peer.Pipe()
	info := AgentInfo{HostPubHex: hex.EncodeToString(hostPub), MachineID: "m", Name: "n"}

	go func() { _, _, _ = RunResponder(ctx, agentMC, NewToken(), info) }() // different token

	if _, _, err := RunInitiator(ctx, clientMC, NewToken(), ownerPub); err == nil {
		t.Fatal("expected pairing to fail with mismatched tokens")
	}
}
