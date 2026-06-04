# terminal-relay — Plan 4b: multi-machine focus-switcher (`tr` mux)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** turn `tr` into a **cross-machine multiplexer** — `tr attach m1 m2 m3`
holds one P2P session per machine, shows one in focus, and switches between them
with a prefix hotkey (Ctrl-] then a number / `n` / `q`). "Like tmux, but the
terminals run on different machines." Each machine still runs its own tmux for
per-host windows + persistence; this layer only chooses *which machine* is in focus.

**Architecture:** built directly on Plan 4's `Attach` + the frame protocol. New: a
concurrency-safe per-session sender (fixing a latent nonce race), a TTY-free `Mux`
core (focus state, output routing, stdin/prefix parsing, lifecycle), and the
raw-mode wiring that attaches N machines and runs the Mux.

**Tech Stack:** Go (same module), existing deps. No new dependencies.

**Implementer rules (same as before):** Reuse Plans 1-4; do not modify
`internal/noise`/`internal/identity`. Keep invariants: strict P2P, no TURN, no
terminal data through `tr-signal`, Mux **core** testable without a TTY. Run every
command for real; never fake green; run the race detector where noted.

---

## Critical correctness note (read first)

`noise.Session.Encrypt` increments an internal nonce counter and is **not
safe for concurrent use**. Concurrent `Encrypt` calls on one session race the
counter and can reuse a nonce — which breaks ChaCha20-Poly1305 entirely. Today
`ClientBridge` (Plan 4) calls `sess.Encrypt` from *two* goroutines (stdin + resize)
on the same session — a latent bug. The multiplexer adds more senders per session
(stdin + resize + switch-nudge). **Task 1 introduces one serialized sender per
session and routes every encrypt through it.** Decrypt stays single-goroutine per
session (one reader), so it needs no lock.

## File structure

```
go/
  internal/client/
    sender.go     sender_test.go   # serialized per-session encrypt+send; ClientBridge retrofitted to use it
    mux.go        mux_test.go       # Mux: focus, output routing, prefix parsing, lifecycle
    muxterm.go                      # RunInteractiveMux: raw mode + attach N + run Mux (build-only)
    e2e_mux_test.go                 # mux over 2 real tr-agents through tr-signal: switch focus, assert routing
  cmd/tr/main.go                    # `tr attach <m1> [m2 ...]` -> single attach or mux
```

---

## Task 1: Serialized sender + ClientBridge retrofit (fix the nonce race)

**Files:** Create `go/internal/client/sender.go`, `go/internal/client/sender_test.go`; Modify `go/internal/client/bridge.go`

- [ ] **Step 1: Write the failing race test**

```go
// go/internal/client/sender_test.go
package client

import (
	"context"
	"sync"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// Run with -race: concurrent sends through one sender must not race the Noise
// nonce, and the peer must decrypt every frame in order without auth failures.
func TestSenderSerializesConcurrentEncrypts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	aPriv, aPub, _ := noise.GenerateStatic()
	bPriv, bPub, _ := noise.GenerateStatic()
	clientMC, agentMC := peer.Pipe()

	const N = 200
	got := make(chan int, N)
	go func() {
		sess, err := peer.RunResponder(ctx, agentMC, aPriv, bPub)
		if err != nil {
			return
		}
		for i := 0; i < N; i++ {
			ct, err := agentMC.Recv(ctx)
			if err != nil {
				return
			}
			pt, err := sess.Decrypt(ct) // single decrypter: must never auth-fail
			if err != nil {
				t.Errorf("decrypt failed at %d: %v", i, err)
				return
			}
			_, payload, _ := noise.DecodeFrame(pt)
			got <- len(payload)
		}
	}()

	sess, err := peer.RunInitiator(ctx, clientMC, bPriv, aPub)
	if err != nil {
		t.Fatal(err)
	}
	s := newSender(clientMC, sess)

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.send(noise.EncodeData([]byte("x")))
		}()
	}
	wg.Wait()
	for i := 0; i < N; i++ {
		<-got // all N decrypted successfully (no nonce reuse / auth failure)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/client/ -run TestSenderSerializes -race -v`
Expected: FAIL — `undefined: newSender` (and, if you temporarily wire raw concurrent `sess.Encrypt`, the race detector flags it — that is the bug we are preventing).

- [ ] **Step 3: Write `sender.go`**

