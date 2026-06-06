// go/internal/signal/server.go
package signal

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// sendTimeout bounds how long the broker will wait to hand a SignalMsg to a
// peer's writer before giving up. One wedged agent/browser must never stall
// another peer's handler. SDP relay is one-shot per session, so a give-up here
// just fails that session fast (the peer re-attaches) rather than blocking
// forever.
const sendTimeout = 2 * time.Second

// flushTimeout bounds the graceful drain of queued messages (e.g. a final
// TypeError "machine offline") before the socket is closed.
const flushTimeout = 2 * time.Second

const (
	maxSignalMessageBytes   = 256 << 10
	defaultMaxAgentSessions = 128
	defaultMaxPairRooms     = 1024
	capacityReason          = "server capacity reached"
)

var (
	errSignalTooLarge = errors.New("signal message too large")
	errAgentGone      = errors.New("agent gone")
	errAgentCapacity  = errors.New(capacityReason)
)

// acceptOpts allows WebSocket connections from any origin. Browsers send an
// Origin header (e.g. https://term.sourceful-labs.net connecting to
// relay.sourceful-labs.net is cross-origin), and coder/websocket rejects
// cross-origin by default. This is safe here: the relay is blind and carries no
// ambient authority (no cookies/sessions), so there is nothing for a cross-site
// request to forge — all authentication lives in the Noise/owner-key layer.
var acceptOpts = &websocket.AcceptOptions{OriginPatterns: []string{"*"}}

// AgentRegistrationSecretHeader carries the agent's local registration proof.
// It authenticates only relay registration replacement for one owner|machine
// slot; terminal data and peer authentication remain end-to-end on the data
// plane.
const AgentRegistrationSecretHeader = "X-TR-Agent-Registration-Secret"

// Server brokers SDP between agents and browsers. It never carries terminal
// data — only SignalMsg (SDP + routing). Once a DataChannel is up P2P, the two
// signaling sockets for that session are no longer needed.
type Server struct {
	mu           sync.Mutex
	agents       map[string]*agentConn   // owner|machine -> live agent
	agentSecrets map[string]string       // owner|machine -> learned registration proof
	sessions     map[string]*browserConn // session id -> browser (global lookup)
	pair         *pairRooms              // roomID -> waiting pairing party (blind bridge)

	// TURN (optional): when both are set, /turn-credentials issues ephemeral
	// creds for this URL. The secret is shared with coturn only — never shipped.
	TURNSecret string
	TURNURL    string

	maxAgentSessions int
	maxPairRooms     int
}

// agentConn is one agent control socket. It owns the set of browser sessions
// bound to it so that when the agent dies — or is replaced by a re-registration —
// those browsers are torn down and told the machine went offline, instead of
// hanging or routing offers to a dead agent.
type agentConn struct {
	out  chan SignalMsg
	done chan struct{} // closed once when the agent is gone/replaced
	once sync.Once

	mu       sync.Mutex
	sessions map[string]*browserConn // session id -> bound browser
}

func newAgentConn() *agentConn {
	return &agentConn{
		out:      make(chan SignalMsg, 32),
		done:     make(chan struct{}),
		sessions: map[string]*browserConn{},
	}
}

// bind attaches a browser session to this agent. Returns false if the agent has
// already been torn down (its socket is gone / it was replaced), in which case
// the caller must treat the machine as offline.
func (ac *agentConn) bind(sess string, bc *browserConn, maxSessions int) error {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	select {
	case <-ac.done:
		return errAgentGone
	default:
	}
	if maxSessions > 0 && len(ac.sessions) >= maxSessions {
		return errAgentCapacity
	}
	ac.sessions[sess] = bc
	return nil
}

func (ac *agentConn) unbind(sess string) {
	ac.mu.Lock()
	delete(ac.sessions, sess)
	ac.mu.Unlock()
}

// teardown marks the agent gone and tells every browser bound to it that the
// machine went offline so it can re-attach. Idempotent.
func (ac *agentConn) teardown() {
	ac.once.Do(func() {
		ac.mu.Lock()
		bound := make([]*browserConn, 0, len(ac.sessions))
		for _, bc := range ac.sessions {
			bound = append(bound, bc)
		}
		ac.sessions = map[string]*browserConn{}
		ac.mu.Unlock()
		close(ac.done)
		for _, bc := range bound {
			bc.fail("machine offline")
		}
	})
}

// browserConn is one attach socket.
type browserConn struct {
	out  chan SignalMsg
	done chan struct{} // closed once to make the writer flush+close gracefully
	once sync.Once
}

func newBrowserConn() *browserConn {
	return &browserConn{out: make(chan SignalMsg, 32), done: make(chan struct{})}
}

