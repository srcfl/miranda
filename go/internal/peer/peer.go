// go/internal/peer/peer.go
package peer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// Pion's ICE liveness defaults (disconnected 5s / failed 25s / keepalive 2s)
// are tuned for media, not a terminal: after a network flip an attach sat
// frozen for up to half a minute before `failed` finally tore it down. A
// terminal session should notice a dead path in ~1s and give up on it in ~10s.
// Both ends share these: the client redials sooner, and the agent's per-attach
// cleanup rides the same faster `failed` (tmux keeps the shell either way).
//
// iceDisconnectedTimeout is two missed keepalives. It was 2s (four missed) until
// netsim measured where resume actually goes: detection was 3.23s of a 4.08s
// resume, and the redial it gates was already sub-second, so the beta's "under
// 3s" could only come out of detection. See netsim/results/results.md.
const (
	iceDisconnectedTimeout = time.Second
	iceFailedTimeout       = 10 * time.Second
	iceKeepAlive           = 500 * time.Millisecond
)

func newPeerConnection(servers []ICEServer) (*webrtc.PeerConnection, error) {
	se := webrtc.SettingEngine{}
	se.SetICETimeouts(iceDisconnectedTimeout, iceFailedTimeout, iceKeepAlive)
	return webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(config(servers))
}

// LinkGrace is how long an established attach may sit in `disconnected` before
// LinkWatch tears it down for a prompt redial. With iceDisconnectedTimeout this
// puts client reaction at ~1.5s after a flip instead of the failed timeout.
//
// The grace is the whole cost of being wrong: a blip that heals inside it is
// free, and one that heals just outside it buys a redial (~0.8s direct, ~1.2s
// relayed) instead of a frozen terminal. That trade is why it is half a second
// and no longer the full one — the redial is cheap, so waiting is the expensive
// option.
const LinkGrace = 500 * time.Millisecond

// LinkWatch enforces the early-reaction policy on one PeerConnection: feed it
// every connection-state change. `disconnected` arms a grace timer; expiry runs
// kill (normally pc.Close — idempotent, closes the DataChannel and unblocks
// Recv so the reconnect loop redials). `connected` inside the window disarms
// (a blip that healed costs nothing). failed/closed disarm too: that teardown
// belongs to ICESessionDead's handler.
type LinkWatch struct {
	grace time.Duration
	kill  func()
	mu    sync.Mutex
	timer *time.Timer
}

func NewLinkWatch(grace time.Duration, kill func()) *LinkWatch {
	return &LinkWatch{grace: grace, kill: kill}
}

func (w *LinkWatch) State(s webrtc.PeerConnectionState) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s == webrtc.PeerConnectionStateDisconnected {
		if w.timer == nil {
			// Fire outside the lock: kill usually closes the pc, and that close
			// re-enters State with `closed`.
			w.timer = time.AfterFunc(w.grace, func() {
				w.mu.Lock()
				w.timer = nil
				w.mu.Unlock()
				w.kill()
			})
		}
		return
	}
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}

// Stop disarms any pending grace timer (session teardown).
func (w *LinkWatch) Stop() { w.State(webrtc.PeerConnectionStateClosed) }

// attachICEDebug logs gathered ICE candidates and connection-state changes to
// stderr when MIR_ICE_DEBUG is set. Useful to confirm srflx (NAT-traversal)
// candidates are gathered and which path ICE selects.
func attachICEDebug(pc *webrtc.PeerConnection) {
	if os.Getenv("MIR_ICE_DEBUG") == "" {
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
		// Copy: Pion may reuse the buffer. Never block the SCTP read loop —
		// a full recv chan drops the frame instead of parking OnMessage.
		d.offerRecv(append([]byte(nil), m.Data...))
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

func (d *DataChannel) offerRecv(buf []byte) {
	select {
	case d.recv <- buf:
	case <-d.closed:
	default:
	}
}

// ICESessionDead reports whether the PeerConnection state should tear down an
// established attach. disconnected is recoverable (Wi-Fi/cellular flip); failed
// and closed are not.
func ICESessionDead(s webrtc.PeerConnectionState) bool {
	switch s {
	case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
		return true
	default:
		return false
	}
}

func waitGather(ctx context.Context, done <-chan struct{}, abort func()) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		if abort != nil {
			abort()
		}
		return ctx.Err()
	}
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
	pc, err := newPeerConnection(servers)
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
	pc, err := newPeerConnection(servers)
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
	return CreateOfferContext(context.Background(), pc)
}

func CreateOfferContext(ctx context.Context, pc *webrtc.PeerConnection) (string, error) {
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return "", err
	}
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return "", err
	}
	if err := waitGather(ctx, done, func() { _ = pc.Close() }); err != nil {
		return "", err
	}
	return pc.LocalDescription().SDP, nil
}

func CreateAnswer(pc *webrtc.PeerConnection, offerSDP string) (string, error) {
	return CreateAnswerContext(context.Background(), pc, offerSDP)
}

func CreateAnswerContext(ctx context.Context, pc *webrtc.PeerConnection, offerSDP string) (string, error) {
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
	if err := waitGather(ctx, done, func() { _ = pc.Close() }); err != nil {
		return "", err
	}
	return pc.LocalDescription().SDP, nil
}

func AcceptAnswer(pc *webrtc.PeerConnection, answerSDP string) error {
	return pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answerSDP})
}