```go
// go/internal/client/sender.go
package client

import (
	"sync"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// sender serializes encrypt+send for one Noise session. noise.Session.Encrypt
// mutates a nonce counter and is NOT safe for concurrent use, so every frame for
// a given session must go through one sender.
type sender struct {
	mc   peer.MsgConn
	sess *noise.Session
	mu   sync.Mutex
}

func newSender(mc peer.MsgConn, sess *noise.Session) *sender {
	return &sender{mc: mc, sess: sess}
}

// send encrypts an already-framed payload and writes it, holding the lock across
// both so the nonce order matches the ciphertext order on the wire.
func (s *sender) send(framed []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ct, err := s.sess.Encrypt(framed)
	if err != nil {
		return err
	}
	return s.mc.Send(ct)
}
```

- [ ] **Step 4: Retrofit `bridge.go` to use the sender**

In `bridge.go`, replace the package-level `sendFrame` usage with a single `sender`
shared by the stdin and resize goroutines. Concretely, change `ClientBridge` so it
constructs `s := newSender(mc, sess)` once and both goroutines call `s.send(...)`;
remove the old `sendFrame` helper (or keep it only if nothing else uses it).

```go
// in ClientBridge, replace the body's frame sends:
func ClientBridge(ctx context.Context, in io.Reader, out io.Writer, resizes <-chan Size, initial Size, mc peer.MsgConn, sess *noise.Session) error {
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

	// peer -> stdout (skip HELLO) — unchanged from Plan 4
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
```
Delete the now-unused `func sendFrame(...)` from `bridge.go` if present.

- [ ] **Step 5: Run the tests under -race**

Run: `cd go && go test ./internal/client/ -race -count=1`
Expected: PASS — `TestSenderSerializes...`, `TestClientBridge...`, the E2E, and the store tests, all race-free.

- [ ] **Step 6: Commit**

```bash
git add go/internal/client/sender.go go/internal/client/sender_test.go go/internal/client/bridge.go
git commit -m "fix(client): serialize per-session encrypt (Noise nonce is not concurrency-safe)"
```

---

## Task 2: Mux core (focus, routing, prefix parsing, lifecycle)

**Files:** Create `go/internal/client/mux.go`, `go/internal/client/mux_test.go`

- [ ] **Step 1: Write the failing test (two fake agents over pipes)**

```go
// go/internal/client/mux_test.go
package client

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// fakeAgent: Noise responder. On DATA "emit:<x>" it sends DATA "<x>". A trigger
// channel makes it emit out-of-band (to test that non-focused output is dropped).
type fakeAgent struct {
	mc      peer.MsgConn
	trigger chan string
}

func startFakeAgent(t *testing.T, ctx context.Context, agentPriv, clientPub []byte) (*fakeAgent, []byte) {
	t.Helper()
	clientMC, agentMC := peer.Pipe()
	fa := &fakeAgent{mc: clientMC, trigger: make(chan string, 8)}
	go func() {
		sess, err := peer.RunResponder(ctx, agentMC, agentPriv, clientPub)
		if err != nil {
			return
		}
		send := func(b []byte) { ct, _ := sess.Encrypt(noise.EncodeData(b)); _ = agentMC.Send(ct) }
		go func() {
			for {
				select {
				case s := <-fa.trigger:
					send([]byte(s))
				case <-ctx.Done():
					return
				}
			}
		}()
		for {
			ct, err := agentMC.Recv(ctx)
			if err != nil {
				return
			}
			pt, err := sess.Decrypt(ct)
			if err != nil {
				return
			}
			typ, payload, _ := noise.DecodeFrame(pt)
			if typ == noise.FrameData && bytes.HasPrefix(payload, []byte("emit:")) {
				send(payload[len("emit:"):])
			}
		}
	}()
	return fa, nil
}

func TestMuxRoutesToFocusAndDropsBackground(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Build two client sessions, one per fake agent.
	mk := func() (*MuxSession, *fakeAgent) {
		aPriv, aPub, _ := noise.GenerateStatic()
		cPriv, cPub, _ := noise.GenerateStatic()
		fa, _ := startFakeAgent(t, ctx, aPriv, cPub)
		sess, err := peer.RunInitiator(ctx, fa.mc, cPriv, aPub)
		if err != nil {
			t.Fatal(err)
		}
		return &MuxSession{Name: "", MC: fa.mc, Sess: sess}, fa
	}
	s0, fa0 := mk()
	s0.Name = "box0"
	s1, fa1 := mk()
	s1.Name = "box1"

	out := &syncWriter{} // from bridge_test.go (same package)
	in := newBlockingReader()
	mux := NewMux([]*MuxSession{s0, s1}, out, DefaultPrefix, Size{Cols: 80, Rows: 24})
	go func() { _ = mux.Run(ctx, in, make(chan Size)) }()

	// Focus starts on box0: ask box0 to emit, see it.
	in.feed([]byte("emit:HELLO0\n"))
	waitFor(t, out, "HELLO0")

	// Switch to box1 (prefix + '2'), ask box1 to emit, see it.
	in.feed([]byte{DefaultPrefix, '2'})
	in.feed([]byte("emit:HELLO1\n"))
	waitFor(t, out, "HELLO1")

	// While focused on box1, box0 emits out-of-band: it must NOT reach out.
	before := out.String()
	fa0.trigger <- "GHOST0"
	time.Sleep(300 * time.Millisecond)
	if bytes.Contains([]byte(out.String()[len(before):]), []byte("GHOST0")) {
		t.Fatal("background (non-focused) machine output leaked to the terminal")
	}
}

func waitFor(t *testing.T, out *syncWriter, want string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains([]byte(out.String()), []byte(want)) {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("never saw %q in out; got:\n%s", want, out.String())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/client/ -run TestMuxRoutes -v`
Expected: FAIL — `undefined: NewMux` / `undefined: MuxSession` / `undefined: DefaultPrefix`.

