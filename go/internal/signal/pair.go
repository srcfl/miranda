// go/internal/signal/pair.go
package signal

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// pairWaiter is a connection waiting in a room for its partner. done is a shared
// completion signal: it is closed exactly once when the bridge ends, so the
// non-driving handler can return (and release its hijacked socket) without
// depending on its own request context — which, after a websocket hijack, only
// fires once that very handler returns (a circular dependency that would leak
// the goroutine and its FD).
type pairWaiter struct {
	conn    *websocket.Conn
	partner chan *websocket.Conn
	done    chan struct{}
}

type pairRooms struct {
	mu      sync.Mutex
	waiting map[string]*pairWaiter
}

func newPairRooms() *pairRooms { return &pairRooms{waiting: map[string]*pairWaiter{}} }

// rendezvous pairs two conns in a room. It returns the partner conn, a shared
// done channel for the pairing, and true if THIS conn should drive the bridge.
// The first arrival waits; the second hands itself to the first and returns
// immediately. The driver owns teardown of BOTH conns and closes done when the
// bridge ends, so the non-driving handler is released too.
func (p *pairRooms) rendezvous(room string, c *websocket.Conn) (*websocket.Conn, chan struct{}, bool) {
	p.mu.Lock()
	if w, ok := p.waiting[room]; ok {
		delete(p.waiting, room)
		p.mu.Unlock()
		w.partner <- c
		return w.conn, w.done, false // partner drives; we wait on the shared done
	}
	w := &pairWaiter{conn: c, partner: make(chan *websocket.Conn, 1), done: make(chan struct{})}
	p.waiting[room] = w
	p.mu.Unlock()

	select {
	case other := <-w.partner:
		return other, w.done, true // we drive the bridge
	case <-time.After(2 * time.Minute):
		p.mu.Lock()
		if p.waiting[room] == w {
			delete(p.waiting, room)
			p.mu.Unlock()
			return nil, w.done, false
		}
		// A second party already claimed this room (it deleted us from the map
		// and sent on w.partner) in the same instant the timer fired. Drive the
		// bridge for it instead of orphaning it on <-done.
		p.mu.Unlock()
		return <-w.partner, w.done, true
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
	other, done, drive := s.pair.rendezvous(room, c)
	if other == nil {
		c.Close(websocket.StatusGoingAway, "pair timeout")
		return
	}
	if !drive {
		// The partner drives the bridge. Wait on the shared done signal — NOT
		// r.Context(), which after a websocket hijack only fires once this very
		// handler returns. When the driver tears the bridge down it closes done
		// AND closes this conn (other, from the driver's perspective), so this
		// handler returns promptly and releases its hijacked socket.
		<-done
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	// On bridge end, release BOTH conns: cancel our copy loops, close the
	// non-driving partner's socket, and close done so the non-driving handler
	// returns. close(done) must run exactly once.
	defer func() {
		cancel()
		other.Close(websocket.StatusNormalClosure, "pair complete")
		close(done)
	}()
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
