// go/internal/peer/peer_test.go
package peer

import (
	"context"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
)

func TestPionPeersEstablishDataChannelWithNoise(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Agent = answerer = Noise responder; browser = offerer = Noise initiator.
	agentPriv, agentPub, err := noise.GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	browserPriv, browserPub, err := noise.GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}

	off, offOpened, err := NewOfferer(nil) // nil stun => host candidates (localhost)
	if err != nil {
		t.Fatal(err)
	}
	defer off.Close()
	ans, ansOpened, err := NewAnswerer(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ans.Close()

	// In-memory signaling (non-trickle): offer -> answerer, answer -> offerer.
	offerSDP, err := CreateOffer(off)
	if err != nil {
		t.Fatal(err)
	}
	answerSDP, err := CreateAnswer(ans, offerSDP)
	if err != nil {
		t.Fatal(err)
	}
	if err := AcceptAnswer(off, answerSDP); err != nil {
		t.Fatal(err)
	}

	// Wait for both DataChannels to open.
	var browserDC, agentDC *DataChannel
	select {
	case browserDC = <-offOpened:
	case <-ctx.Done():
		t.Fatal("offerer DataChannel never opened (P2P connectivity failed)")
	}
	select {
	case agentDC = <-ansOpened:
	case <-ctx.Done():
		t.Fatal("answerer DataChannel never opened")
	}

	// Agent side: Noise responder + echo loop.
	go func() {
		sess, err := RunResponder(ctx, agentDC, agentPriv, browserPub)
		if err != nil {
			return
		}
		for {
			ct, err := agentDC.Recv(ctx)
			if err != nil {
				return
			}
			pt, err := sess.Decrypt(ct)
			if err != nil {
				return
			}
			reply, _ := sess.Encrypt(pt) // echo back, re-encrypted
			_ = agentDC.Send(reply)
		}
	}()

	// Browser side: Noise initiator, send one encrypted message, expect echo.
	sess, err := RunInitiator(ctx, browserDC, browserPriv, agentPub)
	if err != nil {
		t.Fatalf("initiator handshake failed: %v", err)
	}
	ct, err := sess.Encrypt([]byte("hello over p2p"))
	if err != nil {
		t.Fatal(err)
	}
	if err := browserDC.Send(ct); err != nil {
		t.Fatal(err)
	}
	echo, err := browserDC.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := sess.Decrypt(echo)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "hello over p2p" {
		t.Fatalf("echo mismatch: %q", pt)
	}
}
