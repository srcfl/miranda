// go/internal/agent/runtime.go
package agent

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/srcful/terminal-relay/go/internal/identity"
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

// ownerPubFromBinding verifies the offer's owner binding and returns the X25519
// transport key to pin for Noise-KK. The signed record retains the historical
// Wallet field on the wire, but it contains the Miranda owner id.
func ownerPubFromBinding(bindingJSON, owner string) ([]byte, error) {
	if bindingJSON == "" {
		return nil, fmt.Errorf("attach: missing owner binding")
	}
	sb, err := identity.ParseSignedBinding([]byte(bindingJSON))
	if err != nil {
		return nil, err
	}
	if sb.Wallet != owner {
		return nil, fmt.Errorf("attach: binding owner %q != owner_id %q", sb.Wallet, owner)
	}
	if err := identity.VerifyBinding(sb); err != nil {
		return nil, err
	}
	return hex.DecodeString(sb.X25519)
}

// minHealthyUptime is the shortest a signaling connection must stay up before we
// treat it as a genuinely healthy session whose drop warrants a prompt reconnect.
// A connection the relay accepts then drops faster than this (a same-identity
// ping-pong, or a crash-looping relay) is a FLAP, not a healthy reconnect:
// resetting the backoff for it produces a flat ~1s reconnect storm, so we grow
// the backoff instead. See nextBackoff.
const minHealthyUptime = 10 * time.Second

// defaultMaxConcurrentAttaches bounds how many attach handshakes (each a full
// WebRTC PeerConnection + ICE gather + Noise responder) may be in flight at once
// across all owners. An attach is unauthenticated until the Noise KK handshake
// completes, and the relay's /attach endpoint is intentionally open at the HTTP
// layer, so without this cap anyone who knows an owner_id+machine_id could pump
// offers and exhaust the agent's FDs/memory/goroutines (a pre-auth DoS) — without
// ever getting the shell. 64 comfortably covers a person's real devices.
const defaultMaxConcurrentAttaches = 16

// Runtime runs the agent: it holds the signaling channel and, per attach,
// answers the WebRTC offer, runs the Noise responder, and bridges to a shell.
type Runtime struct {
	cfg    *Config
	launch []string         // shell command, e.g. {"tmux","new","-A","-s","main"} or {"sh"}
	ice    []peer.ICEServer // STUN/TURN servers; nil for local (host candidates)

	sem chan struct{} // bounds concurrent in-flight attach handshakes (pre-auth DoS guard)

	seenMu       sync.Mutex
	seenAttaches map[string]time.Time // valid signed session ids; replay guard

	pinMu    sync.Mutex
	ownerRun map[string]context.CancelFunc // live serveOwner loops; cancelled on unpin

	active int64 // count of authenticated, serving sessions (atomic); gates auto-update

	baseBackoff    time.Duration        // first reconnect delay (grows on repeated dial failures)
	maxBackoff     time.Duration        // cap
	reloadInterval time.Duration        // how often to re-read config for newly-paired owners
	Logf           func(string, ...any) // optional reconnect/status log (set by the CLI)

	rename renameState // live display name + signaling writers for mid-run rename (see rename.go)
}

// admit reserves a slot for a new attach handshake, returning false immediately
// (never blocking) when too many are already in flight. release frees the slot.
func (rt *Runtime) admit() bool {
	select {
	case rt.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (rt *Runtime) release() { <-rt.sem }

func (rt *Runtime) sessionStarted() { atomic.AddInt64(&rt.active, 1) }
func (rt *Runtime) sessionEnded()   { atomic.AddInt64(&rt.active, -1) }

// ActiveSessions reports the number of in-flight authenticated owner sessions.
// Opt-in auto-update uses this to defer a binary swap until the agent is idle.
func (rt *Runtime) ActiveSessions() int { return int(atomic.LoadInt64(&rt.active)) }

func NewRuntime(cfg *Config, launch []string, ice []peer.ICEServer) *Runtime {
	return &Runtime{cfg: cfg, launch: launch, ice: ice, sem: make(chan struct{}, defaultMaxConcurrentAttaches), seenAttaches: make(map[string]time.Time), ownerRun: map[string]context.CancelFunc{}, baseBackoff: time.Second, maxBackoff: 30 * time.Second, reloadInterval: 3 * time.Second}
}

func (rt *Runtime) ownerPinned(owner string) bool {
	rt.pinMu.Lock()
	defer rt.pinMu.Unlock()
	return rt.cfg.IsOwnerPinned(owner)
}

func (rt *Runtime) replacePins(owners []string) {
	rt.pinMu.Lock()
	defer rt.pinMu.Unlock()
	rt.cfg.PairedOwners = append([]string(nil), owners...)
}

type connWriter interface {
	Write(ctx context.Context, typ websocket.MessageType, p []byte) error
}

type signalWriter struct {
	mu sync.Mutex
	c  connWriter
}

func (w *signalWriter) write(ctx context.Context, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.c.Write(ctx, websocket.MessageText, data)
}

func (rt *Runtime) acceptAttachSession(owner, session string) bool {
	now := time.Now()
	key := owner + "|" + session
	rt.seenMu.Lock()
	defer rt.seenMu.Unlock()
	for k, seen := range rt.seenAttaches {
		if now.Sub(seen) > 5*time.Minute {
			delete(rt.seenAttaches, k)
		}
	}
	if _, replay := rt.seenAttaches[key]; replay {
		return false
	}
	rt.seenAttaches[key] = now
	return true
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
	rt.reconcileOwners(ctx, append([]string(nil), rt.cfg.PairedOwners...))
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
				rt.reconcileOwners(ctx, owners)
			}
		}
	}
}

