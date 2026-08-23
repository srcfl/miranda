package client

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

func TestReconnectLoopRedialsAfterDrop(t *testing.T) {
	var dials int32
	ctx, cancel := context.WithCancel(context.Background())
	dial := func(context.Context) (peer.MsgConn, *noise.Session, func(), error) {
		n := atomic.AddInt32(&dials, 1)
		if n >= 2 {
			cancel()
		}
		return fakeConn{tag: "c"}, nil, func() {}, nil
	}
	runs := 0
	run := func(context.Context, peer.MsgConn, *noise.Session) error {
		runs++
		if runs == 1 {
			return peer.ErrDataChannelClosed
		}
		return io.EOF
	}
	err := ReconnectLoop(ctx, dial, run)
	if err != nil && err != context.Canceled {
		t.Fatalf("loop: %v", err)
	}
	if atomic.LoadInt32(&dials) < 2 {
		t.Fatalf("dials = %d, want at least 2 (reconnect after drop)", dials)
	}
	if runs < 1 {
		t.Fatal("session run was never invoked")
	}
}

func TestReconnectLoopStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ReconnectLoop(ctx, func(context.Context) (peer.MsgConn, *noise.Session, func(), error) {
		t.Fatal("must not dial after cancel")
		return nil, nil, nil, nil
	}, func(context.Context, peer.MsgConn, *noise.Session) error { return nil })
	if err != context.Canceled {
		t.Fatalf("want canceled, got %v", err)
	}
}
