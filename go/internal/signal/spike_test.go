// go/internal/signal/spike_test.go
package signal

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// runAgentPeer connects to the signaling server as an agent, answers the offer,
// and runs a Noise responder + echo over the resulting DataChannel.
func runAgentPeer(t *testing.T, baseURL, owner, machine string, staticPriv, browserPub []byte) {
	t.Helper()
	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, wsURL(baseURL, "/agent/signal", map[string]string{"owner_id": owner, "machine_id": machine}), nil)
	if err != nil {
		t.Fatalf("agent signal dial: %v", err)
	}
	go func() {
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			m, err := decodeSignal(data)
			if err != nil {
				continue
			}
			switch m.Type {
			case TypeReady, TypeAttach:
				// nothing to do until the offer arrives
			case TypeOffer:
				ans, ansOpened, err := peer.NewAnswerer(nil)
				if err != nil {
					return
				}
				answerSDP, err := peer.CreateAnswer(ans, m.SDP)
				if err != nil {
					return
				}
				reply, _ := SignalMsg{Type: TypeAnswer, Session: m.Session, SDP: answerSDP}.encode()
				if err := c.Write(ctx, websocket.MessageText, reply); err != nil {
					return
				}
				go func() {
					dc := <-ansOpened
					sess, err := peer.RunResponder(ctx, dc, staticPriv, browserPub)
					if err != nil {
						return
					}
					for {
						ct, err := dc.Recv(ctx)
						if err != nil {
							return
						}
						pt, err := sess.Decrypt(ct)
						if err != nil {
							return
						}
						out, _ := sess.Encrypt(pt)
						_ = dc.Send(out)
					}
				}()
			}
		}
	}()
}

func TestSpikeFullPathThroughSignalingServer(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	agentPriv, agentPub, _ := noise.GenerateStatic()
	browserPriv, browserPub, _ := noise.GenerateStatic()

	runAgentPeer(t, srv.URL, "o", "m", agentPriv, browserPub)
	// give the agent a moment to register its control socket
	time.Sleep(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Browser: connect, create offer, send it, await answer, open DataChannel.
	bc, _, err := websocket.Dial(ctx, wsURL(srv.URL, "/attach", map[string]string{"owner_id": "o", "machine_id": "m"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	off, offOpened, err := peer.NewOfferer(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer off.Close()

	offerSDP, err := peer.CreateOffer(off)
	if err != nil {
		t.Fatal(err)
	}
	offerMsg, _ := SignalMsg{Type: TypeOffer, SDP: offerSDP}.encode()
	if err := bc.Write(ctx, websocket.MessageText, offerMsg); err != nil {
		t.Fatal(err)
	}

	// Await the answer from the server.
	_, data, err := bc.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ans, err := decodeSignal(data)
	if err != nil || ans.Type != TypeAnswer {
		t.Fatalf("expected answer, got %+v (%v)", ans, err)
	}
	if err := peer.AcceptAnswer(off, ans.SDP); err != nil {
		t.Fatal(err)
	}

	// DataChannel opens P2P; run Noise initiator + round-trip.
	var dc *peer.DataChannel
	select {
	case dc = <-offOpened:
	case <-ctx.Done():
		t.Fatal("DataChannel never opened through the signaling server")
	}
	sess, err := peer.RunInitiator(ctx, dc, browserPriv, agentPub)
	if err != nil {
		t.Fatalf("initiator handshake: %v", err)
	}
	ct, _ := sess.Encrypt([]byte("p2p via signaling"))
	if err := dc.Send(ct); err != nil {
		t.Fatal(err)
	}
	echo, err := dc.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := sess.Decrypt(echo)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "p2p via signaling" {
		t.Fatalf("echo mismatch: %q", pt)
	}
}
