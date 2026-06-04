// go/internal/agent/runtime.go
package agent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

// Runtime runs the agent: it holds the signaling channel and, per attach,
// answers the WebRTC offer, runs the Noise responder, and bridges to a shell.
type Runtime struct {
	cfg    *Config
	launch []string // shell command, e.g. {"tmux","new","-A","-s","main"} or {"sh"}
	stun   []string // STUN URLs; nil for local (host candidates)
}

func NewRuntime(cfg *Config, launch, stun []string) *Runtime {
	return &Runtime{cfg: cfg, launch: launch, stun: stun}
}

// Up registers on the signaling channel under {pinned owner, machine id} and
// serves attaches until ctx is cancelled or the connection drops.
func (rt *Runtime) Up(ctx context.Context) error {
	if len(rt.cfg.PairedOwners) == 0 {
		return errNoOwner
	}
	owner := rt.cfg.PairedOwners[0]
	c, _, err := websocket.Dial(ctx, agentSignalURL(rt.cfg.SignalURL, owner, rt.cfg.MachineID), nil)
	if err != nil {
		return err
	}
	defer c.CloseNow()

	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return err
		}
		var m signal.SignalMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.Type == signal.TypeOffer {
			go rt.handleOffer(ctx, c, m)
		}
	}
}

func (rt *Runtime) handleOffer(ctx context.Context, c *websocket.Conn, m signal.SignalMsg) {
	ans, opened, err := peer.NewAnswerer(rt.stun)
	if err != nil {
		return
	}
	closed := false
	closeOnce := func() {
		if !closed {
			closed = true
			_ = ans.Close()
		}
	}
	defer closeOnce()

	answerSDP, err := peer.CreateAnswer(ans, m.SDP)
	if err != nil {
		return
	}
	reply, _ := json.Marshal(signal.SignalMsg{Type: signal.TypeAnswer, Session: m.Session, SDP: answerSDP})
	if err := c.Write(ctx, websocket.MessageText, reply); err != nil {
		return
	}

	octx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var dc *peer.DataChannel
	select {
	case dc = <-opened:
	case <-octx.Done():
		return // no P2P path (strict P2P) — give up this attach
	}

	ownerPub, err := hex.DecodeString(rt.cfg.PairedOwners[0])
	if err != nil {
		return
	}
	sess, err := peer.RunResponder(ctx, dc, rt.cfg.HostPriv(), ownerPub)
	if err != nil {
		return
	}

	pty, err := StartPTY(ctx, rt.launch)
	if err != nil {
		return
	}
	defer pty.Close()

	_ = RunAgentSession(ctx, dc, sess, pty, rt.cfg.MachineName)
}

// agentSignalURL builds ws(s)://host/agent/signal?owner_id=..&machine_id=..
func agentSignalURL(base, owner, machine string) string {
	ws := "ws" + strings.TrimPrefix(base, "http") // http->ws, https->wss
	return ws + "/agent/signal?owner_id=" + url.QueryEscape(owner) + "&machine_id=" + url.QueryEscape(machine)
}

type runtimeError string

func (e runtimeError) Error() string { return string(e) }

const errNoOwner = runtimeError("no paired owner; run `tr-agent pair-dev --owner-pub <hex>` first")
