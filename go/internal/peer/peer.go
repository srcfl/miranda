// go/internal/peer/peer.go
package peer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/pion/webrtc/v4"
)

// attachICEDebug logs gathered ICE candidates and connection-state changes to
// stderr when TR_ICE_DEBUG is set. Useful to confirm srflx (NAT-traversal)
// candidates are gathered and which path ICE selects.
func attachICEDebug(pc *webrtc.PeerConnection) {
	if os.Getenv("TR_ICE_DEBUG") == "" {
		return
	}
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			fmt.Fprintf(os.Stderr, "[ice] local candidate type=%s %s:%d\n", c.Typ, c.Address, c.Port)
		}
	})
	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		fmt.Fprintf(os.Stderr, "[ice] connection state=%s\n", s)
	})
}

// ErrDataChannelClosed is returned by Recv when the DataChannel is closed
// (locally or by the remote peer) before a message arrives.
var ErrDataChannelClosed = errors.New("peer: data channel closed")

// MsgConn is a reliable, ordered, discrete-message channel — a WebRTC
// DataChannel. Noise handshake/transport messages map 1:1 to channel messages.
type MsgConn interface {
	Send(b []byte) error
	Recv(ctx context.Context) ([]byte, error)
}

// DataChannel adapts a pion DataChannel to MsgConn.
type DataChannel struct {
	dc        *webrtc.DataChannel
	recv      chan []byte
	closed    chan struct{} // closed when the channel is closed (local or remote)
	closeOnce sync.Once
}

func wrap(dc *webrtc.DataChannel) *DataChannel {
	d := &DataChannel{dc: dc, recv: make(chan []byte, 64), closed: make(chan struct{})}
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		select {
		case d.recv <- m.Data:
		case <-d.closed:
		}
	})
	// On remote close (or error), signal Recv so it unblocks instead of parking
	// forever. Without this a remote PeerConnection/DataChannel close would leave
	// any Recv blocked, leaking the goroutine and everything it captured.
	dc.OnClose(func() { d.signalClosed() })
	dc.OnError(func(error) { d.signalClosed() })
	return d
}

func (d *DataChannel) signalClosed() {
	d.closeOnce.Do(func() { close(d.closed) })
}

func (d *DataChannel) Send(b []byte) error { return d.dc.Send(b) }

func (d *DataChannel) Recv(ctx context.Context) ([]byte, error) {
	select {
	case b := <-d.recv:
		return b, nil
	case <-d.closed:
		return nil, ErrDataChannelClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// strict P2P: STUN only (hole-punch), never TURN. Empty stun => host candidates
// only (fine for localhost tests).
// ICEServer is a STUN or TURN server. TURN servers carry Username/Credential;
// STUN servers leave them empty. Empty list = host candidates only (local).
type ICEServer struct {
	URLs       []string
	Username   string
	Credential string
}

func config(servers []ICEServer) webrtc.Configuration {
	if len(servers) == 0 {
		return webrtc.Configuration{}
	}
	ws := make([]webrtc.ICEServer, 0, len(servers))
	for _, s := range servers {
		ice := webrtc.ICEServer{URLs: s.URLs}
		if s.Username != "" || s.Credential != "" {
			ice.Username = s.Username
			ice.Credential = s.Credential
		}
		ws = append(ws, ice)
	}
	return webrtc.Configuration{ICEServers: ws}
}

// NewOfferer creates a peer that initiates the DataChannel. opened fires when the
// channel is ready to use.
func NewOfferer(servers []ICEServer) (*webrtc.PeerConnection, <-chan *DataChannel, error) {
	pc, err := webrtc.NewPeerConnection(config(servers))
	if err != nil {
		return nil, nil, err
	}
	attachICEDebug(pc)
	dc, err := pc.CreateDataChannel("terminal", nil)
	if err != nil {
		_ = pc.Close()
		return nil, nil, err
	}
	opened := make(chan *DataChannel, 1)
	w := wrap(dc)
	dc.OnOpen(func() { opened <- w })
	return pc, opened, nil
}

// NewAnswerer creates a peer that accepts the offered DataChannel.
func NewAnswerer(servers []ICEServer) (*webrtc.PeerConnection, <-chan *DataChannel, error) {
	pc, err := webrtc.NewPeerConnection(config(servers))
	if err != nil {
		return nil, nil, err
	}
	attachICEDebug(pc)
	opened := make(chan *DataChannel, 1)
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		w := wrap(dc)
		dc.OnOpen(func() { opened <- w })
	})
	return pc, opened, nil
}

// CreateOffer / CreateAnswer / AcceptAnswer use non-trickle ICE: gather all
// candidates, then return the SDP with them embedded.
func CreateOffer(pc *webrtc.PeerConnection) (string, error) {
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return "", err
	}
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return "", err
	}
	<-done
	return pc.LocalDescription().SDP, nil
}

func CreateAnswer(pc *webrtc.PeerConnection, offerSDP string) (string, error) {
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		return "", err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		return "", err
	}
	<-done
	return pc.LocalDescription().SDP, nil
}

func AcceptAnswer(pc *webrtc.PeerConnection, answerSDP string) error {
	return pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answerSDP})
}
