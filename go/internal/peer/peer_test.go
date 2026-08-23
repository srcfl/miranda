// go/internal/peer/peer_test.go
package peer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
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

func TestICESessionDead(t *testing.T) {
	if ICESessionDead(webrtc.PeerConnectionStateDisconnected) {
		t.Fatal("disconnected must not tear down an established session")
	}
	if ICESessionDead(webrtc.PeerConnectionStateConnected) {
		t.Fatal("connected is not dead")
	}
	if !ICESessionDead(webrtc.PeerConnectionStateFailed) {
		t.Fatal("failed must tear down")
	}
	if !ICESessionDead(webrtc.PeerConnectionStateClosed) {
		t.Fatal("closed must tear down")
	}
}

func TestCreateOfferContextReturnsOnCancel(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := pc.CreateDataChannel("data", nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := CreateOfferContext(ctx, pc)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("CreateOfferContext after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CreateOfferContext hung after ctx cancel")
	}
}

func TestWaitGatherHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	aborted := false
	err := waitGather(ctx, done, func() { aborted = true })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled, got %v", err)
	}
	if !aborted {
		t.Fatal("abort must run when ctx is cancelled")
	}
}

func TestDataChannelOfferRecvDoesNotBlockWhenFull(t *testing.T) {
	d := &DataChannel{recv: make(chan []byte, 64), closed: make(chan struct{})}
	for i := 0; i < 64; i++ {
		d.offerRecv([]byte{byte(i)})
	}
	finished := make(chan struct{})
	go func() {
		d.offerRecv([]byte{255})
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("offerRecv blocked Pion's read loop when the recv buffer was full")
	}
}
