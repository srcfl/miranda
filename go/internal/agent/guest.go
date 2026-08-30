// go/internal/agent/guest.go
//
// Guest enforcement at the agent (spec G1c). An offer whose proven principal is
// not a pinned owner but is bound by a stored, valid grant is served as a GUEST:
//
//   - read-only (default): a pane mirror, not a tmux client. The agent paints
//     the scope's active pane once with capture-pane, then streams pipe-pane
//     output as terminal frames. Guest input is read and dropped — there is no
//     shell and no tmux client, so a ro guest cannot type, switch windows, or
//     see any other session. It is confinement by construction.
//   - read-write: a grouped guest-<gid> session (D4's machinery) so the guest
//     gets a real tmux client on the shared windows. A writable shell is total
//     control as the agent user; the grant's --write consent said so.
//
// Both are bounded by a per-session deadline at the grant's na and by the
// revoke registry, so expiry and revocation tear a live guest down at once.
package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// gidShapeRe matches a grant id (16 lowercase hex) before it names a file or a
// tombstone — the same shape identity.Grant validates.
var gidShapeRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// guestRegistry tracks live guest sessions so revoke-grant (and a future admin
// action) can cancel every session serving a given gid immediately.
type guestRegistry struct {
	mu   sync.Mutex
	next uint64
	byID map[string]map[uint64]context.CancelFunc
}

func (g *guestRegistry) add(gid string, cancel context.CancelFunc) func() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.byID == nil {
		g.byID = map[string]map[uint64]context.CancelFunc{}
	}
	if g.byID[gid] == nil {
		g.byID[gid] = map[uint64]context.CancelFunc{}
	}
	id := g.next
	g.next++
	g.byID[gid][id] = cancel
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if m := g.byID[gid]; m != nil {
			delete(m, id)
			if len(m) == 0 {
				delete(g.byID, gid)
			}
		}
	}
}

func (g *guestRegistry) drop(gid string) {
	g.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(g.byID[gid]))
	for _, c := range g.byID[gid] {
		cancels = append(cancels, c)
	}
	g.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// serveGuest serves an authenticated guest over the Noise session per the
// grant's mode. The deadline at na and the revoke registry both cancel gctx, so
// expiry or revocation ends the session within the frame loop's next read.
func (rt *Runtime) serveGuest(ctx context.Context, mc peer.MsgConn, sess *noise.Session, grant *identity.SignedGrant) error {
	rt.sessionStarted()
	defer rt.sessionEnded()

	gctx, cancel := context.WithDeadline(ctx, time.Unix(grant.NA, 0))
	defer cancel()
	unregister := rt.guests.add(grant.GID, cancel)
	defer unregister()

	if rt.Logf != nil {
		rt.Logf("event=guest_attach gid=%s mode=%s scope=%q", grant.GID, grant.Mode, grant.Scope)
	}
	if grant.Mode == "rw" {
		return rt.serveGuestRW(gctx, mc, sess, grant)
	}
	return rt.serveGuestRO(gctx, mc, sess, grant)
}

// endedByGrant turns a deadline/cancel into the honest guest-facing reason.
func endedByGrant(ctx context.Context) string {
	if ctx.Err() == context.DeadlineExceeded {
		return "\r\n[this share has ended — ask for a new invite]\r\n"
	}
	return "\r\n[this share was ended by the owner]\r\n"
}

// serveGuestRO mirrors the scope's active pane read-only. No PTY, no tmux
// client, no control channel: the guest sees output and nothing else.
func (rt *Runtime) serveGuestRO(ctx context.Context, mc peer.MsgConn, sess *noise.Session, grant *identity.SignedGrant) error {
	var sendMu sync.Mutex
	safeSend := func(framed []byte) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return send(mc, sess, framed)
	}
	hello, _ := json.Marshal(map[string]string{"name": rt.machineName() + " (shared, read-only)"})
	_ = safeSend(noise.EncodeHello(hello))

	// The scope names the session; tmux resolves it to that session's active
	// pane. (An exact-match "=name" works for session targets but not pane
	// targets on tmux 3.x, so the plain name is what pane commands take.)
	target := grant.Scope
	// Initial paint. A missing scope session is an honest dead end, not a crash.
	paint, err := exec.Command("tmux", "capture-pane", "-e", "-p", "-t", target).Output()
	if err != nil {
		_ = safeSend(noise.EncodeData([]byte("\r\n[this share's session isn't running right now]\r\n")))
		<-ctx.Done()
		return nil
	}
	_ = safeSend(noise.EncodeData(paint))

	// Live stream: tmux pipes the pane's output into a fifo we read. -O sends
	// only pane output (never our input) down the pipe.
	fifoDir, err := os.MkdirTemp("", "mir-guest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(fifoDir)
	fifo := filepath.Join(fifoDir, "pane")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		return err
	}
	if err := exec.Command("tmux", "pipe-pane", "-O", "-t", target, "cat > "+fifo).Run(); err != nil {
		return err
	}
	defer exec.Command("tmux", "pipe-pane", "-t", target).Run() // stop piping

	streamErr := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_RDONLY, 0)
		if err != nil {
			streamErr <- err
			return
		}
		defer f.Close()
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				if e := safeSend(noise.EncodeData(buf[:n])); e != nil {
					streamErr <- e
					return
				}
			}
			if err != nil {
				streamErr <- err
				return
			}
		}
	}()

	// Inbound frames from a read-only guest are dropped. Reading them keeps the
	// transport drained and lets us notice a disconnect; nothing is forwarded to
	// any pane, and FrameControl never reaches tmux.
	dropped := 0
	recvErr := make(chan error, 1)
	go func() {
		for {
			ct, err := mc.Recv(ctx)
			if err != nil {
				recvErr <- err
				return
			}
			pt, err := sess.Decrypt(ct)
			if err != nil {
				recvErr <- err
				return
			}
			if typ, _, derr := noise.DecodeFrame(pt); derr == nil && typ == noise.FrameData {
				dropped++
			}
		}
	}()

	var reason string
	select {
	case <-ctx.Done():
		reason = endedByGrant(ctx)
	case <-streamErr:
	case <-recvErr:
	}
	if reason != "" {
		_ = safeSend(noise.EncodeData([]byte(reason)))
	}
	if rt.Logf != nil && dropped > 0 {
		rt.Logf("event=guest_input_dropped gid=%s frames=%d", grant.GID, dropped)
	}
	return nil
}

