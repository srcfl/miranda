// go/internal/pairing/e2e_test.go
package pairing_test

import (
	"context"
	"encoding/hex"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/pairing"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

func TestPairThroughSignalingServer(t *testing.T) {
	srv := httptest.NewServer(signal.New().Handler())
	defer srv.Close()

	token := pairing.NewToken()
	code := pairing.EncodeCode(srv.URL, token)

	prf := make([]byte, 32)
	for i := range prf {
		prf[i] = byte(i + 1)
	}
	wallet, err := identity.DeriveWallet(prf)
	if err != nil {
		t.Fatal(err)
	}
	_, hostPub, _ := noise.GenerateStatic()
	info := pairing.AgentInfo{HostPubHex: hex.EncodeToString(hostPub), MachineID: "mid42", Name: "box"}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	// Agent side (responder).
	gotWallet := make(chan string, 1)
	go func() {
		mc, closeConn, err := pairing.DialPair(ctx, srv.URL, pairing.RoomID(token))
		if err != nil {
			return
		}
		defer closeConn()
		wal, _, err := pairing.RunResponder(ctx, mc, token, info)
		if err == nil {
			gotWallet <- wal
		}
	}()
	time.Sleep(150 * time.Millisecond) // let the agent register the room first

	// Client side (initiator), decoding the code as `mir pair` would.
	signalURL, tok, err := pairing.DecodeCode(code)
	if err != nil {
		t.Fatal(err)
	}
	mc, closeConn, err := pairing.DialPair(ctx, signalURL, pairing.RoomID(tok))
	if err != nil {
		t.Fatal(err)
	}
	defer closeConn()
	got, _, err := pairing.RunInitiator(ctx, mc, tok, wallet)
	if err != nil {
		t.Fatalf("client pair: %v", err)
	}
	if got.MachineID != "mid42" || got.HostPubHex != info.HostPubHex {
		t.Fatalf("client pinned wrong info: %+v", got)
	}
	select {
	case wal := <-gotWallet:
		if wal != wallet.Address {
			t.Fatalf("agent pinned wrong wallet: got %s want %s", wal, wallet.Address)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent never paired")
	}
}