- [ ] **Step 3: Write `mux.go`**

```go
// go/internal/client/mux.go
package client

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// DefaultPrefix is the switch key (Ctrl-]). Press it, then: a digit 1-9 to focus
// that machine, 'n' for next, 'q' to quit, or the prefix again to send a literal.
const DefaultPrefix byte = 0x1d

// MuxSession is one attached machine.
type MuxSession struct {
	Name string
	MC   peer.MsgConn
	Sess *noise.Session
	snd  *sender
}

// Mux multiplexes several machine sessions onto one local terminal: only the
// focused machine's output reaches the screen; keystrokes go to the focused
// machine; a prefix hotkey switches focus.
type Mux struct {
	sessions []*MuxSession
	out      io.Writer
	prefix   byte

	mu    sync.Mutex // guards focus, size, dead, and writes to out
	focus int
	size  Size
	dead  []bool

	quit     chan struct{}
	quitOnce sync.Once
}

func NewMux(sessions []*MuxSession, out io.Writer, prefix byte, initial Size) *Mux {
	if prefix == 0 {
		prefix = DefaultPrefix
	}
	for _, s := range sessions {
		s.snd = newSender(s.MC, s.Sess)
	}
	return &Mux{
		sessions: sessions,
		out:      out,
		prefix:   prefix,
		size:     initial,
		dead:     make([]bool, len(sessions)),
		quit:     make(chan struct{}),
	}
}

// Run drives the mux until quit, ctx cancel, or all machines disconnect.
func (m *Mux) Run(ctx context.Context, in io.Reader, resizes <-chan Size) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i := range m.sessions {
		_ = m.sessions[i].snd.send(noise.EncodeResize(m.size.Cols, m.size.Rows))
	}
	m.setTitle(m.sessions[m.focus].Name)

	for i := range m.sessions {
		go m.readSession(ctx, i)
	}
	go m.resizeLoop(ctx, resizes)
	return m.readStdin(ctx, in)
}

func (m *Mux) readSession(ctx context.Context, i int) {
	s := m.sessions[i]
	for {
		ct, err := s.MC.Recv(ctx)
		if err != nil {
			m.onSessionEnd(i)
			return
		}
		pt, err := s.Sess.Decrypt(ct)
		if err != nil {
			m.onSessionEnd(i)
			return
		}
		typ, payload, err := noise.DecodeFrame(pt)
		if err != nil {
			continue
		}
		if typ == noise.FrameData {
			m.mu.Lock()
			if i == m.focus {
				_, _ = m.out.Write(payload)
			}
			m.mu.Unlock()
		}
	}
}

func (m *Mux) resizeLoop(ctx context.Context, resizes <-chan Size) {
	for {
		select {
		case sz := <-resizes:
			m.mu.Lock()
			m.size = sz
			f := m.focus
			m.mu.Unlock()
			_ = m.sessions[f].snd.send(noise.EncodeResize(sz.Cols, sz.Rows))
		case <-ctx.Done():
			return
		case <-m.quit:
			return
		}
	}
}

func (m *Mux) readStdin(ctx context.Context, in io.Reader) error {
	buf := make([]byte, 4096)
	armed := false
	for {
		n, err := in.Read(buf)
		for k := 0; k < n; k++ {
			b := buf[k]
			if armed {
				armed = false
				m.command(b)
				continue
			}
			if b == m.prefix {
				armed = true
				continue
			}
			m.forward(b)
		}
		select {
		case <-m.quit:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err != nil {
			return err
		}
	}
}

func (m *Mux) forward(b byte) {
	m.mu.Lock()
	f := m.focus
	m.mu.Unlock()
	_ = m.sessions[f].snd.send(noise.EncodeData([]byte{b}))
}

func (m *Mux) command(b byte) {
	switch {
	case b >= '1' && b <= '9':
		m.switchTo(int(b - '1'))
	case b == 'n':
		m.mu.Lock()
		next := m.nextLiveLocked()
		m.mu.Unlock()
		if next >= 0 {
			m.switchTo(next)
		}
	case b == 'q':
		m.quitOnce.Do(func() { close(m.quit) })
	case b == m.prefix:
		m.mu.Lock()
		f := m.focus
		m.mu.Unlock()
		_ = m.sessions[f].snd.send(noise.EncodeData([]byte{m.prefix}))
	}
}

func (m *Mux) switchTo(idx int) {
	m.mu.Lock()
	if idx < 0 || idx >= len(m.sessions) || m.dead[idx] || idx == m.focus {
		m.mu.Unlock()
		return
	}
	m.focus = idx
	size := m.size
	_, _ = io.WriteString(m.out, "\x1b[2J\x1b[H") // clear + home, under lock with other out writes
	m.mu.Unlock()

	m.setTitle(m.sessions[idx].Name)
	// Nudge the newly-focused machine's tmux to redraw the current screen.
	_ = m.sessions[idx].snd.send(noise.EncodeResize(size.Cols, size.Rows))
}

func (m *Mux) onSessionEnd(i int) {
	m.mu.Lock()
	if m.dead[i] {
		m.mu.Unlock()
		return
	}
	m.dead[i] = true
	fmt.Fprintf(m.out, "\r\n[tr] %s disconnected\r\n", m.sessions[i].Name)
	wasFocus := i == m.focus
	next := m.nextLiveLocked()
	m.mu.Unlock()

	if next < 0 {
		m.quitOnce.Do(func() { close(m.quit) })
		return
	}
	if wasFocus {
		m.switchTo(next)
	}
}

// nextLiveLocked returns the next non-dead session after focus (wrapping), or -1.
func (m *Mux) nextLiveLocked() int {
	for off := 1; off <= len(m.sessions); off++ {
		j := (m.focus + off) % len(m.sessions)
		if !m.dead[j] {
			return j
		}
	}
	return -1
}

func (m *Mux) setTitle(name string) {
	m.mu.Lock()
	_, _ = io.WriteString(m.out, "\x1b]0;tr: "+name+"\x07")
	m.mu.Unlock()
}
```

