// go/internal/agent/runtime.go
package agent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

// Runtime runs the agent: it holds the signaling channel and, per attach,
// answers the WebRTC offer, runs the Noise responder, and bridges to a shell.
type Runtime struct {
	cfg    *Config
	launch []string         // shell command, e.g. {"tmux","new","-A","-s","main"} or {"sh"}
	ice    []peer.ICEServer // STUN/TURN servers; nil for local (host candidates)

	baseBackoff    time.Duration        // first reconnect delay (grows on repeated dial failures)
	maxBackoff     time.Duration        // cap
	reloadInterval time.Duration        // how often to re-read config for newly-paired owners
	Logf           func(string, ...any) // optional reconnect/status log (set by the CLI)
}

func NewRuntime(cfg *Config, launch []string, ice []peer.ICEServer) *Runtime {
	return &Runtime{cfg: cfg, launch: launch, ice: ice, baseBackoff: time.Second, maxBackoff: 30 * time.Second, reloadInterval: 3 * time.Second}
}

// Up keeps the agent registered on the signaling channel for EVERY paired owner
// under {owner, machine id}, serving attaches — so any of your devices (laptop
// CLI, phone, ...) can reach this machine. Each owner gets its own connection
// that RECONNECTS with backoff if it drops (Cloudflare idle timeout, relay
// restart, network blip). Returns only when ctx is cancelled or no owner paired.
func (rt *Runtime) Up(ctx context.Context) error {
	if len(rt.cfg.PairedOwners) == 0 {
		return errNoOwner
	}
	var mu sync.Mutex
	served := map[string]bool{}
	start := func(owner string) {
		mu.Lock()
		defer mu.Unlock()
		if served[owner] {
			return
		}
		served[owner] = true
		if rt.Logf != nil {
			rt.Logf("serving owner %s", short(owner))
		}
		go rt.serveOwner(ctx, owner)
	}
	for _, o := range rt.cfg.PairedOwners {
		start(o)
	}
	// Hot-reload: pick up owners added by `tr-agent pair` WITHOUT a restart, so
	// pairing a new device (or a new passkey identity) just works.
	t := time.NewTicker(rt.reloadInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if rt.cfg.Dir == "" {
				continue
			}
			if owners, err := ReloadOwners(rt.cfg.Dir); err == nil {
				for _, o := range owners {
					start(o)
				}
			}
		}
	}
}

// serveOwner maintains one owner's registration, reconnecting with backoff.
func (rt *Runtime) serveOwner(ctx context.Context, owner string) {
	backoff := rt.baseBackoff
	for {
		connected, err := rt.serveOnce(ctx, owner)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = rt.baseBackoff // a live connection dropped -> retry promptly
		}
		if rt.Logf != nil {
			rt.Logf("owner %s disconnected (%v); reconnecting in %s", short(owner), err, backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if !connected { // dial keeps failing (relay down) -> exponential backoff
			if backoff *= 2; backoff > rt.maxBackoff {
				backoff = rt.maxBackoff
			}
		}
	}
}

// serveOnce dials the signaling channel for one owner and serves offers until
// the connection drops. The bool reports whether the dial succeeded (a live
// connection that later dropped), so the caller can reconnect promptly vs. back
// off a down relay.
func (rt *Runtime) serveOnce(ctx context.Context, owner string) (bool, error) {
	c, _, err := websocket.Dial(ctx, agentSignalURL(rt.cfg.SignalURL, owner, rt.cfg.MachineID), agentDialOptions(rt.cfg.RegistrationSecret))
	if err != nil {
		return false, err
	}
	defer c.CloseNow()

	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return true, err
		}
		var m signal.SignalMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.Type == signal.TypeOffer {
			go rt.handleOffer(ctx, c, m, owner)
		}
	}
}

func short(hexKey string) string {
	if len(hexKey) > 8 {
		return hexKey[:8] + "…"
	}
	return hexKey
}

// iceFor returns the agent's static ICE servers plus ephemeral TURN creds
// fetched from the signaling server (for symmetric-NAT / cellular reachability).
func (rt *Runtime) iceFor(ctx context.Context) []peer.ICEServer {
	if turn, err := peer.FetchTURN(ctx, rt.cfg.SignalURL); err == nil && len(turn) > 0 {
		return append(append([]peer.ICEServer{}, rt.ice...), turn...)
	}
	return rt.ice
}

func (rt *Runtime) handleOffer(ctx context.Context, c *websocket.Conn, m signal.SignalMsg, owner string) {
	ans, opened, err := peer.NewAnswerer(rt.iceFor(ctx))
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

	// Per-attach context tied to PeerConnection liveness. When the remote
	// disconnects (the dominant steady-state path), the state handler cancels
	// attachCtx, which unblocks RunResponder/RunAgentSession so the deferred
	// closeOnce()/pty.Close() actually run and reclaim the PC, shell, and
	// goroutines while the agent's long-lived ctx stays alive.
	attachCtx, attachCancel := context.WithCancel(ctx)
	defer attachCancel()
	ans.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		switch s {
		case webrtc.PeerConnectionStateDisconnected,
			webrtc.PeerConnectionStateFailed,
			webrtc.PeerConnectionStateClosed:
			attachCancel()
		}
	})

	answerSDP, err := peer.CreateAnswer(ans, m.SDP)
	if err != nil {
		return
	}
	reply, _ := json.Marshal(signal.SignalMsg{Type: signal.TypeAnswer, Session: m.Session, SDP: answerSDP})
	if err := c.Write(ctx, websocket.MessageText, reply); err != nil {
		return
	}

	octx, cancel := context.WithTimeout(attachCtx, 20*time.Second)
	defer cancel()
	var dc *peer.DataChannel
	select {
	case dc = <-opened:
	case <-octx.Done():
		return // no P2P path (strict P2P) — give up this attach
	}

	ownerPub, err := hex.DecodeString(owner)
	if err != nil {
		return
	}
	sess, err := peer.RunResponder(attachCtx, dc, rt.cfg.HostPriv(), ownerPub)
	if err != nil {
		return
	}

	pty, err := StartPTY(attachCtx, rt.launch)
	if err != nil {
		return
	}
	defer pty.Close()

	// For a tmux launch, push window-list snapshots so clients render an overview,
	// and accept window control commands (select/new/rename/kill) for that session.
	session := sessionFromLaunch(rt.launch)
	var windows func() []byte
	if session != "" {
		windows = func() []byte { return tmuxWindowsJSON(session) }
	}
	_ = RunAgentSession(attachCtx, dc, sess, pty, rt.cfg.MachineName, windows, session)
}

// agentSignalURL builds ws(s)://host/agent/signal?owner_id=..&machine_id=..
func agentSignalURL(base, owner, machine string) string {
	ws := "ws" + strings.TrimPrefix(base, "http") // http->ws, https->wss
	return ws + "/agent/signal?owner_id=" + url.QueryEscape(owner) + "&machine_id=" + url.QueryEscape(machine)
}

func agentDialOptions(registrationSecret string) *websocket.DialOptions {
	if registrationSecret == "" {
		return nil
	}
	return &websocket.DialOptions{
		HTTPHeader: http.Header{
			signal.AgentRegistrationSecretHeader: []string{registrationSecret},
		},
	}
}

type runtimeError string

func (e runtimeError) Error() string { return string(e) }

const errNoOwner = runtimeError("no paired owner; run `tr-agent pair-dev --owner-pub <hex>` first")