func (rt *Runtime) reconcileOwners(ctx context.Context, owners []string) {
	want := map[string]bool{}
	for _, o := range owners {
		if o != "" {
			want[o] = true
		}
	}
	rt.replacePins(owners)

	rt.pinMu.Lock()
	defer rt.pinMu.Unlock()
	if rt.ownerRun == nil {
		rt.ownerRun = map[string]context.CancelFunc{}
	}
	for owner, stop := range rt.ownerRun {
		if !want[owner] {
			stop()
			delete(rt.ownerRun, owner)
			if rt.Logf != nil {
				rt.Logf("stopped owner %s", short(owner))
			}
		}
	}
	for owner := range want {
		if _, live := rt.ownerRun[owner]; live {
			continue
		}
		octx, stop := context.WithCancel(ctx)
		rt.ownerRun[owner] = stop
		if rt.Logf != nil {
			rt.Logf("serving owner %s", short(owner))
		}
		go rt.serveOwner(octx, owner)
	}
}

// nextBackoff decides the next reconnect delay from the outcome of the previous
// attempt, decoupled from sleeping/jitter so it can be unit-tested in isolation.
//
//   - dialed && uptime >= minHealthyUptime: a genuinely healthy session dropped
//     (idle timeout, relay restart) -> reset to base for a prompt reconnect.
//   - dialed && uptime < minHealthyUptime: a FLAP (relay accepts-then-closes, a
//     same-identity ping-pong, a crash loop) -> GROW (×2, capped) so we damp the
//     storm instead of hammering at base.
//   - !dialed: the dial itself failed (relay down) -> GROW (×2, capped).
//
// The returned value is the *ceiling* for the sleep; the caller applies full
// jitter (a random duration in [0, ceiling]) so fleets/clones don't phase-lock.
func nextBackoff(prev, base, max time.Duration, dialed bool, uptime time.Duration) time.Duration {
	if dialed && uptime >= minHealthyUptime {
		return base
	}
	next := prev * 2
	if next < base { // prev was 0 or sub-base (defensive)
		next = base
	}
	if next > max {
		next = max
	}
	return next
}

// jitter returns a random duration in [0, d] (full jitter). Decorrelating the
// reconnect sleep across a fleet (or several clones of one identity) prevents a
// synchronized thundering herd against the relay.
func (rt *Runtime) jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(d) + 1))
}

// serveOwner maintains one owner's registration, reconnecting with backoff. The
// backoff is UPTIME-GATED: only a connection that stayed healthy for at least
// minHealthyUptime resets it; a flap (or a failed dial) grows it. Every sleep is
// fully jittered.
func (rt *Runtime) serveOwner(ctx context.Context, owner string) {
	backoff := rt.baseBackoff
	for {
		dialed, uptime, err := rt.serveOnce(ctx, owner)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errMachineRevoked) {
			if rt.Logf != nil {
				rt.Logf("event=revoked owner=%s; stopping registration for this owner", short(owner))
			}
			return
		}
		backoff = nextBackoff(backoff, rt.baseBackoff, rt.maxBackoff, dialed, uptime)
		sleep := rt.jitter(backoff)
		code, reason := closeCodeReason(err)
		if rt.Logf != nil {
			rt.Logf("event=disconnect owner=%s uptime=%s code=%d reason=%q backoff=%s",
				short(owner), uptime.Round(time.Millisecond), code, reason, sleep)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(sleep):
		}
	}
}

