// go/internal/signal/pair.go
package signal

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

var errPairCapacity = errors.New("pair capacity reached")

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
	active  int
}

func (p *pairRooms) beginBridge(max int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if max > 0 && p.active >= max {
		return false
	}
	p.active++
	return true
}

func (p *pairRooms) endBridge() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active > 0 {
		p.active--
	}
}

func newPairRooms() *pairRooms { return &pairRooms{waiting: map[string]*pairWaiter{}} }

// rendezvous pairs two conns in a room. It returns the partner conn, a shared
// done channel for the pairing, and true if THIS conn should drive the bridge.
// The first arrival waits; the second hands itself to the first and returns
// immediately. The driver owns teardown of BOTH conns and closes done when the
// bridge ends, so the non-driving handler is released too.
func (p *pairRooms) rendezvous(room string, c *websocket.Conn, maxRooms int) (*websocket.Conn, chan struct{}, bool, error) {
	p.mu.Lock()
	if w, ok := p.waiting[room]; ok {
		delete(p.waiting, room)
		p.mu.Unlock()
		w.partner <- c
		return w.conn, w.done, false, nil // partner drives; we wait on the shared done
	}
	if maxRooms > 0 && len(p.waiting) >= maxRooms {
		p.mu.Unlock()
		return nil, nil, false, errPairCapacity
	}
	w := &pairWaiter{conn: c, partner: make(chan *websocket.Conn, 1), done: make(chan struct{})}
	p.waiting[room] = w
	p.mu.Unlock()

	select {
	case other := <-w.partner:
		return other, w.done, true, nil // we drive the bridge
	case <-time.After(2 * time.Minute):
		p.mu.Lock()
		if p.waiting[room] == w {
			delete(p.waiting, room)
			p.mu.Unlock()
			return nil, w.done, false, nil
		}
		// A second party already claimed this room (it deleted us from the map
		// and sent on w.partner) in the same instant the timer fired. Drive the
		// bridge for it instead of orphaning it on <-done.
		p.mu.Unlock()
		return <-w.partner, w.done, true, nil
	}
}

// handlePair bridges two parties in the same room, forwarding opaque binary
// frames (NNpsk0 pairing messages) until either closes. The token never reaches
// the server — only roomID = H(token) and ciphertext.
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	room := r.URL.Query().Get("room")
	if room == "" || len(room) > maxPairRoomIDLen {
		http.Error(w, "missing or invalid room", http.StatusBadRequest)
		return
	}
	c, err := websocket.Accept(w, r, acceptOpts)
	if err != nil {
		return
	}
	c.SetReadLimit(maxPairFrameBytes)
	other, done, drive, err := s.pair.rendezvous(room, c, s.maxPairRooms)
	if err != nil {
		c.Close(websocket.StatusTryAgainLater, err.Error())
		return
	}
	if other == nil {
		c.Close(websocket.StatusGoingAway, "pair timeout")
		return
	}
	if !drive {
		<-done
		return
	}
	if !s.pair.beginBridge(s.maxPairBridges) {
		c.Close(websocket.StatusTryAgainLater, "pair bridge capacity reached")
		other.Close(websocket.StatusTryAgainLater, "pair bridge capacity reached")
		close(done)
		return
	}
	defer s.pair.endBridge()
	other.SetReadLimit(maxPairFrameBytes)
	ttl := s.pairBridgeTTL
	if ttl <= 0 {
		ttl = defaultPairBridgeTTL
	}
	ctx, cancel := context.WithTimeout(r.Context(), ttl)
	budget := s.pairBridgeMaxBytes
	if budget <= 0 {
		budget = defaultPairBridgeBytes
	}
	var used atomic.Int64
	defer func() {
		cancel()
		other.Close(websocket.StatusNormalClosure, "pair complete")
		close(done)
	}()
	go pairCopy(ctx, c, other, cancel, &used, budget)
	pairCopy(ctx, other, c, cancel, &used, budget)
}

func pairCopy(ctx context.Context, src, dst *websocket.Conn, done func(), used *atomic.Int64, budget int64) {
	for {
		_, data, err := src.Read(ctx)
		if err != nil {
			done()
			return
		}
		if budget > 0 && used != nil {
			if used.Add(int64(len(data))) > budget {
				done()
				return
			}
		}
		if err := dst.Write(ctx, websocket.MessageBinary, data); err != nil {
			done()
			return
		}
	}
}
