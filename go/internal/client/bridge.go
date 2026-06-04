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

// ClientBridge pumps a local terminal (in/out) over an established Noise session:
// stdin -> DATA frames; incoming DATA -> out; window changes (resizes) -> RESIZE;
// the agent's HELLO is consumed (not written to out). Returns when either side ends.
func ClientBridge(ctx context.Context, in io.Reader, out io.Writer, resizes <-chan Size, initial Size, mc peer.MsgConn, sess *noise.Session) error {
	if err := sendFrame(mc, sess, noise.EncodeResize(initial.Cols, initial.Rows)); err != nil {
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
				if e := sendFrame(mc, sess, noise.EncodeData(buf[:n])); e != nil {
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
			// FrameHello / FrameResize from the agent are ignored by the client.
		}
	}()

	// resize -> peer
	go func() {
		for {
			select {
			case s := <-resizes:
				if e := sendFrame(mc, sess, noise.EncodeResize(s.Cols, s.Rows)); e != nil {
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

func sendFrame(mc peer.MsgConn, sess *noise.Session, framed []byte) error {
	ct, err := sess.Encrypt(framed)
	if err != nil {
		return err
	}
	return mc.Send(ct)
}