// serveOnce dials the signaling channel for one owner and serves offers until
// the connection drops. It returns:
//   - dialed: whether the dial itself succeeded (vs. relay down).
//   - uptime: how long the read loop ran (≈ how long the connection stayed
//     healthy). serveOwner gates its backoff on this to tell a genuine idle
//     reconnect (long uptime) from a flap (sub-threshold uptime).
//   - err: the read error, with any websocket.CloseError code+reason preserved
//     so a deliberate relay rejection isn't misread as a network blip.
func (rt *Runtime) serveOnce(ctx context.Context, owner string) (dialed bool, uptime time.Duration, err error) {
	c, response, err := websocket.Dial(ctx, agentSignalURL(rt.cfg.SignalURL, owner, rt.cfg.MachineID), agentDialOptions(rt.cfg.RegistrationSecret, RegistrationAuthForOwner(rt.cfg.Dir, owner)))
	if err != nil {
		if response != nil {
			if response.Body != nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
				response.Body.Close()
			}
			if response.StatusCode == http.StatusGone {
				return false, 0, fmt.Errorf("%w: relay returned %s", errMachineRevoked, response.Status)
			}
		}
		return false, 0, err
	}
	defer c.CloseNow()
	w := &signalWriter{c: c}
	// Reachable for mid-run registry republish (machine rename) while this
	// connection lives.
	defer rt.registerSignaling(owner, w, ctx)()

	// Mark the start of the healthy read loop: uptime is measured from here so a
	// relay that accepts-then-immediately-closes reports a tiny uptime (a flap),
	// while a connection that idles for minutes reports a large one.
	start := time.Now()
	if rt.Logf != nil {
		rt.Logf("event=connected owner=%s", short(owner))
	}

	// Publish the opaque record the owner provisioned during pairing. The agent
	// has no key capable of opening or forging it.
	if blob := RegistryForOwner(rt.cfg.Dir, owner); blob != "" {
		if msg, err := json.Marshal(signal.SignalMsg{Type: signal.TypeRegistry, Registry: blob}); err == nil {
			_ = w.write(ctx, msg)
		}
	}

	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return true, time.Since(start), wrapCloseErr(err)
		}
		var m signal.SignalMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.Type == signal.TypeOffer {
			go func() {
				rt.handleOffer(ctx, w, m, owner)
			}()
		}
	}
}

func short(hexKey string) string {
	if len(hexKey) > 8 {
		return hexKey[:8] + "…"
	}
	return hexKey
}

// wrapCloseErr surfaces a websocket close handshake in the returned error. The
// relay closes with a status+reason for deliberate rejections (e.g. policy
// violation when a registration proof is missing); without this, errors.As is
// the only way to recover it and the human-readable disconnect log would just
// read like a generic read failure. We annotate so even callers that only log
// %v see the code and reason.
func wrapCloseErr(err error) error {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return fmt.Errorf("websocket closed: code=%d reason=%q: %w", int(ce.Code), ce.Reason, err)
	}
	return err
}

// closeCodeReason extracts the websocket close code and reason for the
// structured disconnect log. For a non-close error (a raw network blip, ctx
// cancel) it returns code -1 and the error's message, so the log still carries
// *why* the connection ended rather than discarding it.
func closeCodeReason(err error) (code int, reason string) {
	if err == nil {
		return -1, ""
	}
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return int(ce.Code), ce.Reason
	}
	return -1, err.Error()
}

// iceFor returns ICE servers for an answer. Ephemeral TURN from the signal URL
// wins when offered — a TURN allocation already yields a server-reflexive
// candidate, so a third-party STUN server is omitted (matches client ResolveICE).
func (rt *Runtime) iceFor(ctx context.Context) []peer.ICEServer {
	if turn, err := peer.FetchTURN(ctx, rt.cfg.SignalURL); err == nil && len(turn) > 0 {
		return turn
	}
	return rt.ice
}