- [ ] **Step 4: Run the test (and under -race)**

Run: `cd go && go test ./internal/client/ -run TestMuxRoutes -v && go test ./internal/client/ -run TestMuxRoutes -race -count=1`
Expected: PASS — focused routing works, background output is dropped, no data races.

- [ ] **Step 5: Commit**

```bash
git add go/internal/client/mux.go go/internal/client/mux_test.go
git commit -m "feat(client): cross-machine focus-switcher mux core"
```

---

## Task 3: Raw-mode mux wiring + `tr attach <m1> [m2 ...]`

**Files:** Create `go/internal/client/muxterm.go`; Modify `go/cmd/tr/main.go`

- [ ] **Step 1: Write `muxterm.go`**

```go
// go/internal/client/muxterm.go
package client

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// AttachAll attaches every named machine and returns their sessions + a cleanup.
// On any failure it cleans up the ones already attached.
func AttachAll(ctx context.Context, dir string, names []string, id *Identity) ([]*MuxSession, func(), error) {
	var sessions []*MuxSession
	var cleanups []func()
	cleanupAll := func() {
		for _, c := range cleanups {
			c()
		}
	}
	for _, name := range names {
		m, err := GetMachine(dir, name)
		if err != nil {
			cleanupAll()
			return nil, nil, err
		}
		mc, sess, cleanup, err := Attach(ctx, *m, id, nil) // nil STUN = host candidates (local)
		if err != nil {
			cleanupAll()
			return nil, nil, fmt.Errorf("attach %s: %w", name, err)
		}
		sessions = append(sessions, &MuxSession{Name: m.Name, MC: mc, Sess: sess})
		cleanups = append(cleanups, cleanup)
	}
	return sessions, cleanupAll, nil
}

// RunInteractiveMux puts the terminal in raw mode and runs the mux over sessions.
func RunInteractiveMux(ctx context.Context, sessions []*MuxSession) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("tr attach requires a TTY (stdin is not a terminal)")
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer func() { _ = term.Restore(fd, old) }()

	cols, rows, err := term.GetSize(fd)
	if err != nil {
		cols, rows = 80, 24
	}
	names := make([]string, len(sessions))
	for i, s := range sessions {
		names[i] = s.Name
	}
	fmt.Fprintf(os.Stderr, "[tr] %d machines: %v — switch with Ctrl-] then 1-9 / n / q\r\n", len(sessions), names)

	resizes := make(chan Size, 1)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			if c, r, e := term.GetSize(fd); e == nil {
				select {
				case resizes <- Size{Cols: uint16(c), Rows: uint16(r)}:
				default:
				}
			}
		}
	}()

	mux := NewMux(sessions, os.Stdout, DefaultPrefix, Size{Cols: uint16(cols), Rows: uint16(rows)})
	return mux.Run(ctx, os.Stdin, resizes)
}
```

