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
