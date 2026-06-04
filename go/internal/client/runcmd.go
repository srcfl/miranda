// go/internal/client/runcmd.go
package client

import (
	"context"
	"io"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// RunCommand sends cmd (with a trailing newline) over an established session and
// streams the shell's output to out for the given window, then returns. It is the
// non-interactive counterpart to the bridge — used by `tr run` and the NAT-sim
// smoke test (no TTY needed). The agent's HELLO and RESIZE frames are skipped.
func RunCommand(ctx context.Context, mc peer.MsgConn, sess *noise.Session, cmd string, window time.Duration, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, window)
	defer cancel()

	s := newSender(mc, sess)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			ct, err := mc.Recv(ctx)
			if err != nil {
				return
			}
			pt, err := sess.Decrypt(ct)
			if err != nil {
				return
			}
			typ, payload, err := noise.DecodeFrame(pt)
			if err != nil {
				continue
			}
			if typ == noise.FrameData {
				if _, err := out.Write(payload); err != nil {
					return
				}
			}
		}
	}()

	if err := s.send(noise.EncodeData([]byte(cmd + "\n"))); err != nil {
		return err
	}
	<-ctx.Done()
	<-done
	return nil
}
