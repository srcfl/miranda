package quicmsg

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

// TestConnRoundTripFrames stands up a quicmsg listener on an ephemeral port,
// dials it, and asserts that several frames round-trip exactly and in order in
// both directions: an empty frame, a small frame, and a 70_000-byte frame
// (which exercises the length prefix beyond 64 KiB).
func TestConnRoundTripFrames(t *testing.T) {
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	frames := [][]byte{
		{},                                 // empty frame
		[]byte("hello, miranda"),           // small frame
		bytes.Repeat([]byte{0xAB}, 70_000), // > 64 KiB
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Accept side runs concurrently with the dial.
	type acceptResult struct {
		c   *Conn
		err error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		c, err := ln.Accept(ctx)
		accepted <- acceptResult{c, err}
	}()

	client, err := Dial(ctx, ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	ar := <-accepted
	if ar.err != nil {
		t.Fatalf("Accept: %v", ar.err)
	}
	server := ar.c
	defer server.Close()

	// Exercise both directions: client->server and server->client.
	dirs := []struct {
		name     string
		sender   *Conn
		receiver *Conn
	}{
		{"client->server", client, server},
		{"server->client", server, client},
	}

	for _, d := range dirs {
		d := d
		t.Run(d.name, func(t *testing.T) {
			var wg sync.WaitGroup
			wg.Add(1)
			var recvErr error
			got := make([][]byte, len(frames))
			go func() {
				defer wg.Done()
				for i := range frames {
					b, err := d.receiver.Recv(ctx)
					if err != nil {
						recvErr = err
						return
					}
					got[i] = b
				}
			}()

			for _, f := range frames {
				if err := d.sender.Send(f); err != nil {
					t.Fatalf("Send: %v", err)
				}
			}

			wg.Wait()
			if recvErr != nil {
				t.Fatalf("Recv: %v", recvErr)
			}
			for i, want := range frames {
				if !bytes.Equal(got[i], want) {
					t.Fatalf("frame %d: got %d bytes, want %d bytes (in-order mismatch)", i, len(got[i]), len(want))
				}
			}
		})
	}
}

// TestRecvRespectsContext asserts that Recv returns promptly with an error when
// its ctx is already cancelled, rather than blocking forever waiting for data.
func TestRecvRespectsContext(t *testing.T) {
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	dialCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	accepted := make(chan *Conn, 1)
	go func() {
		c, err := ln.Accept(dialCtx)
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()

	client, err := Dial(dialCtx, ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	server := <-accepted
	if server == nil {
		t.Fatal("Accept failed")
	}
	defer server.Close()

	// Already-cancelled ctx: Recv must return promptly with an error and not
	// block (no peer is sending anything).
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()

	done := make(chan error, 1)
	go func() {
		_, err := client.Recv(cctx)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Recv with cancelled ctx returned nil error, want non-nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Recv blocked despite cancelled ctx")
	}

	// The connection must still be usable afterwards: a subsequent Send/Recv
	// with a fresh ctx should round-trip, proving the cancel didn't corrupt the
	// stream.
	okCtx, okCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer okCancel()
	if err := server.Send([]byte("ping")); err != nil {
		t.Fatalf("post-cancel Send: %v", err)
	}
	b, err := client.Recv(okCtx)
	if err != nil {
		t.Fatalf("post-cancel Recv: %v", err)
	}
	if !bytes.Equal(b, []byte("ping")) {
		t.Fatalf("post-cancel Recv: got %q, want %q", b, "ping")
	}
}
