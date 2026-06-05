// go/internal/signal/pair.go
package signal

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// pairWaiter is a connection waiting in a room for its partner.
type pairWaiter struct {
	conn    *websocket.Conn
	partner chan *websocket.Conn
}

type pairRooms struct {
	mu      sync.Mutex
	waiting map[string]*pairWaiter
}

func newPairRooms() *pairRooms { return &pairRooms{waiting: map[string]*pairWaiter{}} }

// rendezvous returns the partner conn (and true if THIS conn should drive the
// bridge). The first arrival waits; the second hands itself to the first and
// returns immediately to keep its socket open.
func (p *pairRooms) rendezvous(room string, c *websocket.Conn) (*websocket.Conn, bool) {
	p.mu.Lock()
	if w, ok := p.waiting[room]; ok {
		delete(p.waiting, room)
		p.mu.Unlock()
		w.partner <- c
		return w.conn, false // partner drives; this side just keeps its socket open
	}
	w := &pairWaiter{conn: c, partner: make(chan *websocket.Conn, 1)}
	p.waiting[room] = w
	p.mu.Unlock()

	select {
	case other := <-w.partner:
		return other, true // we drive the bridge
	case <-time.After(2 * time.Minute):
		p.mu.Lock()
		if p.waiting[room] == w {
			delete(p.waiting, room)
		}
		p.mu.Unlock()
		return nil, false
	}
}

// handlePair bridges two parties in the same room, forwarding opaque binary
// frames (NNpsk0 pairing messages) until either closes. The token never reaches
// the server — only roomID = H(token) and ciphertext.
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	room := r.URL.Query().Get("room")
	if room == "" {
		http.Error(w, "missing room", http.StatusBadRequest)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	other, drive := s.pair.rendezvous(room, c)
	if other == nil {
		c.Close(websocket.StatusGoingAway, "pair timeout")
		return
	}
	if !drive {
		<-r.Context().Done() // partner drives the bridge; keep this socket open
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go pairCopy(ctx, c, other, cancel)
	pairCopy(ctx, other, c, cancel)
}

func pairCopy(ctx context.Context, src, dst *websocket.Conn, done func()) {
	for {
		_, data, err := src.Read(ctx)
		if err != nil {
			done()
			return
		}
		if err := dst.Write(ctx, websocket.MessageBinary, data); err != nil {
			done()
			return
		}
	}
}
