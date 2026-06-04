# terminal-relay — Plan 2: Signaling server + WebRTC P2P spike

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `tr-signal` — a tiny WebSocket server that brokers the WebRTC handshake (SDP offer/answer) between a browser and an agent matched by `{owner_id, machine_id}` and **carries no terminal data** — plus a headless proof that two `pion` peers establish a *direct* P2P DataChannel **through** it (strict P2P, STUN-only, no TURN) with the Plan-1 Noise `KK` channel running inside.

**Architecture:** The data plane is peer-to-peer. The server only relays SDP (offer→agent, answer→browser), tagged by a session id so one agent connection can serve several browsers. We use **non-trickle ICE** (each peer gathers its candidates, embeds them in the SDP, then sends one blob) — simplest correct path; trickle is a later optimization. Once the DataChannel is open, the server is out of the loop entirely; Noise `KK` (Plan 1) runs inside the DataChannel and is what actually authenticates the peers (DTLS alone could be MITM'd via the untrusted server, see spec).

**Tech Stack:** Go (same module, `github.com/srcful/terminal-relay/go`), `github.com/pion/webrtc/v4` for the P2P DataChannel, `github.com/coder/websocket` for signaling, plus the Plan-1 `internal/noise` package.

**Implementer note (pion/coder API):** As in Plan 1, follow this plan's design and
the Plan-1 invariants exactly, but you MAY adapt pion/webrtc and coder/websocket
**method names / signatures** to whatever the installed versions expose if a call
fails to compile — the WebRTC/Noise *semantics* must not change. Note every such
adaptation. Run every command for real; never fake green; if the P2P spike cannot
connect after honest iteration, STOP and report the exact pion connection-state
transitions and error.

---

## File structure

```
go/
  cmd/tr-signal/main.go               # boot the signaling server
  internal/signal/
    protocol.go      protocol_test.go # SignalMsg JSON + helpers
    server.go        server_test.go   # WSS broker: agent register + attach routing
    spike_test.go                     # two pion peers THROUGH tr-signal: DataChannel + Noise + echo
  internal/peer/
    peer.go          peer_test.go     # pion PeerConnection (strict P2P) + DataChannel MsgConn
    handshake.go                      # Noise KK over a discrete-message conn (reuses internal/noise)
```

---

## Task 1: Add pion/webrtc and coder/websocket

**Files:** Modify `go/go.mod`, `go/go.sum`

- [ ] **Step 1: Add deps**

Run:
```bash
cd go && go get github.com/pion/webrtc/v4@latest && go get github.com/coder/websocket@latest && go mod tidy
```
Expected: both appear in `go.mod`. (If `go mod tidy` drops one because nothing imports it yet, that is fine — the task that first imports it re-adds it; include `go.mod`/`go.sum` in that task's commit.)

- [ ] **Step 2: Commit**

```bash
git add go/go.mod go/go.sum
git commit -m "chore(go): add pion/webrtc + coder/websocket"
```

---

## Task 2: Signaling protocol

**Files:** Create `go/internal/signal/protocol.go`, `go/internal/signal/protocol_test.go`

- [ ] **Step 1: Write the failing test**

```go
// go/internal/signal/protocol_test.go
package signal

import "testing"

func TestSignalMsgRoundTrip(t *testing.T) {
	in := SignalMsg{Type: TypeOffer, Session: "s1", SDP: "v=0..."}
	data, err := in.encode()
	if err != nil {
		t.Fatal(err)
	}
	out, err := decodeSignal(data)
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != TypeOffer || out.Session != "s1" || out.SDP != "v=0..." {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := decodeSignal([]byte("not json")); err == nil {
		t.Fatal("expected decode error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/signal/ -run TestSignalMsg -v`
Expected: FAIL — `undefined: SignalMsg`.

- [ ] **Step 3: Write `protocol.go`**

```go
// go/internal/signal/protocol.go
package signal

import "encoding/json"

// Message types on the signaling channel. SDP carries candidates (non-trickle).
const (
	TypeReady  = "ready"  // server -> agent: registered
	TypeAttach = "attach" // server -> agent: a browser wants you; session id attached
	TypeOffer  = "offer"  // browser -> server -> agent (tagged with session)
	TypeAnswer = "answer" // agent -> server -> browser
	TypeError  = "error"  // server -> peer: e.g. machine offline
	TypeClose  = "close"  // either way: session ended
)

// SignalMsg is the only thing that crosses the signaling WSS. It never contains
// terminal data — only WebRTC SDP and routing. Session is set on agent-facing
// messages so one agent connection can serve multiple browser sessions.
type SignalMsg struct {
	Type    string `json:"type"`
	Session string `json:"session,omitempty"`
	SDP     string `json:"sdp,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

func (m SignalMsg) encode() ([]byte, error) { return json.Marshal(m) }

func decodeSignal(b []byte) (SignalMsg, error) {
	var m SignalMsg
	err := json.Unmarshal(b, &m)
	return m, err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/signal/ -run "TestSignalMsg|TestDecode" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/signal/protocol.go go/internal/signal/protocol_test.go
git commit -m "feat(signal): signaling message protocol"
```

---

## Task 3: Signaling server (broker)

**Files:** Create `go/internal/signal/server.go`, `go/internal/signal/server_test.go`

- [ ] **Step 1: Write the failing test (signaling routing with dummies, no WebRTC yet)**

```go
// go/internal/signal/server_test.go
package signal

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func wsURL(base, path string, q map[string]string) string {
	u := "ws" + base[len("http"):] + path
	sep := "?"
	for k, v := range q {
		u += sep + k + "=" + v
		sep = "&"
	}
	return u
}

func dialJSON(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.Dial(context.Background(), url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	return c
}

func writeMsg(t *testing.T, c *websocket.Conn, m SignalMsg) {
	t.Helper()
	data, _ := m.encode()
	if err := c.Write(context.Background(), websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
}

func readMsg(t *testing.T, c *websocket.Conn) SignalMsg {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	m, err := decodeSignal(data)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestOfferReachesAgentAnswerReachesBrowser(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	agent := dialJSON(t, wsURL(srv.URL, "/agent/signal", map[string]string{"owner_id": "o", "machine_id": "m"}))
	if ready := readMsg(t, agent); ready.Type != TypeReady {
		t.Fatalf("expected ready, got %q", ready.Type)
	}

	browser := dialJSON(t, wsURL(srv.URL, "/attach", map[string]string{"owner_id": "o", "machine_id": "m"}))

	// Agent is notified of the attach with a session id.
	attach := readMsg(t, agent)
	if attach.Type != TypeAttach || attach.Session == "" {
		t.Fatalf("expected attach with session, got %+v", attach)
	}

	// Browser sends an offer; server tags it with the session toward the agent.
	writeMsg(t, browser, SignalMsg{Type: TypeOffer, SDP: "OFFER-SDP"})
	gotOffer := readMsg(t, agent)
	if gotOffer.Type != TypeOffer || gotOffer.SDP != "OFFER-SDP" || gotOffer.Session != attach.Session {
		t.Fatalf("agent got wrong offer: %+v", gotOffer)
	}

	// Agent answers (tagged with session); browser receives it untagged.
	writeMsg(t, agent, SignalMsg{Type: TypeAnswer, Session: attach.Session, SDP: "ANSWER-SDP"})
	gotAnswer := readMsg(t, browser)
	if gotAnswer.Type != TypeAnswer || gotAnswer.SDP != "ANSWER-SDP" {
		t.Fatalf("browser got wrong answer: %+v", gotAnswer)
	}
}

func TestAttachOfflineMachineGetsError(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	browser := dialJSON(t, wsURL(srv.URL, "/attach", map[string]string{"owner_id": "x", "machine_id": "y"}))
	m := readMsg(t, browser)
	if m.Type != TypeError {
		t.Fatalf("expected error for offline machine, got %+v", m)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/signal/ -run "TestOffer|TestAttachOffline" -v`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write `server.go`**

```go
// go/internal/signal/server.go
package signal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

// Server brokers SDP between agents and browsers. It never carries terminal
// data — only SignalMsg (SDP + routing). Once a DataChannel is up P2P, the two
// signaling sockets for that session are no longer needed.
type Server struct {
	mu       sync.Mutex
	agents   map[string]*agentConn   // owner|machine -> agent
	sessions map[string]*browserConn // session id -> browser
}

type agentConn struct {
	out chan SignalMsg
}

type browserConn struct {
	out chan SignalMsg
}

func New() *Server {
	return &Server{agents: map[string]*agentConn{}, sessions: map[string]*browserConn{}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/signal", s.handleAgent)
	mux.HandleFunc("/attach", s.handleAttach)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func key(owner, machine string) string { return owner + "|" + machine }

func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	owner := r.URL.Query().Get("owner_id")
	machine := r.URL.Query().Get("machine_id")
	if owner == "" || machine == "" {
		http.Error(w, "missing owner_id/machine_id", http.StatusBadRequest)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ac := &agentConn{out: make(chan SignalMsg, 32)}
	k := key(owner, machine)
	s.mu.Lock()
	s.agents[k] = ac
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.agents[k] == ac {
			delete(s.agents, k)
		}
		s.mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Reader: agent -> server (answers); route to the browser by session.
	go func() {
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				cancel()
				return
			}
			m, err := decodeSignal(data)
			if err != nil {
				continue
			}
			if m.Type == TypeAnswer {
				s.mu.Lock()
				bc := s.sessions[m.Session]
				s.mu.Unlock()
				if bc != nil {
					bc.out <- SignalMsg{Type: TypeAnswer, SDP: m.SDP}
				}
			}
		}
	}()

	// Writer: drain ac.out to the agent.
	send(ctx, c, ac.out, SignalMsg{Type: TypeReady})
}

func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	owner := r.URL.Query().Get("owner_id")
	machine := r.URL.Query().Get("machine_id")
	if owner == "" || machine == "" {
		http.Error(w, "missing owner_id/machine_id", http.StatusBadRequest)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	s.mu.Lock()
	ac := s.agents[key(owner, machine)]
	s.mu.Unlock()
	if ac == nil {
		data, _ := SignalMsg{Type: TypeError, Reason: "machine offline"}.encode()
		_ = c.Write(ctx, websocket.MessageText, data)
		c.Close(websocket.StatusGoingAway, "machine offline")
		return
	}

	sess := newSessionID()
	bc := &browserConn{out: make(chan SignalMsg, 32)}
	s.mu.Lock()
	s.sessions[sess] = bc
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, sess)
		s.mu.Unlock()
	}()

	// Notify the agent that a browser wants it.
	ac.out <- SignalMsg{Type: TypeAttach, Session: sess}

	// Reader: browser -> server (offer); forward to the agent tagged with session.
	go func() {
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				cancel()
				return
			}
			m, err := decodeSignal(data)
			if err != nil {
				continue
			}
			if m.Type == TypeOffer {
				ac.out <- SignalMsg{Type: TypeOffer, Session: sess, SDP: m.SDP}
			}
		}
	}()

	// Writer: drain bc.out to the browser.
	send(ctx, c, bc.out, SignalMsg{})
}

// send writes an optional first message, then drains out until ctx is done.
func send(ctx context.Context, c *websocket.Conn, out <-chan SignalMsg, first SignalMsg) {
	if first.Type != "" {
		data, _ := first.encode()
		if err := c.Write(ctx, websocket.MessageText, data); err != nil {
			return
		}
	}
	for {
		select {
		case m := <-out:
			data, _ := m.encode()
			if err := c.Write(ctx, websocket.MessageText, data); err != nil {
				return
			}
		case <-ctx.Done():
			c.Close(websocket.StatusNormalClosure, "")
			return
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/signal/ -run "TestOffer|TestAttachOffline" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/signal/server.go go/internal/signal/server_test.go go/go.mod go/go.sum
git commit -m "feat(signal): SDP broker (agent register + attach routing)"
```

---

## Task 4: Noise-over-message-channel handshake driver

**Files:** Create `go/internal/peer/handshake.go`, and (part of) `go/internal/peer/peer.go` for the `MsgConn` type. Test added in Task 5.

- [ ] **Step 1: Write `peer.go` (the MsgConn type + pion factory)**

```go
// go/internal/peer/peer.go
package peer

import (
	"context"

	"github.com/pion/webrtc/v4"
)

// MsgConn is a reliable, ordered, discrete-message channel — a WebRTC
// DataChannel. Noise handshake/transport messages map 1:1 to channel messages.
type MsgConn interface {
	Send(b []byte) error
	Recv(ctx context.Context) ([]byte, error)
}

// DataChannel adapts a pion DataChannel to MsgConn.
type DataChannel struct {
	dc   *webrtc.DataChannel
	recv chan []byte
}

func wrap(dc *webrtc.DataChannel) *DataChannel {
	d := &DataChannel{dc: dc, recv: make(chan []byte, 64)}
	dc.OnMessage(func(m webrtc.DataChannelMessage) { d.recv <- m.Data })
	return d
}

func (d *DataChannel) Send(b []byte) error { return d.dc.Send(b) }

func (d *DataChannel) Recv(ctx context.Context) ([]byte, error) {
	select {
	case b := <-d.recv:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// strict P2P: STUN only (hole-punch), never TURN. Empty stun => host candidates
// only (fine for localhost tests).
func config(stun []string) webrtc.Configuration {
	if len(stun) == 0 {
		return webrtc.Configuration{}
	}
	return webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: stun}}}
}

// NewOfferer creates a peer that initiates the DataChannel. opened fires when the
// channel is ready to use.
func NewOfferer(stun []string) (*webrtc.PeerConnection, <-chan *DataChannel, error) {
	pc, err := webrtc.NewPeerConnection(config(stun))
	if err != nil {
		return nil, nil, err
	}
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
func NewAnswerer(stun []string) (*webrtc.PeerConnection, <-chan *DataChannel, error) {
	pc, err := webrtc.NewPeerConnection(config(stun))
	if err != nil {
		return nil, nil, err
	}
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
```

- [ ] **Step 2: Write `handshake.go`**

```go
// go/internal/peer/handshake.go
package peer

import (
	"context"

	"github.com/srcful/terminal-relay/go/internal/noise"
)

// RunInitiator runs the Plan-1 Noise_KK initiator over a DataChannel and returns
// the established encrypted session. staticPriv is the local static key; peerPub
// is the pinned remote static key (set at pairing).
func RunInitiator(ctx context.Context, mc MsgConn, staticPriv, peerPub []byte) (*noise.Session, error) {
	hs, err := noise.NewInitiator(staticPriv, peerPub)
	if err != nil {
		return nil, err
	}
	msg0, err := hs.WriteMessage(nil)
	if err != nil {
		return nil, err
	}
	if err := mc.Send(msg0); err != nil {
		return nil, err
	}
	resp, err := mc.Recv(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := hs.ReadMessage(resp); err != nil {
		return nil, err
	}
	return hs.Session(), nil
}

// RunResponder runs the Noise_KK responder over a DataChannel.
func RunResponder(ctx context.Context, mc MsgConn, staticPriv, peerPub []byte) (*noise.Session, error) {
	hs, err := noise.NewResponder(staticPriv, peerPub)
	if err != nil {
		return nil, err
	}
	msg0, err := mc.Recv(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := hs.ReadMessage(msg0); err != nil {
		return nil, err
	}
	resp, err := hs.WriteMessage(nil)
	if err != nil {
		return nil, err
	}
	if err := mc.Send(resp); err != nil {
		return nil, err
	}
	return hs.Session(), nil
}
```

- [ ] **Step 3: Verify it compiles**

Run: `cd go && go build ./internal/peer/`
Expected: builds (no test yet — exercised in Task 5).

- [ ] **Step 4: Commit**

```bash
git add go/internal/peer/peer.go go/internal/peer/handshake.go go/go.mod go/go.sum
git commit -m "feat(peer): pion DataChannel MsgConn + Noise KK handshake driver"
```

---

## Task 5: peer test — two pion peers, direct signaling, DataChannel + Noise + echo

This isolates the WebRTC + Noise glue from the signaling server (signaling done
in-memory).

**Files:** Create `go/internal/peer/peer_test.go`

- [ ] **Step 1: Write the test**

```go
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
```

- [ ] **Step 2: Run the test**

Run: `cd go && go test ./internal/peer/ -run TestPionPeers -v`
Expected: PASS. (This is WebRTC de-risk #1: a real pion↔pion DataChannel over localhost host candidates, with Noise KK inside. If it hangs/fails, debug pion connection state via `pc.OnConnectionStateChange` / `pc.OnICEConnectionStateChange` logging before changing anything else.)

- [ ] **Step 3: Commit**

```bash
git add go/internal/peer/peer_test.go
git commit -m "test(peer): pion P2P DataChannel + Noise KK echo (WebRTC de-risk)"
```

---

## Task 6: Spike — two pion peers THROUGH `tr-signal`

The acceptance: prove the full path — signaling via the real server, then a
direct P2P DataChannel with Noise inside.

**Files:** Create `go/internal/signal/spike_test.go`

- [ ] **Step 1: Write the test**

```go
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
		var pc = (*struct{})(nil) // placeholder; real pc created on offer
		_ = pc
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
```

- [ ] **Step 2: Run the test**

Run: `cd go && go test ./internal/signal/ -run TestSpike -v`
Expected: PASS — the full path works: signaling server brokers SDP, DataChannel goes P2P, Noise runs inside, bytes round-trip. (Remove the unused `pc` placeholder line in `runAgentPeer` if the linter complains — it is only there to make the structure obvious; the real PeerConnection is `ans`.)

- [ ] **Step 3: Run the whole Go suite**

Run: `cd go && go test ./...`
Expected: PASS across `identity`, `noise`, `peer`, `signal`.

- [ ] **Step 4: Commit**

```bash
git add go/internal/signal/spike_test.go
git commit -m "test(signal): full P2P spike through the signaling server"
```

---

## Task 7: `tr-signal` binary + docs

**Files:** Create `go/cmd/tr-signal/main.go`; modify `README.md`

- [ ] **Step 1: Write `main.go`**

```go
// go/cmd/tr-signal/main.go
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/srcful/terminal-relay/go/internal/signal"
)

func main() {
	addr := flag.String("addr", ":8443", "listen address (TLS terminated by the fronting proxy)")
	flag.Parse()

	s := signal.New()
	srv := &http.Server{Addr: *addr, Handler: s.Handler()}
	log.Printf("tr-signal listening on %s (signaling only; no terminal data)", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
```

- [ ] **Step 2: Build + health smoke**

Run:
```bash
cd go && go build -o /tmp/tr-signal ./cmd/tr-signal && /tmp/tr-signal --addr :8443 &
sleep 1
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8443/healthz
kill %1
```
Expected: prints `200`.

- [ ] **Step 3: Append a signaling section to `README.md`**

```markdown
## Signaling server (Plan 2)

`go/cmd/tr-signal` — brokers the WebRTC handshake (SDP offer/answer) between a
browser and an agent matched by `{owner_id, machine_id}`. It carries **no terminal
data**: terminal bytes flow peer-to-peer over a WebRTC DataChannel (strict P2P,
STUN-only, no TURN), with the Plan-1 Noise channel running inside. Once the
DataChannel is up, the server is out of the loop.

    cd go && go run ./cmd/tr-signal --addr :8443

Endpoints: `/agent/signal`, `/attach` (both WSS), `/healthz`.
```

- [ ] **Step 4: Commit**

```bash
git add go/cmd/tr-signal/main.go README.md
git commit -m "feat(signal): tr-signal binary + docs"
```

---

## Self-review (completed during planning)

- **Spec coverage:** Implements the spec's component #2 (`tr-signal`, signaling
  only, no data) and the spec's testing-strategy de-risk #2 ("prove a pion↔pion
  DataChannel through the signaling server, strict P2P STUN-only, Noise inside").
  Roadmap Plan 2 acceptance (two pion peers signal through `tr-signal`, open a P2P
  DataChannel, Noise round-trips) is met by `TestSpikeFullPathThroughSignalingServer`,
  with `TestPionPeers…` isolating the WebRTC+Noise glue and `TestOffer…`/`TestAttachOffline…`
  covering signaling routing + offline handling.
- **No terminal data through the server:** by construction the server only reads/
  writes `SignalMsg` (SDP + routing); `server_test.go` asserts only SDP strings
  cross it, and the DataChannel (the data path) is created directly between peers.
- **Placeholder scan:** No TBD/TODO; complete code in every code step; exact
  commands + expected output in every run step.
- **Type consistency:** `SignalMsg{Type,Session,SDP,Reason}`, type constants
  (`TypeReady/Attach/Offer/Answer/Error/Close`), `Server.New/Handler`,
  `MsgConn`/`DataChannel.Send/Recv`, `NewOfferer/NewAnswerer/CreateOffer/
  CreateAnswer/AcceptAnswer`, `RunInitiator/RunResponder` (reusing Plan-1
  `noise.NewInitiator/NewResponder/Session`) are used consistently across files
  and tests. Endpoint paths `/agent/signal`, `/attach` match between server and
  tests.
- **Strict P2P / no TURN:** `config()` only ever adds STUN ICE servers; TURN is
  never configured. Tests use host candidates (localhost), so they need no
  network. Real STUN is a deploy-time config value, not a code change.
- **Deferred:** trickle ICE (non-trickle used for simplicity — a latency
  optimization for later); pairing, PTY/tmux, and the real browser are Plans 3–4.
```
