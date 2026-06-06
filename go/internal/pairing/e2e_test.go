// go/internal/pairing/e2e_test.go
package pairing_test

import (
	"context"
	"encoding/hex"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/pairing"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

func TestPairThroughSignalingServer(t *testing.T) {
	srv := httptest.NewServer(signal.New().Handler())
	defer srv.Close()

	token := pairing.NewToken()
	code := pairing.EncodeCode(srv.URL, token)

	_, ownerPub, _ := noise.GenerateStatic()
	_, hostPub, _ := noise.GenerateStatic()
	info := pairing.AgentInfo{HostPubHex: hex.EncodeToString(hostPub), MachineID: "mid42", Name: "box"}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	// Agent side (responder).
	gotOwner := make(chan []byte, 1)
	go func() {
		mc, closeConn, err := pairing.DialPair(ctx, srv.URL, pairing.RoomID(token))
		if err != nil {
			return
		}
		defer closeConn()
		op, _, err := pairing.RunResponder(ctx, mc, token, info)
		if err == nil {
			gotOwner <- op
		}
	}()
	time.Sleep(150 * time.Millisecond) // let the agent register the room first

	// Client side (initiator), decoding the code as `trm pair` would.
	signalURL, tok, err := pairing.DecodeCode(code)
	if err != nil {
		t.Fatal(err)
	}
	mc, closeConn, err := pairing.DialPair(ctx, signalURL, pairing.RoomID(tok))
	if err != nil {
		t.Fatal(err)
	}
	defer closeConn()
	got, _, err := pairing.RunInitiator(ctx, mc, tok, ownerPub)
	if err != nil {
		t.Fatalf("client pair: %v", err)
	}
	if got.MachineID != "mid42" || got.HostPubHex != info.HostPubHex {
		t.Fatalf("client pinned wrong info: %+v", got)
	}
	select {
	case op := <-gotOwner:
		if hex.EncodeToString(op) != hex.EncodeToString(ownerPub) {
			t.Fatal("agent pinned wrong owner")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent never paired")
	}
}
