# terminal-relay — Plan 3: Agent — a real shell over P2P, locally

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `tr-agent` — the machine side. It connects to `tr-signal`, accepts an attach, negotiates a WebRTC DataChannel, runs the Noise `KK` responder inside it, and bridges that encrypted session to a **real PTY/shell** using the Plan-1 frame protocol. Delivered with a Go end-to-end test (and a `make dev`) where a browser-stand-in attaches through a local `tr-signal` and runs a **real `sh` command over P2P** — provable entirely locally.

**Architecture:** Reuses Plan 1 (`internal/noise`) and Plan 2 (`internal/peer`, `internal/signal`). New: a keystore (host key + machine id + pinned owner), a PTY launcher, a frame-protocol session bridge, and the agent runtime loop (formalizing the Plan-2 spike's inline agent into a real one with a shell instead of an echo).

**Tech Stack:** Go (same module), `github.com/creack/pty` for the PTY, plus existing deps.

**Scoping note (read before starting):** The interactive **QR/token pairing** (NNpsk0) is intentionally **deferred to Plan 4**, where it is co-developed with the real browser (pairing's other end). Plan 3 ships a **dev pre-pin** shortcut (`tr-agent pair-dev --owner-pub <hex>`) — equivalent to dropping an `authorized_keys` line by hand — which is all the local loop needs. Tests pin keys directly via the keystore API.

**tmux note:** the real `tr-agent up` launches `tmux new -A -s main` (persistence) and checks tmux is installed (`brew install tmux` if missing). **All tests and the E2E use `sh`** via the configurable launch command, so they are hermetic and need no tmux.

**Implementer rules (same as Plans 1-2):** Reuse Plan 1/2 packages; do not modify `internal/noise`, `internal/identity`. You MAY adapt `creack/pty` / `pion` / `coder/websocket` API specifics to the installed versions if a call fails to compile, keeping semantics identical; note adaptations. Never configure TURN; never route terminal data through `tr-signal`. Run every command for real; never fake green; close every PeerConnection/PTY on error paths (no leaks).

---

## File structure

```
go/
  internal/peer/pipe.go               # in-memory MsgConn pair (test/util) — small additive helper
  internal/agent/
    store.go        store_test.go     # keystore: host key + machine id/name + pinned owner + signal url
    pty.go          pty_test.go       # PTY launch + Read/Write/Resize (creack/pty); sh in tests
    session.go      session_test.go   # frame-protocol bridge: Noise session <-> PTY (HELLO/DATA/RESIZE)
    runtime.go                        # agent loop: signaling -> answerer -> Noise responder -> session bridge
    e2e_test.go                       # LOCAL E2E: browser-stand-in -> tr-signal -> real sh -> echo over P2P
  cmd/tr-agent/main.go                # enroll | pair-dev | up
Makefile                              # build / test / dev
```

---

## Task 1: Add creack/pty + in-memory MsgConn helper

**Files:** Modify `go/go.mod`; Create `go/internal/peer/pipe.go`, `go/internal/peer/pipe_test.go`

- [ ] **Step 1: Add the PTY dependency**

Run: `cd go && go get github.com/creack/pty@latest`
Expected: `github.com/creack/pty` added to `go.mod` (becomes direct once imported in Task 3).

- [ ] **Step 2: Write the failing test for the pipe helper**

```go
// go/internal/peer/pipe_test.go
package peer

import (
	"context"
	"testing"
	"time"
)

func TestPipeCarriesMessagesBothWays(t *testing.T) {
	a, b := Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := a.Send([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got, err := b.Recv(ctx)
	if err != nil || string(got) != "ping" {
		t.Fatalf("b got %q err %v", got, err)
	}

	if err := b.Send([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	got, err = a.Recv(ctx)
	if err != nil || string(got) != "pong" {
		t.Fatalf("a got %q err %v", got, err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd go && go test ./internal/peer/ -run TestPipe -v`
Expected: FAIL — `undefined: Pipe`.

- [ ] **Step 4: Write `pipe.go`**

```go
// go/internal/peer/pipe.go
package peer

import "context"

// Pipe returns two connected in-memory MsgConns (like net.Pipe but
// message-oriented). For tests that need a DataChannel-shaped conn without WebRTC.
func Pipe() (MsgConn, MsgConn) {
	a2b := make(chan []byte, 64)
	b2a := make(chan []byte, 64)
	return &memConn{in: b2a, out: a2b}, &memConn{in: a2b, out: b2a}
}

type memConn struct {
	in  <-chan []byte
	out chan<- []byte
}

func (m *memConn) Send(b []byte) error {
	cp := make([]byte, len(b))
	copy(cp, b)
	m.out <- cp
	return nil
}

func (m *memConn) Recv(ctx context.Context) ([]byte, error) {
	select {
	case b := <-m.in:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd go && go test ./internal/peer/ -run TestPipe -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go/internal/peer/pipe.go go/internal/peer/pipe_test.go go/go.mod go/go.sum
git commit -m "feat(peer): in-memory MsgConn pipe for tests + creack/pty dep"
```

---

## Task 2: Agent keystore

**Files:** Create `go/internal/agent/store.go`, `go/internal/agent/store_test.go`

- [ ] **Step 1: Write the failing test**

```go
// go/internal/agent/store_test.go
package agent

import (
	"testing"
)

func TestStoreInitPersistsAndReloads(t *testing.T) {
	dir := t.TempDir()

	cfg, err := LoadOrInit(dir, "macbook", "http://localhost:8443")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.HostPriv()) != 32 || len(cfg.HostPub()) != 32 {
		t.Fatalf("host key not initialized: priv=%d pub=%d", len(cfg.HostPriv()), len(cfg.HostPub()))
	}
	if cfg.MachineID == "" {
		t.Fatal("machine id not generated")
	}

	// Reload: same host key + machine id (stable identity).
	cfg2, err := LoadOrInit(dir, "macbook", "http://localhost:8443")
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.MachineID != cfg.MachineID || cfg2.HostPrivHex != cfg.HostPrivHex {
		t.Fatal("identity not stable across reloads")
	}
}

func TestPinOwnerPersists(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := LoadOrInit(dir, "m", "http://localhost:8443")
	if cfg.IsOwnerPinned("deadbeef") {
		t.Fatal("owner should not be pinned yet")
	}
	if err := PinOwner(dir, "deadbeef"); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := LoadOrInit(dir, "m", "http://localhost:8443")
	if !reloaded.IsOwnerPinned("deadbeef") {
		t.Fatal("pinned owner did not persist")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/agent/ -run "TestStore|TestPinOwner" -v`
Expected: FAIL — `undefined: LoadOrInit`.

- [ ] **Step 3: Write `store.go`**

```go
// go/internal/agent/store.go
package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/srcful/terminal-relay/go/internal/noise"
)

// Config is the agent's persisted identity + settings, stored as config.json.
type Config struct {
	HostPrivHex  string   `json:"host_priv"`     // X25519 host private key (hex)
	HostPubHex   string   `json:"host_pub"`      // derived; convenience
	MachineID    string   `json:"machine_id"`    // random, stable
	MachineName  string   `json:"machine_name"`  // human label (travels E2E only)
	SignalURL    string   `json:"signal_url"`    // e.g. http://localhost:8443
	PairedOwners []string `json:"paired_owners"` // hex owner pubkeys
}

func configPath(dir string) string { return filepath.Join(dir, "config.json") }

// LoadOrInit reads config.json from dir, creating a fresh host key + machine id
// on first use. machineName/signalURL update the stored values.
func LoadOrInit(dir, machineName, signalURL string) (*Config, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	cfg := &Config{}
	if data, err := os.ReadFile(configPath(dir)); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
	}
	if cfg.HostPrivHex == "" {
		priv, pub, err := noise.GenerateStatic()
		if err != nil {
			return nil, err
		}
		cfg.HostPrivHex = hex.EncodeToString(priv)
		cfg.HostPubHex = hex.EncodeToString(pub)
	}
	if cfg.MachineID == "" {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		cfg.MachineID = hex.EncodeToString(b)
	}
	cfg.MachineName = machineName
	cfg.SignalURL = signalURL
	if err := save(dir, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func save(dir string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(dir), data, 0o600)
}

// PinOwner adds an owner pubkey (hex) to the trusted set and persists it.
func PinOwner(dir, ownerPubHex string) error {
	cfg := &Config{}
	data, err := os.ReadFile(configPath(dir))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return err
	}
	if !cfg.IsOwnerPinned(ownerPubHex) {
		cfg.PairedOwners = append(cfg.PairedOwners, ownerPubHex)
	}
	return save(dir, cfg)
}

func (c *Config) IsOwnerPinned(ownerPubHex string) bool {
	for _, o := range c.PairedOwners {
		if o == ownerPubHex {
			return true
		}
	}
	return false
}

func (c *Config) HostPriv() []byte { b, _ := hex.DecodeString(c.HostPrivHex); return b }
func (c *Config) HostPub() []byte  { b, _ := hex.DecodeString(c.HostPubHex); return b }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/agent/ -run "TestStore|TestPinOwner" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/store.go go/internal/agent/store_test.go
git commit -m "feat(agent): keystore (host key, machine id, pinned owners)"
```

---

## Task 3: PTY launcher

**Files:** Create `go/internal/agent/pty.go`, `go/internal/agent/pty_test.go`

- [ ] **Step 1: Write the failing test (uses `sh`, not tmux)**

```go
// go/internal/agent/pty_test.go
package agent

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestPTYRunsShellCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	p, err := StartPTY(ctx, []string{"sh"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if _, err := p.Write([]byte("echo terminal_relay_marker\n")); err != nil {
		t.Fatal(err)
	}

	// Read until we see the marker echoed by the shell.
	deadline := time.Now().Add(8 * time.Second)
	var acc bytes.Buffer
	buf := make([]byte, 1024)
	for time.Now().Before(deadline) {
		_ = p.SetReadDeadlineSoon()
		n, _ := p.Read(buf)
		if n > 0 {
			acc.Write(buf[:n])
			if bytes.Contains(acc.Bytes(), []byte("terminal_relay_marker")) {
				return // success
			}
		}
	}
	t.Fatalf("marker never seen; got:\n%s", acc.String())
}

func TestPTYResizeDoesNotError(t *testing.T) {
	ctx := context.Background()
	p, err := StartPTY(ctx, []string{"sh"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Resize(100, 30); err != nil {
		t.Fatalf("resize: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/agent/ -run TestPTY -v`
Expected: FAIL — `undefined: StartPTY`.

- [ ] **Step 3: Write `pty.go`**

```go
// go/internal/agent/pty.go
package agent

import (
	"context"
	"os"
	"os/exec"
	"time"

	"github.com/creack/pty"
)

// PTY is a pseudo-terminal running a command (a shell, or tmux in production).
type PTY struct {
	f   *os.File
	cmd *exec.Cmd
}

// StartPTY launches argv behind a PTY. Production passes
// {"tmux","new","-A","-s","main"}; tests pass {"sh"}.
func StartPTY(ctx context.Context, argv []string) (*PTY, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	f, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	return &PTY{f: f, cmd: cmd}, nil
}

func (p *PTY) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *PTY) Write(b []byte) (int, error) { return p.f.Write(b) }

// SetReadDeadlineSoon nudges a short read deadline so a polling read loop in a
// test does not block forever. Best-effort (ignored if unsupported).
func (p *PTY) SetReadDeadlineSoon() error {
	return p.f.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
}

func (p *PTY) Resize(cols, rows uint16) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: cols, Rows: rows})
}

func (p *PTY) Close() error {
	_ = p.f.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	return nil
}

// TmuxInstalled reports whether tmux is on PATH (checked before a real `up`).
func TmuxInstalled() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/agent/ -run TestPTY -v`
Expected: PASS (uses `sh`, no tmux needed).

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/pty.go go/internal/agent/pty_test.go go/go.mod go/go.sum
git commit -m "feat(agent): PTY launcher (creack/pty) with resize"
```

---

## Task 4: Frame-protocol session bridge

**Files:** Create `go/internal/agent/session.go`, `go/internal/agent/session_test.go`

- [ ] **Step 1: Write the failing test (real `sh` PTY, real Noise sessions)**

```go
// go/internal/agent/session_test.go
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

func TestSessionBridgeRunsRealShellOverNoise(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	agentPriv, agentPub, _ := noise.GenerateStatic()
	browserPriv, browserPub, _ := noise.GenerateStatic()

	browserMC, agentMC := peer.Pipe()

	// Establish Noise KK over the pipe (browser=initiator, agent=responder).
	var browserSess *noise.Session
	done := make(chan error, 1)
	go func() {
		s, err := peer.RunResponder(ctx, agentMC, agentPriv, browserPub)
		if err != nil {
			done <- err
			return
		}
		p, err := StartPTY(ctx, []string{"sh"})
		if err != nil {
			done <- err
			return
		}
		defer p.Close()
		done <- RunAgentSession(ctx, agentMC, s, p, "test-machine")
	}()
	bs, err := peer.RunInitiator(ctx, browserMC, browserPriv, agentPub)
	if err != nil {
		t.Fatal(err)
	}
	browserSess = bs

	// Browser receives HELLO first.
	hello := recvFrame(t, ctx, browserMC, browserSess)
	htype, hpayload, _ := noise.DecodeFrame(hello)
	if htype != noise.FrameHello {
		t.Fatalf("expected HELLO, got type %d", htype)
	}
	var meta map[string]string
	_ = json.Unmarshal(hpayload, &meta)
	if meta["name"] != "test-machine" {
		t.Fatalf("HELLO name = %q", meta["name"])
	}

	// Browser sends a command; expects to see it echoed by the real shell.
	sendData(t, ctx, browserMC, browserSess, []byte("echo bridge_works\n"))

	deadline := time.Now().Add(8 * time.Second)
	var acc bytes.Buffer
	for time.Now().Before(deadline) {
		frame := recvFrame(t, ctx, browserMC, browserSess)
		typ, payload, err := noise.DecodeFrame(frame)
		if err != nil || typ != noise.FrameData {
			continue
		}
		acc.Write(payload)
		if bytes.Contains(acc.Bytes(), []byte("bridge_works")) {
			return // success
		}
	}
	t.Fatalf("never saw command output; got:\n%s", acc.String())
}

func sendData(t *testing.T, ctx context.Context, mc peer.MsgConn, sess *noise.Session, data []byte) {
	t.Helper()
	ct, err := sess.Encrypt(noise.EncodeData(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := mc.Send(ct); err != nil {
		t.Fatal(err)
	}
}

func recvFrame(t *testing.T, ctx context.Context, mc peer.MsgConn, sess *noise.Session) []byte {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ct, err := mc.Recv(rctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	pt, err := sess.Decrypt(ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	return pt
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/agent/ -run TestSessionBridge -v`
Expected: FAIL — `undefined: RunAgentSession`.

- [ ] **Step 3: Write `session.go`**

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/agent/ -run TestSessionBridge -v`
Expected: PASS — a real `sh` runs `echo bridge_works`, the output flows back through Noise.

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/session.go go/internal/agent/session_test.go
git commit -m "feat(agent): frame-protocol session bridge (Noise <-> shell)"
```

---

## Task 5: Agent runtime (signaling -> answerer -> responder -> shell)

**Files:** Create `go/internal/agent/runtime.go`

- [ ] **Step 1: Write `runtime.go`**

```go
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
```

- [ ] **Step 2: Verify it compiles**

Run: `cd go && go build ./internal/agent/`
Expected: builds (exercised end-to-end in Task 7).

- [ ] **Step 3: Commit**

```bash
git add go/internal/agent/runtime.go
git commit -m "feat(agent): runtime loop (signaling -> answerer -> responder -> shell)"
```

---

## Task 6: `tr-agent` binary

**Files:** Create `go/cmd/tr-agent/main.go`

- [ ] **Step 1: Write `main.go`**

```go
// go/cmd/tr-agent/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/srcful/terminal-relay/go/internal/agent"
)

func defaultDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".terminal-relay")
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "enroll":
		cmdEnroll(os.Args[2:])
	case "pair-dev":
		cmdPairDev(os.Args[2:])
	case "up":
		cmdUp(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tr-agent <enroll|pair-dev|up> [flags]")
	os.Exit(2)
}

func cmdEnroll(args []string) {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	name := fs.String("name", hostname(), "machine display name")
	signalURL := fs.String("signal", "http://localhost:8443", "signaling server base URL")
	_ = fs.Parse(args)

	cfg, err := agent.LoadOrInit(*dir, *name, *signalURL)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("enrolled %q\n  machine_id: %s\n  host_pub:   %s\n  signal:     %s\n",
		cfg.MachineName, cfg.MachineID, cfg.HostPubHex, cfg.SignalURL)
	fmt.Println("\nNext: pair an owner. For local dev:")
	fmt.Printf("  tr-agent pair-dev --owner-pub <hex>\n")
	if !agent.TmuxInstalled() {
		fmt.Println("\nwarning: tmux is not installed (needed for persistent sessions): brew install tmux")
	}
}

func cmdPairDev(args []string) {
	fs := flag.NewFlagSet("pair-dev", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	ownerPub := fs.String("owner-pub", "", "owner X25519 public key (hex) to trust")
	_ = fs.Parse(args)
	if *ownerPub == "" {
		fatal(fmt.Errorf("--owner-pub is required"))
	}
	if err := agent.PinOwner(*dir, strings.ToLower(*ownerPub)); err != nil {
		fatal(err)
	}
	fmt.Printf("pinned owner %s\n", *ownerPub)
}

func cmdUp(args []string) {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	name := fs.String("name", hostname(), "machine display name")
	signalURL := fs.String("signal", "http://localhost:8443", "signaling server base URL")
	shell := fs.String("shell", "tmux:new:-A:-s:main", "launch command, ':'-separated")
	_ = fs.Parse(args)

	cfg, err := agent.LoadOrInit(*dir, *name, *signalURL)
	if err != nil {
		fatal(err)
	}
	launch := strings.Split(*shell, ":")
	if launch[0] == "tmux" && !agent.TmuxInstalled() {
		fatal(fmt.Errorf("tmux not installed (brew install tmux), or pass --shell sh"))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rt := agent.NewRuntime(cfg, launch, nil) // nil STUN = host candidates (local); set for real NAT
	fmt.Printf("tr-agent up: machine %s, signaling %s\n", cfg.MachineID, cfg.SignalURL)
	if err := rt.Up(ctx); err != nil && ctx.Err() == nil {
		fatal(err)
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "machine"
	}
	return h
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
```

- [ ] **Step 2: Build + smoke**

Run:
```bash
cd go && go build -o /tmp/tr-agent ./cmd/tr-agent && /tmp/tr-agent enroll --dir /tmp/tr-agent-test --signal http://localhost:8443
```
Expected: prints `enrolled`, a machine_id, a host_pub, and the pair-dev hint.

- [ ] **Step 3: Commit**

```bash
git add go/cmd/tr-agent/main.go
git commit -m "feat(agent): tr-agent binary (enroll | pair-dev | up)"
```

---

## Task 7: Local end-to-end test — real shell over P2P through `tr-signal`

The "test soon" deliverable: a browser-stand-in attaches through a local
`tr-signal` and runs a real `sh` command over a direct P2P DataChannel.

**Files:** Create `go/internal/agent/e2e_test.go`

- [ ] **Step 1: Write the test**

```go
// go/internal/agent/e2e_test.go
package agent

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

func TestEndToEndRealShellOverP2P(t *testing.T) {
	srv := httptest.NewServer(signal.New().Handler())
	defer srv.Close()

	// Owner (browser) identity + agent keystore with that owner pinned.
	ownerPriv, ownerPub, _ := noise.GenerateStatic()
	dir := t.TempDir()
	cfg, err := LoadOrInit(dir, "e2e-machine", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := PinOwner(dir, hex.EncodeToString(ownerPub)); err != nil {
		t.Fatal(err)
	}
	cfg, _ = LoadOrInit(dir, "e2e-machine", srv.URL) // reload with the pinned owner

	// Start the agent runtime (sh, not tmux; nil STUN = localhost host candidates).
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	go func() { _ = rt.Up(ctx) }()
	time.Sleep(300 * time.Millisecond) // let the agent register

	// Browser-stand-in: attach, offer, await answer, open DataChannel, Noise init.
	bws := "ws" + strings.TrimPrefix(srv.URL, "http") +
		"/attach?owner_id=" + url.QueryEscape(hex.EncodeToString(ownerPub)) +
		"&machine_id=" + url.QueryEscape(cfg.MachineID)
	bc, _, err := websocket.Dial(ctx, bws, nil)
	if err != nil {
		t.Fatal(err)
	}
	off, opened, err := peer.NewOfferer(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer off.Close()

	offerSDP, err := peer.CreateOffer(off)
	if err != nil {
		t.Fatal(err)
	}
	offerMsg, _ := json.Marshal(signal.SignalMsg{Type: signal.TypeOffer, SDP: offerSDP})
	if err := bc.Write(ctx, websocket.MessageText, offerMsg); err != nil {
		t.Fatal(err)
	}
	_, data, err := bc.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ans signal.SignalMsg
	if json.Unmarshal(data, &ans) != nil || ans.Type != signal.TypeAnswer {
		t.Fatalf("expected answer, got %s", string(data))
	}
	if err := peer.AcceptAnswer(off, ans.SDP); err != nil {
		t.Fatal(err)
	}

	var dc *peer.DataChannel
	select {
	case dc = <-opened:
	case <-ctx.Done():
		t.Fatal("DataChannel never opened")
	}

	agentHostPub := cfg.HostPub()
	sess, err := peer.RunInitiator(ctx, dc, ownerPriv, agentHostPub)
	if err != nil {
		t.Fatalf("initiator handshake: %v", err)
	}

	// First frame must be HELLO with the machine name.
	hello := recvFrame(t, ctx, dc, sess)
	htype, hpayload, _ := noise.DecodeFrame(hello)
	if htype != noise.FrameHello {
		t.Fatalf("expected HELLO, got %d", htype)
	}
	var meta map[string]string
	_ = json.Unmarshal(hpayload, &meta)
	if meta["name"] != "e2e-machine" {
		t.Fatalf("HELLO name = %q", meta["name"])
	}

	// Run a real command over the encrypted P2P channel.
	sendData(t, ctx, dc, sess, []byte("echo E2E_P2P_OK\n"))
	deadline := time.Now().Add(10 * time.Second)
	var acc bytes.Buffer
	for time.Now().Before(deadline) {
		frame := recvFrame(t, ctx, dc, sess)
		typ, payload, err := noise.DecodeFrame(frame)
		if err != nil || typ != noise.FrameData {
			continue
		}
		acc.Write(payload)
		if bytes.Contains(acc.Bytes(), []byte("E2E_P2P_OK")) {
			return // SUCCESS: real shell, over P2P, through the signaling server
		}
	}
	t.Fatalf("never saw command output; got:\n%s", acc.String())
}
```

- [ ] **Step 2: Run the E2E test**

Run: `cd go && go test ./internal/agent/ -run TestEndToEnd -v`
Expected: PASS — a browser-stand-in pairs (pre-pinned), attaches through `tr-signal`, opens a P2P DataChannel, runs `echo E2E_P2P_OK` in a real shell, and reads it back, all Noise-encrypted.

- [ ] **Step 3: Run the whole Go suite (with race)**

Run: `cd go && go test ./... && go test -race -count=1 ./internal/agent/ ./internal/peer/ ./internal/signal/`
Expected: PASS, race-free.

- [ ] **Step 4: Commit**

```bash
git add go/internal/agent/e2e_test.go
git commit -m "test(agent): local E2E — real shell over P2P through tr-signal"
```

---

## Task 8: `make dev` + README

**Files:** Create `Makefile`; modify `README.md`

- [ ] **Step 1: Write `Makefile`**

```makefile
# terminal-relay — local dev

.PHONY: build test race dev

build:
	cd go && go build -o ../bin/tr-signal ./cmd/tr-signal
	cd go && go build -o ../bin/tr-agent ./cmd/tr-agent

test:
	cd go && go test ./...

race:
	cd go && go test -race -count=1 ./...

# dev: run the signaling server + an agent locally.
# 1) `make build`
# 2) `make dev` (starts tr-signal, enrolls + runs an agent against it)
# Pair an owner the web client will use with: bin/tr-agent pair-dev --owner-pub <hex>
dev: build
	@echo "starting tr-signal on :8443 ..."
	@./bin/tr-signal --addr :8443 & echo $$! > /tmp/tr-signal.pid
	@sleep 1
	@./bin/tr-agent enroll --dir /tmp/tr-agent-dev --signal http://localhost:8443 || true
	@echo "agent enrolled. Pair an owner, then: ./bin/tr-agent up --dir /tmp/tr-agent-dev --shell sh"
	@echo "stop the signaling server with: kill \`cat /tmp/tr-signal.pid\`"
```

- [ ] **Step 2: Verify build + test targets**

Run: `make build && make test`
Expected: binaries appear in `bin/`; all Go tests pass.

- [ ] **Step 3: Append an agent section to `README.md`**

```markdown
## Agent + local dev (Plan 3)

`go/cmd/tr-agent` — the machine side. It registers on `tr-signal`, accepts an
attach, opens a P2P WebRTC DataChannel, runs the Noise `KK` responder, and bridges
it to a real PTY. Production launches `tmux new -A -s main` (persistence; install
with `brew install tmux`); `--shell sh` runs a plain shell.

Local loop:

    make build
    ./bin/tr-signal --addr :8443 &
    ./bin/tr-agent enroll --signal http://localhost:8443
    ./bin/tr-agent pair-dev --owner-pub <owner-hex>   # dev pre-pin (QR pairing is Plan 4)
    ./bin/tr-agent up --shell sh

The full path — browser-stand-in → `tr-signal` → real shell over P2P — is proven
hermetically by `go test ./internal/agent/ -run TestEndToEnd`.

Note: interactive QR/token pairing is built in Plan 4 alongside the browser.
```

- [ ] **Step 4: Commit**

```bash
git add Makefile README.md
git commit -m "feat: make dev + agent docs for the local loop"
```

---

## Self-review (completed during planning)

- **Spec coverage:** Implements the spec's component #1 (`tr-agent up`: signaling →
  WebRTC DataChannel → Noise `KK` responder inside → `tmux`/PTY bridge with the
  frame protocol; HELLO carries the machine name E2E). The keystore implements the
  agent's static host key + pinned `owner_id`. `enroll` initializes identity and
  checks tmux. Persistent-session via tmux is the production launch command;
  reconnect/resume is inherent to `tmux new -A` (re-attach) and is exercised
  manually (the spec's acceptance) — the automated E2E uses `sh` for hermeticity.
- **Deferred (explicit):** interactive QR/token pairing (NNpsk0) → Plan 4 (its
  other end is the browser); the dev pre-pin (`pair-dev`) covers the local loop.
  The Plan-2 lower-severity follow-ups: the agent runtime now Closes its
  PeerConnection on every error path (addresses the pion-leak finding for the real
  caller); the signaling-server answer-ownership check remains a server-side
  hardening note for when multiple agents/pairing land.
- **Placeholder scan:** no TBD/TODO; complete code in every code step; exact
  commands + expected output in every run step.
- **Type consistency:** `Config`/`LoadOrInit`/`PinOwner`/`IsOwnerPinned`/`HostPriv`/
  `HostPub`, `StartPTY`/`PTY.Read/Write/Resize/Close`/`TmuxInstalled`, `Shell`,
  `RunAgentSession`/`send`, `Runtime`/`NewRuntime`/`Up`/`handleOffer`/
  `agentSignalURL`, reuse of `peer.Pipe/NewOfferer/NewAnswerer/CreateOffer/
  CreateAnswer/AcceptAnswer/RunInitiator/RunResponder/DataChannel`, `noise`
  frame codec + `Session`, and `signal.SignalMsg`/`Type*` constants are consistent
  across files and tests. `sendData`/`recvFrame` helpers are shared between
  `session_test.go` and `e2e_test.go` (same package `agent`).
- **Hermetic tests:** every test uses `sh` (no tmux) and `nil` STUN (localhost host
  candidates, no network). The real `tmux`/STUN paths are config values, not code
  changes.
```