// notify hands a message to the browser's writer without blocking forever: it
// gives up on done or after sendTimeout.
func (bc *browserConn) notify(m SignalMsg) {
	t := time.NewTimer(sendTimeout)
	defer t.Stop()
	select {
	case bc.out <- m:
	case <-bc.done:
	case <-t.C:
	}
}

// fail enqueues a final TypeError and signals the writer to flush+close. The
// writer (not the reader) owns the socket close so the queued error is actually
// delivered before the connection goes away.
func (bc *browserConn) fail(reason string) {
	bc.notify(SignalMsg{Type: TypeError, Reason: reason})
	bc.close()
}

// close signals the browser writer to flush and shut down. Idempotent.
func (bc *browserConn) close() { bc.once.Do(func() { close(bc.done) }) }

func New() *Server {
	return &Server{
		agents:           map[string]*agentConn{},
		agentSecrets:     map[string]string{},
		sessions:         map[string]*browserConn{},
		pair:             newPairRooms(),
		maxAgentSessions: defaultMaxAgentSessions,
		maxPairRooms:     defaultMaxPairRooms,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/signal", s.handleAgent)
	mux.HandleFunc("/attach", s.handleAttach)
	mux.HandleFunc("/pair", s.handlePair)
	mux.HandleFunc("/turn-credentials", s.handleTURN)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func key(owner, machine string) string { return owner + "|" + machine }

func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func acceptSignal(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	c, err := websocket.Accept(w, r, acceptOpts)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(maxSignalMessageBytes)
	return c, nil
}

func decodeInboundSignal(data []byte) (SignalMsg, error) {
	if len(data) > maxSignalMessageBytes {
		return SignalMsg{}, errSignalTooLarge
	}
	return decodeSignal(data)
}

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	owner := r.URL.Query().Get("owner_id")
	machine := r.URL.Query().Get("machine_id")
	if owner == "" || machine == "" {
		http.Error(w, "missing owner_id/machine_id", http.StatusBadRequest)
		return
	}
	proof := r.Header.Get(AgentRegistrationSecretHeader)
	k := key(owner, machine)
	s.mu.Lock()
	if !s.agentProofOKLocked(k, proof) {
		s.mu.Unlock()
		http.Error(w, "agent registration proof required", http.StatusUnauthorized)
		return
	}
	s.mu.Unlock()

	c, err := acceptSignal(w, r)
	if err != nil {
		return
	}

	ac := newAgentConn()

	// Register, replacing any previous agent for this key. Re-registration is
	// routine on agent restart; the previous agent — and the browser sessions
	// bound to it — must be torn down so nothing keeps routing offers to the
	// dead one.
	s.mu.Lock()
	if !s.agentProofOKLocked(k, proof) {
		s.mu.Unlock()
		c.Close(websocket.StatusPolicyViolation, "agent registration proof required")
		return
	}
	prev := s.agents[k]
	if proof != "" && s.agentSecrets[k] == "" {
		s.agentSecrets[k] = proof
	}
	s.agents[k] = ac
	s.mu.Unlock()
	if prev != nil {
		prev.teardown()
	}

	// readCtx bounds reads only. Cancelling the read context in coder/websocket
	// closes the socket, so we never cancel it to deliver a message — the writer
	// owns graceful shutdown via ac.done / writeCtx.
	readCtx, cancelRead := context.WithCancel(r.Context())
	defer cancelRead()

	defer func() {
		// Remove from the map only if we are still the live agent (a later
		// re-registration may have already replaced us).
		s.mu.Lock()
		if s.agents[k] == ac {
			delete(s.agents, k)
		}
		s.mu.Unlock()
		ac.teardown()
	}()

	// Reader: agent -> server (answers); route to the browser by session.
	go func() {
		for {
			_, data, err := c.Read(readCtx)
			if err != nil {
				ac.teardown() // agent socket died -> tear down bound browsers
				return
			}
			m, err := decodeInboundSignal(data)
			if err != nil {
				continue
			}
			if m.Type == TypeAnswer {
				s.mu.Lock()
				bc := s.sessions[m.Session]
				s.mu.Unlock()
				if bc != nil {
					bc.notify(SignalMsg{Type: TypeAnswer, SDP: m.SDP})
				}
			}
		}
	}()

	// Writer: drain ac.out to the agent until ac.done (or r.Context()) fires.
	writeUntil(r.Context(), ac.done, c, ac.out, SignalMsg{Type: TypeReady})
}

func (s *Server) agentProofOKLocked(k, proof string) bool {
	expected := s.agentSecrets[k]
	if expected == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(proof), []byte(expected)) == 1
}

