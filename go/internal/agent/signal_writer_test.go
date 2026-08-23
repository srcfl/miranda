package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// recConn records overlapping Write calls. If signalWriter dropped its mutex,
// inFlight would exceed 1.
type recConn struct {
	inFlight atomic.Int32
	maxFly   atomic.Int32
	writes   atomic.Int32
}

func (r *recConn) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	n := r.inFlight.Add(1)
	for {
		cur := r.maxFly.Load()
		if n <= cur || r.maxFly.CompareAndSwap(cur, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	r.inFlight.Add(-1)
	r.writes.Add(1)
	return nil
}

func TestSignalWriterSerializesWrites(t *testing.T) {
	rec := &recConn{}
	w := &signalWriter{c: rec}
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.write(context.Background(), []byte("x")); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("write: %v", err)
	}
	if rec.writes.Load() != 8 {
		t.Fatalf("writes = %d, want 8", rec.writes.Load())
	}
	if rec.maxFly.Load() != 1 {
		t.Fatalf("overlapping writes = %d, signalWriter must serialize", rec.maxFly.Load())
	}
}
