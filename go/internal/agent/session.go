// go/internal/agent/session.go
package agent

import (
	"context"
	"encoding/json"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// Shell is the subset of *PTY the session bridge needs.
type Shell interface {
	Read(b []byte) (int, error)
	Write(b []byte) (int, error)
	Resize(cols, rows uint16) error
}

// RunAgentSession bridges an established Noise session to a shell using the
// Plan-1 frame protocol: it sends HELLO (machine name) once, then pumps DATA in
// both directions and applies RESIZE. Returns when either side ends.
func RunAgentSession(ctx context.Context, mc peer.MsgConn, sess *noise.Session, sh Shell, machineName string) error {
	hello, _ := json.Marshal(map[string]string{"name": machineName})
	if err := send(mc, sess, noise.EncodeHello(hello)); err != nil {
		return err
	}

	errc := make(chan error, 2)

	// shell -> peer
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := sh.Read(buf)
			if n > 0 {
				if e := send(mc, sess, noise.EncodeData(buf[:n])); e != nil {
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

	// peer -> shell
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
			switch typ {
			case noise.FrameData:
				if _, err := sh.Write(payload); err != nil {
					errc <- err
					return
				}
			case noise.FrameResize:
				if cols, rows, err := noise.DecodeResize(payload); err == nil {
					_ = sh.Resize(cols, rows)
				}
			}
		}
	}()

	return <-errc
}

func send(mc peer.MsgConn, sess *noise.Session, framed []byte) error {
	ct, err := sess.Encrypt(framed)
	if err != nil {
		return err
	}
	return mc.Send(ct)
}
