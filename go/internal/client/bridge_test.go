// go/internal/client/bridge_test.go
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// syncWriter is an io.Writer safe for concurrent use, for assertions.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestClientBridgePumpsTerminalOverNoise(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	clientPriv, clientPub, _ := noise.GenerateStatic()
	agentPriv, agentPub, _ := noise.GenerateStatic()
	clientMC, agentMC := peer.Pipe()

	// Fake "agent": Noise responder that sends HELLO, echoes DATA, records RESIZE.
	gotResize := make(chan [2]uint16, 1)
	go func() {
		sess, err := peer.RunResponder(ctx, agentMC, agentPriv, clientPub)
		if err != nil {
			return
		}
		hello, _ := json.Marshal(map[string]string{"name": "fake"})
		_ = agentMC.Send(mustEnc(sess, noise.EncodeHello(hello)))
		for {
			ct, err := agentMC.Recv(ctx)
			if err != nil {
				return
			}
			pt, err := sess.Decrypt(ct)
			if err != nil {
				return
			}
			typ, payload, _ := noise.DecodeFrame(pt)
			switch typ {
			case noise.FrameData:
				_ = agentMC.Send(mustEnc(sess, noise.EncodeData(payload))) // echo
			case noise.FrameResize:
				c, r, _ := noise.DecodeResize(payload)
				select {
				case gotResize <- [2]uint16{c, r}:
				default:
				}
			}
		}
	}()

	clientSess, err := peer.RunInitiator(ctx, clientMC, clientPriv, agentPub)
	if err != nil {
		t.Fatal(err)
	}

	in := newBlockingReader()
	out := &syncWriter{}
	resizes := make(chan Size, 1)
	bridgeDone := make(chan error, 1)
	go func() {
		bridgeDone <- ClientBridge(ctx, in, out, resizes, Size{Cols: 80, Rows: 24}, clientMC, clientSess)
	}()

	// Initial RESIZE must reach the agent.
	select {
	case rs := <-gotResize:
		if rs != [2]uint16{80, 24} {
			t.Fatalf("initial resize = %v", rs)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no initial resize")
	}

	// Type a line; expect it echoed to out (HELLO must NOT appear in out).
	in.feed([]byte("hello-bridge"))
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains([]byte(out.String()), []byte("hello-bridge")) {
			if bytes.Contains([]byte(out.String()), []byte("fake")) {
				t.Fatal("HELLO metadata leaked into terminal output")
			}
			return // success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("echo not seen in out; got %q", out.String())
}

func mustEnc(sess *noise.Session, framed []byte) []byte {
	ct, err := sess.Encrypt(framed)
	if err != nil {
		panic(err)
	}
	return ct
}

// blockingReader is an io.Reader fed on demand; Read blocks until data or close.
type blockingReader struct {
	ch chan []byte
}

func newBlockingReader() *blockingReader { return &blockingReader{ch: make(chan []byte, 16)} }
func (b *blockingReader) feed(p []byte)  { b.ch <- p }
func (b *blockingReader) Read(p []byte) (int, error) {
	chunk, ok := <-b.ch
	if !ok {
		return 0, io.EOF
	}
	return copy(p, chunk), nil
}