func (rt *Runtime) handleOffer(ctx context.Context, w *signalWriter, m signal.SignalMsg, owner string) {
	ownerPub, err := rt.authorizeOffer(owner, m)
	if err != nil {
		return
	}
	if !rt.admit() {
		return
	}
	held := true
	releaseHS := func() {
		if held {
			held = false
			rt.release()
		}
	}
	defer releaseHS()

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

	attachCtx, attachCancel := context.WithCancel(ctx)
	defer attachCancel()
	ans.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if peer.ICESessionDead(s) {
			attachCancel()
		}
	})

	answerSDP, err := peer.CreateAnswerContext(attachCtx, ans, m.SDP)
	if err != nil {
		return
	}
	reply, _ := json.Marshal(signal.SignalMsg{Type: signal.TypeAnswer, Session: m.Session, SDP: answerSDP})
	if err := w.write(ctx, reply); err != nil {
		return
	}

	octx, cancel := context.WithTimeout(attachCtx, 20*time.Second)
	defer cancel()
	var dc *peer.DataChannel
	select {
	case dc = <-opened:
	case <-octx.Done():
		return
	}

	_ = rt.serveAuthenticated(attachCtx, dc, owner, ownerPub, releaseHS)
}

// authorizeOffer verifies pin, binding, SDP-bound owner signature, and session
// replay before any ICE/Pion allocation. Exported-to-package so tests can drive
// the live pin set without a full WebRTC handshake.
func (rt *Runtime) authorizeOffer(owner string, m signal.SignalMsg) ([]byte, error) {
	if !rt.ownerPinned(owner) || m.Session == "" || m.Auth == "" {
		return nil, fmt.Errorf("attach: not authorized")
	}
	ownerPub, err := ownerPubFromBinding(m.Binding, owner)
	if err != nil {
		return nil, err
	}
	auth, err := base64.StdEncoding.DecodeString(m.Auth)
	if err != nil || identity.VerifyAuth(owner, identity.AttachChallenge(m.Session, rt.cfg.MachineID, m.SDP), auth) != nil {
		return nil, fmt.Errorf("attach: bad auth")
	}
	if !rt.acceptAttachSession(owner, m.Session) {
		return nil, fmt.Errorf("attach: replay")
	}
	return ownerPub, nil
}

// serveAuthenticated runs the Noise-KK responder against the pinned owner X25519 key
// and then the PTY session over mc.
//
// The active-session bracket lives HERE — after auth — not at the transport accept:
// pre-auth handshakes (already bounded by admit()) must not inflate the active count
// and starve opt-in auto-update, which defers binary swaps until the agent is idle.
func (rt *Runtime) serveAuthenticated(ctx context.Context, mc peer.MsgConn, owner string, ownerPub []byte, handshakeDone func()) error {
	sess, err := peer.RunResponder(ctx, mc, rt.cfg.HostPriv(), ownerPub)
	if err != nil {
		return err
	}
	if handshakeDone != nil {
		handshakeDone()
	}
	rt.sessionStarted()
	defer rt.sessionEnded()
	pty, err := StartPTY(ctx, rt.launch)
	if err != nil {
		return err
	}
	defer pty.Close()
	// For a tmux launch, push whole-server session/window snapshots so clients
	// render an overview, and accept window+session control commands (select/new/
	// rename/kill, switch-session). Targeting OUR client for cross-session switches
	// needs the PTY child PID (see tmuxClient).
	pid := 0
	if sessionFromLaunch(rt.launch) != "" {
		pid = pty.Pid()
	}
	var windows func() []byte
	if pid > 0 {
		windows = func() []byte { return tmuxSessionsJSON(pid) }
	}
	return RunAgentSession(ctx, mc, sess, pty, rt.machineName(), windows, pid, rt.renameHandler(owner))
}

// agentSignalURL builds ws(s)://host/agent/signal?owner_id=..&machine_id=..
func agentSignalURL(base, owner, machine string) string {
	ws := "ws" + strings.TrimPrefix(base, "http") // http->ws, https->wss
	return ws + "/agent/signal?owner_id=" + url.QueryEscape(owner) + "&machine_id=" + url.QueryEscape(machine)
}

func agentDialOptions(registrationSecret, registrationAuth string) *websocket.DialOptions {
	if registrationSecret == "" && registrationAuth == "" {
		return nil
	}
	return &websocket.DialOptions{
		HTTPHeader: http.Header{
			signal.AgentRegistrationSecretHeader: []string{registrationSecret},
			signal.AgentRegistrationAuthHeader:   []string{registrationAuth},
		},
	}
}

type runtimeError string

func (e runtimeError) Error() string { return string(e) }

const errNoOwner = runtimeError("no paired owner; run `mir pair` first")
const errMachineRevoked = runtimeError("machine revoked by owner")