// serveGuestRW gives the guest a real tmux client on a grouped guest-<gid>
// session. It reuses the owner session bridge but with tmuxPid=0 and no control
// handler, so a guest gets NO agent-level control channel (no FrameControl to
// tmux, no window snapshot) — only the raw terminal, where tmux's own Ctrl-B
// still works inside their own client. A non-tmux launch cannot be grouped, so
// a guest cannot be served rw there; refuse honestly.
func (rt *Runtime) serveGuestRW(ctx context.Context, mc peer.MsgConn, sess *noise.Session, grant *identity.SignedGrant) error {
	if !isDefaultTmuxLaunch(rt.launch) {
		_ = send(mc, sess, noise.EncodeHello(mustJSON(map[string]string{"name": rt.machineName()})))
		_ = send(mc, sess, noise.EncodeData([]byte("\r\n[write sharing needs tmux on this machine — ask the owner]\r\n")))
		<-ctx.Done()
		return nil
	}
	if err := ensureGroupedBase(grant.Scope); err != nil {
		return err
	}
	name := guestSessionName(grant.GID)
	pty, err := StartPTY(ctx, groupedLaunch(grant.Scope, name))
	if err != nil {
		return err
	}
	defer pty.Close()
	defer killGroupedSession(name)

	err = RunAgentSession(ctx, mc, sess, pty, rt.machineName()+" (shared)", nil, 0, nil)
	if ctx.Err() != nil {
		_ = send(mc, sess, noise.EncodeData([]byte(endedByGrant(ctx))))
	}
	return err
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// revokeGrantHandler builds the per-session handler for revoke-grant from one
// authenticated owner: tombstone the gid (future attaches refuse) and drop every
// live session serving it. Only the session owner's own gids are actionable.
func (rt *Runtime) revokeGrantHandler(owner string) ControlHandler {
	return func(payload []byte) (bool, map[string]string) {
		var c struct {
			A   string `json:"a"`
			GID string `json:"gid"`
		}
		if json.Unmarshal(payload, &c) != nil || c.A != "revoke-grant" {
			return false, nil
		}
		if !guestGIDShape(c.GID) {
			return true, nil
		}
		// Only tombstone a gid this owner actually granted; loading it back
		// confirms owner + machine before the gid becomes a persistent tombstone.
		sg := grantByID(rt.cfg.Dir, c.GID)
		if sg == nil || sg.Owner != owner || sg.Machine != rt.cfg.MachineID {
			// Nothing of ours by that gid — still drop any live session and ack,
			// so a revoke stays idempotent even after the file is gone.
			rt.guests.drop(c.GID)
			return true, map[string]string{"name": rt.machineName(), "ack": "revoke-grant:" + c.GID}
		}
		if err := TombstoneGrant(rt.cfg.Dir, c.GID); err != nil {
			if rt.Logf != nil {
				rt.Logf("event=grant_revoke_failed gid=%s err=%v", c.GID, err)
			}
			return true, nil
		}
		rt.guests.drop(c.GID)
		if rt.Logf != nil {
			rt.Logf("event=grant_revoked gid=%s", c.GID)
		}
		return true, map[string]string{"name": rt.machineName(), "ack": "revoke-grant:" + c.GID}
	}
}

// grantByID loads one stored grant by gid, or nil.
func grantByID(dir, gid string) *identity.SignedGrant {
	if !guestGIDShape(gid) {
		return nil
	}
	raw, err := os.ReadFile(grantPath(dir, gid))
	if err != nil {
		return nil
	}
	sg, err := identity.ParseSignedGrant(raw)
	if err != nil {
		return nil
	}
	return sg
}

func guestGIDShape(gid string) bool { return gidShapeRe.MatchString(gid) }
