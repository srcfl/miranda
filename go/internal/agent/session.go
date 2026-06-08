// go/internal/agent/session.go
package agent

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// Shell is the subset of *PTY the session bridge needs. Close lets the bridge
// tear down the shell side itself so the shell->peer pump (blocked in Read)
// unblocks on session end regardless of which side ended first.
type Shell interface {
	Read(b []byte) (int, error)
	Write(b []byte) (int, error)
	Resize(cols, rows uint16) error
	Close() error
}

// RunAgentSession bridges an established Noise session to a shell using the
// Plan-1 frame protocol: it sends HELLO (machine name) once, then pumps DATA in
// both directions and applies RESIZE. Returns when either side ends.
func RunAgentSession(ctx context.Context, mc peer.MsgConn, sess *noise.Session, sh Shell, machineName string, windowsJSON func() []byte, tmuxPid int) error {
	// noise.Session.Encrypt is not concurrency-safe (nonce counter), and several
	// goroutines now send (HELLO, shell->peer, the windows poller) — serialize.
	var sendMu sync.Mutex
	safeSend := func(framed []byte) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return send(mc, sess, framed)
	}

	hello, _ := json.Marshal(map[string]string{"name": machineName})
	if err := safeSend(noise.EncodeHello(hello)); err != nil {
		return err
	}

	// Child context so the two pumps terminate symmetrically: when one ends, we
	// cancel it to unblock the peer->shell goroutine parked in mc.Recv (which is
	// not unblocked merely by this function returning, nor by closing the PTY).
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Window overview: poll the tmux window list and push a WINDOWS snapshot to
	// the client on change (1s low-rate; hooks are a later latency optimization).
	if windowsJSON != nil {
		go func() {
			var last string
			t := time.NewTicker(time.Second)
			defer t.Stop()
			emit := func() {
				if b := windowsJSON(); b != nil && string(b) != last {
					last = string(b)
					_ = safeSend(noise.EncodeWindows(b))
				}
			}
			emit()
			for {
				select {
				case <-sctx.Done():
					return
				case <-t.C:
					emit()
				}
			}
		}()
	}

	errc := make(chan error, 2)

	// shell -> peer
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := sh.Read(buf)
			if n > 0 {
				if e := safeSend(noise.EncodeData(buf[:n])); e != nil {
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
			ct, err := mc.Recv(sctx)
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
			case noise.FrameControl:
				if tmuxPid > 0 { // tmux launch only; 0 = plain shell, no window/session control
					runTmuxControl(tmuxPid, payload)
				}
			}
		}
	}()

	// Wait for the first goroutine to finish, then unblock the other and drain
	// its result — neither goroutine outlives this call:
	//   - cancel() unblocks peer->shell (parked in mc.Recv on sctx).
	//   - sh.Close() unblocks shell->peer (parked in sh.Read).
	err := <-errc
	cancel()
	_ = sh.Close()
	<-errc
	return err
}

func send(mc peer.MsgConn, sess *noise.Session, framed []byte) error {
	ct, err := sess.Encrypt(framed)
	if err != nil {
		return err
	}
	return mc.Send(ct)
}