- [ ] **Step 2: Update `cmdAttach` in `cmd/tr/main.go` to accept multiple machines**

Replace the body of `cmdAttach` with:
```go
func cmdAttach(args []string) {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	_ = fs.Parse(args)
	names := fs.Args()
	if len(names) == 0 {
		fatal(fmt.Errorf("usage: tr attach <machine> [machine...]"))
	}
	idn, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(names) == 1 {
		m, err := client.GetMachine(*dir, names[0])
		if err != nil {
			fatal(err)
		}
		mc, sess, cleanup, err := client.Attach(ctx, *m, idn, nil)
		if err != nil {
			fatal(err)
		}
		defer cleanup()
		if err := client.RunInteractive(ctx, mc, sess, m.Name); err != nil && ctx.Err() == nil {
			fatal(err)
		}
		return
	}

	sessions, cleanup, err := client.AttachAll(ctx, *dir, names, idn)
	if err != nil {
		fatal(err)
	}
	defer cleanup()
	if err := client.RunInteractiveMux(ctx, sessions); err != nil && ctx.Err() == nil {
		fatal(err)
	}
}
```

- [ ] **Step 3: Build + vet**

Run: `cd go && go build ./... && go vet ./...`
Expected: builds clean, vet clean.

- [ ] **Step 4: Commit**

```bash
git add go/internal/client/muxterm.go go/cmd/tr/main.go
git commit -m "feat(client): raw-mode mux wiring; tr attach accepts multiple machines"
```

---

## Task 4: Local E2E — mux over two real agents, switch focus

**Files:** Create `go/internal/client/e2e_mux_test.go`; Modify `Makefile` (race target already exists), `README.md`

- [ ] **Step 1: Write the E2E**

```go
// go/internal/client/e2e_mux_test.go
package client

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

// startAgent spins a real tr-agent (sh) registered to the signaling server and
// trusting the given owner; returns its machine descriptor.
func startAgent(t *testing.T, ctx context.Context, srvURL, name string, id *Identity) Machine {
	t.Helper()
	dir := t.TempDir()
	cfg, err := agent.LoadOrInit(dir, name, srvURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.PinOwner(dir, id.OwnerPubHex); err != nil {
		t.Fatal(err)
	}
	cfg, _ = agent.LoadOrInit(dir, name, srvURL)
	rt := agent.NewRuntime(cfg, []string{"sh"}, nil)
	go func() { _ = rt.Up(ctx) }()
	return Machine{Name: name, MachineID: cfg.MachineID, HostPubHex: cfg.HostPubHex, SignalURL: srvURL}
}

func TestEndToEndMuxSwitchesBetweenTwoMachines(t *testing.T) {
	srv := httptest.NewServer(signal.New().Handler())
	defer srv.Close()

	clientDir := t.TempDir()
	id, err := LoadOrCreateIdentity(clientDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m0 := startAgent(t, ctx, srv.URL, "box0", id)
	m1 := startAgent(t, ctx, srv.URL, "box1", id)
	time.Sleep(400 * time.Millisecond)

	s0mc, s0sess, c0, err := Attach(ctx, m0, id, nil)
	if err != nil {
		t.Fatalf("attach box0: %v", err)
	}
	defer c0()
	s1mc, s1sess, c1, err := Attach(ctx, m1, id, nil)
	if err != nil {
		t.Fatalf("attach box1: %v", err)
	}
	defer c1()

	sessions := []*MuxSession{
		{Name: "box0", MC: s0mc, Sess: s0sess},
		{Name: "box1", MC: s1mc, Sess: s1sess},
	}
	out := &syncWriter{}
	in := newBlockingReader()
	mux := NewMux(sessions, out, DefaultPrefix, Size{Cols: 80, Rows: 24})
	go func() { _ = mux.Run(ctx, in, make(chan Size)) }()

	// Focus box0: run a command, see its marker.
	in.feed([]byte("echo MARKER_BOX0\n"))
	waitFor(t, out, "MARKER_BOX0")

	// Switch to box1 (Ctrl-] then '2'): run a command, see its marker.
	in.feed([]byte{DefaultPrefix, '2'})
	in.feed([]byte("echo MARKER_BOX1\n"))
	waitFor(t, out, "MARKER_BOX1")

	// Sanity: both markers are present overall; they came from two different shells.
	if !bytes.Contains([]byte(out.String()), []byte("MARKER_BOX0")) {
		t.Fatal("lost box0 output")
	}
}
```

