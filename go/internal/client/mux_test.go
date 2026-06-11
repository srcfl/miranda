// go/internal/client/mux_test.go
package client

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// fakeAgent: Noise responder. On DATA "emit:<x>" it sends DATA "<x>". A trigger
// channel makes it emit out-of-band (to test that non-focused output is dropped).
type fakeAgent struct {
	mc      peer.MsgConn
	trigger chan string
}

func startFakeAgent(t *testing.T, ctx context.Context, agentPriv, clientPub []byte) (*fakeAgent, []byte) {
	t.Helper()
	clientMC, agentMC := peer.Pipe()
	fa := &fakeAgent{mc: clientMC, trigger: make(chan string, 8)}
	go func() {
		sess, err := peer.RunResponder(ctx, agentMC, agentPriv, clientPub)
		if err != nil {
			return
		}
		send := func(b []byte) { ct, _ := sess.Encrypt(noise.EncodeData(b)); _ = agentMC.Send(ct) }
		go func() {
			for {
				select {
				case s := <-fa.trigger:
					send([]byte(s))
				case <-ctx.Done():
					return
				}
			}
		}()
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
			if typ == noise.FrameData && bytes.HasPrefix(payload, []byte("emit:")) {
				send(payload[len("emit:"):])
			}
		}
	}()
	return fa, nil
}

func TestMuxRoutesToFocusAndDropsBackground(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Build two client sessions, one per fake agent.
	mk := func() (*MuxSession, *fakeAgent) {
		aPriv, aPub, _ := noise.GenerateStatic()
		cPriv, cPub, _ := noise.GenerateStatic()
		fa, _ := startFakeAgent(t, ctx, aPriv, cPub)
		sess, err := peer.RunInitiator(ctx, fa.mc, cPriv, aPub)
		if err != nil {
			t.Fatal(err)
		}
		return &MuxSession{Name: "", MC: fa.mc, Sess: sess}, fa
	}
	s0, fa0 := mk()
	s0.Name = "box0"
	s1, fa1 := mk()
	s1.Name = "box1"
	_ = fa1

	out := &syncWriter{} // from bridge_test.go (same package)
	in := newBlockingReader()
	mux := NewMux([]*MuxSession{s0, s1}, out, DefaultPrefix, Size{Cols: 80, Rows: 24})
	go func() { _ = mux.Run(ctx, in, make(chan Size)) }()

	// Focus starts on box0: ask box0 to emit, see it.
	in.feed([]byte("emit:HELLO0\n"))
	waitFor(t, out, "HELLO0")

	// Switch to box1 (prefix + '2'), ask box1 to emit, see it.
	in.feed([]byte{DefaultPrefix, '2'})
	in.feed([]byte("emit:HELLO1\n"))
	waitFor(t, out, "HELLO1")

	// While focused on box1, box0 emits out-of-band: it must NOT reach out.
	before := out.String()
	fa0.trigger <- "GHOST0"
	time.Sleep(300 * time.Millisecond)
	if bytes.Contains([]byte(out.String()[len(before):]), []byte("GHOST0")) {
		t.Fatal("background (non-focused) machine output leaked to the terminal")
	}
}

// TestMuxCtrlSpacePrefixBinds guards the regression where prefix 0x00 (Ctrl-Space,
// as produced by cli.parsePrefix("ctrl-space")) was treated as "unset" and
// silently replaced by Ctrl-O. With the 0x00-defaulting removed, NewMux must use
// 0x00 verbatim: pressing it then '2' switches focus to box1 (proving the byte
// was consumed as the prefix, not forwarded as data).
func TestMuxCtrlSpacePrefixBinds(t *testing.T) {
	const ctrlSpace = byte(0x00)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mk := func() (*MuxSession, *fakeAgent) {
		aPriv, aPub, _ := noise.GenerateStatic()
		cPriv, cPub, _ := noise.GenerateStatic()
		fa, _ := startFakeAgent(t, ctx, aPriv, cPub)
		sess, err := peer.RunInitiator(ctx, fa.mc, cPriv, aPub)
		if err != nil {
			t.Fatal(err)
		}
		return &MuxSession{MC: fa.mc, Sess: sess}, fa
	}
	s0, _ := mk()
	s0.Name = "box0"
	s1, _ := mk()
	s1.Name = "box1"

	out := &syncWriter{}
	in := newBlockingReader()
	mux := NewMux([]*MuxSession{s0, s1}, out, ctrlSpace, Size{Cols: 80, Rows: 24})
	if mux.prefix != ctrlSpace {
		t.Fatalf("NewMux prefix = %#x, want %#x (Ctrl-Space must not be defaulted away)", mux.prefix, ctrlSpace)
	}
	go func() { _ = mux.Run(ctx, in, make(chan Size)) }()

	// Focus starts on box0.
	in.feed([]byte("emit:HELLO0\n"))
	waitFor(t, out, "HELLO0")

	// Ctrl-Space then '2' must switch focus to box1 (the prefix is honored).
	in.feed([]byte{ctrlSpace, '2'})
	in.feed([]byte("emit:HELLO1\n"))
	waitFor(t, out, "HELLO1")
}

func waitFor(t *testing.T, out *syncWriter, want string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains([]byte(out.String()), []byte(want)) {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("never saw %q in out; got:\n%s", want, out.String())
}