func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	owner := r.URL.Query().Get("owner_id")
	machine := r.URL.Query().Get("machine_id")
	if owner == "" || machine == "" {
		http.Error(w, "missing owner_id/machine_id", http.StatusBadRequest)
		return
	}
	c, err := acceptSignal(w, r)
	if err != nil {
		return
	}

	readCtx, cancelRead := context.WithCancel(r.Context())
	defer cancelRead()

	k := key(owner, machine)
	s.mu.Lock()
	ac := s.agents[k]
	s.mu.Unlock()
	if ac == nil {
		writeSignalErrorAndClose(readCtx, c, "machine offline", websocket.StatusGoingAway)
		return
	}

	sess := newSessionID()
	bc := newBrowserConn()

	// Bind this session to the agent so the agent's teardown (death or
	// re-registration) tells us the machine went offline and shuts us down.
	if err := ac.bind(sess, bc, s.maxAgentSessions); err != nil {
		reason := "machine offline"
		code := websocket.StatusGoingAway
		if err == errAgentCapacity {
			reason = capacityReason
			code = websocket.StatusTryAgainLater
		}
		writeSignalErrorAndClose(readCtx, c, reason, code)
		return
	}

	s.mu.Lock()
	s.sessions[sess] = bc
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, sess)
		s.mu.Unlock()
		ac.unbind(sess)
		bc.close()
	}()

	// Notify the agent that a browser wants it. Never a bare blocking send: bound
	// to the agent's done and a timeout so a wedged agent can't hang this attach.
	if !agentSend(ac, bc.done, SignalMsg{Type: TypeAttach, Session: sess}) {
		bc.fail("machine offline")
		// fall through to the writer so the error is actually flushed
	}

	// Reader: browser -> server (offer); forward to the live agent tagged with
	// session.
	go func() {
		for {
			_, data, err := c.Read(readCtx)
			if err != nil {
				bc.close()
				return
			}
			m, err := decodeInboundSignal(data)
			if err != nil {
				continue
			}
			if m.Type == TypeOffer {
				// Re-look-up the live agent for this key on every offer rather
				// than trusting the pointer captured at attach time. On agent
				// re-registration the captured ac is stale; routing the offer to
				// it would split-brain. If the live agent is gone, fail fast.
				s.mu.Lock()
				live := s.agents[k]
				s.mu.Unlock()
				if live == nil {
					bc.fail("machine offline")
					return
				}
				if !agentSend(live, bc.done, SignalMsg{Type: TypeOffer, Session: sess, SDP: m.SDP}) {
					bc.fail("machine offline")
					return
				}
			}
		}
	}()

	// Writer: drain bc.out to the browser until bc.done (or r.Context()) fires.
	writeUntil(r.Context(), bc.done, c, bc.out, SignalMsg{})
}

func writeSignalErrorAndClose(ctx context.Context, c *websocket.Conn, reason string, code websocket.StatusCode) {
	data, _ := SignalMsg{Type: TypeError, Reason: reason}.encode()
	_ = c.Write(ctx, websocket.MessageText, data)
	c.Close(code, reason)
}

// agentSend hands a message to an agent's writer without blocking forever. It
// gives up if the browser is done (browserDone), if the agent is torn down
// (ac.done), or after sendTimeout. Returns false if the message could not be
// delivered (caller should treat the machine as offline).
func agentSend(ac *agentConn, browserDone <-chan struct{}, m SignalMsg) bool {
	t := time.NewTimer(sendTimeout)
	defer t.Stop()
	select {
	case ac.out <- m:
		return true
	case <-ac.done:
		return false
	case <-browserDone:
		return false
	case <-t.C:
		return false
	}
}

// writeUntil writes an optional first message, then drains out to the socket
// until done or reqCtx fires. On shutdown it flushes any already-queued messages
// (e.g. a final TypeError) before closing, so the peer learns why. The writer —
// not the reader — owns the socket close; cancelling the read context in
// coder/websocket closes the socket abruptly and would drop the queued error.
func writeUntil(reqCtx context.Context, done <-chan struct{}, c *websocket.Conn, out <-chan SignalMsg, first SignalMsg) {
	if first.Type != "" {
		data, _ := first.encode()
		if err := c.Write(reqCtx, websocket.MessageText, data); err != nil {
			return
		}
	}
	for {
		select {
		case m := <-out:
			data, _ := m.encode()
			if err := c.Write(reqCtx, websocket.MessageText, data); err != nil {
				return
			}
		case <-done:
			flushAndClose(c, out)
			return
		case <-reqCtx.Done():
			flushAndClose(c, out)
			return
		}
	}
}

// flushAndClose writes any already-queued messages, then closes the socket.
func flushAndClose(c *websocket.Conn, out <-chan SignalMsg) {
	flushCtx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	for {
		select {
		case m := <-out:
			data, _ := m.encode()
			if err := c.Write(flushCtx, websocket.MessageText, data); err != nil {
				c.Close(websocket.StatusNormalClosure, "")
				return
			}
		default:
			c.Close(websocket.StatusNormalClosure, "")
			return
		}
	}
}