- [ ] **Step 2: Run the E2E + full suite + race**

Run: `cd go && go test ./internal/client/ -run TestEndToEndMux -v && go test ./... && go test -race -count=1 ./internal/client/ ./internal/agent/`
Expected: PASS — the mux attaches two real shells over P2P and switches focus; all green, race-free.

- [ ] **Step 3: Append a mux section to `README.md`**

```markdown
### Multi-machine (Plan 4b)

`tr attach <m1> <m2> ...` attaches several machines at once and multiplexes them
onto your terminal — one in focus, the rest live in the background. Switch with the
prefix key **Ctrl-]** then:

- `1`–`9` — focus that machine
- `n` — next machine
- `q` — quit (detach all)
- `Ctrl-]` again — send a literal Ctrl-] to the focused machine

Each machine keeps its own tmux (windows/panes + persistence); this layer only
chooses which machine is in focus. Switching clears the screen and nudges the
focused machine's tmux to redraw.
```

- [ ] **Step 4: Commit**

```bash
git add go/internal/client/e2e_mux_test.go README.md
git commit -m "test(client): E2E mux over two real shells; docs"
```

---

## Self-review (completed during planning)

- **Spec coverage:** Implements the spec's component #4 "focus-switcher" and the
  roadmap's "multi-machine focus-switcher" — hold N P2P sessions, one in focus,
  hotkey switch, redraw-nudge on switch, two-level model (tmux per host, `tr`
  across hosts). `tr attach <m1> [m2 ...]` is the entry point.
- **Correctness:** Task 1 fixes a real latent nonce race (concurrent
  `noise.Session.Encrypt`) by serializing per-session sends — applied to both the
  retrofitted `ClientBridge` and every Mux sender. This also resolves the Plan-4
  "bridge goroutine/encrypt" concern at its root for the multi-session case.
- **Lifecycle:** a session that disconnects is marked dead, announced, and focus
  advances to the next live machine (quit when none remain); because Plan 3 made
  `peer.DataChannel.Recv` unblock on remote close, `readSession` returns instead of
  parking — no goroutine leak in the long-lived multi-session client.
- **Testability:** the `Mux` core takes `io.Reader`/`io.Writer` and is fully tested
  without a TTY (`mux_test.go` with fake agents incl. the background-drop case;
  `e2e_mux_test.go` with two real agents). Only `muxterm.go` touches the real
  terminal and is build-only.
- **Placeholder scan:** no TBD/TODO; complete code in every code step; exact
  commands + expected output in every run step.
- **Type consistency:** `sender`/`newSender`/`send`, `MuxSession{Name,MC,Sess,snd}`,
  `Mux`/`NewMux`/`Run`/`readSession`/`resizeLoop`/`readStdin`/`forward`/`command`/
  `switchTo`/`onSessionEnd`/`nextLiveLocked`/`setTitle`, `DefaultPrefix`,
  `AttachAll`/`RunInteractiveMux`, reuse of `Attach`/`RunInteractive`/`Size`/
  `peer.MsgConn`/`noise` codec are consistent across files and tests. Shared test
  helpers (`syncWriter`, `newBlockingReader`, `waitFor`) live in the same `client`
  package test files.
- **Invariants:** STUN-only (nil), never TURN; no terminal data through
  `tr-signal`; `internal/noise`/`internal/identity` untouched.
```
