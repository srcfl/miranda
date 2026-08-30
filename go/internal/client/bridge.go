// go/internal/client/bridge.go
package client

import (
	"context"
	"io"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// Size is a terminal size in character cells.
type Size struct {
	Cols uint16
	Rows uint16
}

// WindowsSink receives each tmux window snapshot the agent pushes (the JSON
// payload of a WINDOWS frame). Called from the receive goroutine — keep it fast
// and non-blocking.
type WindowsSink func(json []byte)

// ClientBridge pumps a local terminal (in/out) over an established Noise session:
// stdin -> DATA frames; incoming DATA -> out; window changes (resizes) -> RESIZE;
// the agent's HELLO is consumed (not written to out). Returns when either side ends.
func ClientBridge(ctx context.Context, in io.Reader, out io.Writer, resizes <-chan Size, initial Size, mc peer.MsgConn, sess *noise.Session) error {
	return ClientBridgeSink(ctx, in, out, resizes, initial, mc, sess, nil)
}

// ClientBridgeSink is ClientBridge with an optional WindowsSink: the overview
// uses it to keep the latest tmux snapshot without changing the byte stream.
func ClientBridgeSink(ctx context.Context, in io.Reader, out io.Writer, resizes <-chan Size, initial Size, mc peer.MsgConn, sess *noise.Session, onWindows WindowsSink) error {
	s := newSender(mc, sess)
	if err := s.send(noise.EncodeResize(initial.Cols, initial.Rows)); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 3)

	// stdin -> peer
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				if e := s.send(noise.EncodeData(buf[:n])); e != nil {
					errc <- e
					return
				}
			}
			if err != nil {
				errc <- err
				return
			}
		}
	}()

	// peer -> stdout (skip HELLO)
	go func() {
		for {
			ct, err := mc.Recv(ctx)
			if err != nil {
				errc <- err
				return
			}
			pt, err := sess.Decrypt(ct)
			if err != nil {
				errc <- err
				return
			}
			typ, payload, err := noise.DecodeFrame(pt)
			if err != nil {
				continue
			}
			if typ == noise.FrameData {
				if _, err := out.Write(payload); err != nil {
					errc <- err
					return
				}
			}
			if typ == noise.FrameWindows && onWindows != nil {
				onWindows(payload)
			}
			// FrameHello / FrameResize from the agent are ignored here: the CLI is
			// a raw passthrough, so it already renders tmux's own status bar and
			// Ctrl-B works. FrameWindows only feeds the overview's summary line —
			// it never touches the byte stream.
		}
	}()

	// resize -> peer
	go func() {
		for {
			select {
			case sz := <-resizes:
				if e := s.send(noise.EncodeResize(sz.Cols, sz.Rows)); e != nil {
					errc <- e
					return
				}
			case <-ctx.Done():
				errc <- ctx.Err()
				return
			}
		}
	}()

	return <-errc
}
