# terminal-relay — Plan 4: `tr` CLI client (single-machine attach)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `tr attach <machine>` — a native terminal client. From your existing
terminal emulator it derives the owner key, signals, opens a P2P DataChannel, runs
the Noise `KK` initiator, and bridges the DataChannel ⇄ your local terminal in raw
mode. You drive a **real remote shell over P2P** from the command line — no
browser, reusing the same Go peer as the agent. (The multi-machine focus-switcher
is the immediate follow-up, Plan 4b, built on this core.)

**Architecture:** Reuses Plan 1 (`internal/noise`), Plan 2 (`internal/peer`), and
the signaling protocol (`internal/signal`). New: a client keystore (owner key +
known machines), a signaling+offer+Noise-initiator `Attach`, a testable terminal
**bridge core** (operates on `io.Reader`/`io.Writer` + a resize channel), and a
thin raw-mode TTY wiring. Identity is an SSH-style local owner key; dev pairing is
manual both ways (the QR/token flow is Plan 5).

**Tech Stack:** Go (same module), `golang.org/x/term` for raw mode + size, existing deps.

**Implementer rules (same as Plans 1-3):** Reuse Plan 1/2/3 packages; do not modify
`internal/noise`/`internal/identity`. Adapt `x/term`/`pion`/`coder/websocket` API
specifics to installed versions if needed, keeping semantics identical; note
adaptations. Never configure TURN; never route terminal data through `tr-signal`.
Run every command for real; never fake green. The bridge **core** must be testable
without a TTY (it takes io.Reader/io.Writer); only the thin `term.go` wiring touches
the real terminal and is build-only.

---

## File structure

```
go/
  internal/client/
    store.go     store_test.go   # owner keypair (owner.json) + known machines (machines.json)
    bridge.go    bridge_test.go  # ClientBridge core: io.Reader/Writer <-> Noise session <-> frames + resize
    attach.go                    # dial signaling, offer, Noise initiator -> (DataChannel, Session)
    term.go                      # raw-mode + SIGWINCH wiring (real TTY; thin, build-only)
    e2e_test.go                  # tr client bridge -> tr-signal -> real tr-agent sh, scripted I/O
  cmd/tr/main.go                 # keygen | add-machine | list | attach
```

---

## Task 1: Client keystore (owner key + known machines)

**Files:** Create `go/internal/client/store.go`, `go/internal/client/store_test.go`

- [ ] **Step 1: Write the failing test**

```go
// go/internal/client/store_test.go
package client

import "testing"

func TestIdentityIsCreatedOnceAndStable(t *testing.T) {
	dir := t.TempDir()
	id, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(id.OwnerPriv()) != 32 || len(id.OwnerPub()) != 32 {
		t.Fatalf("owner key not initialized: priv=%d pub=%d", len(id.OwnerPriv()), len(id.OwnerPub()))
	}
	id2, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if id2.OwnerPrivHex != id.OwnerPrivHex {
		t.Fatal("owner identity not stable across loads")
	}
}

func TestAddAndGetMachine(t *testing.T) {
	dir := t.TempDir()
	m := Machine{Name: "macbook", MachineID: "abc123", HostPubHex: "deadbeef", SignalURL: "http://localhost:8443"}
	if err := AddMachine(dir, m); err != nil {
		t.Fatal(err)
	}
	got, err := GetMachine(dir, "macbook")
	if err != nil {
		t.Fatal(err)
	}
	if got.MachineID != "abc123" || got.HostPubHex != "deadbeef" {
		t.Fatalf("machine mismatch: %+v", got)
	}
	// Re-adding the same name updates in place (no duplicate).
	m.HostPubHex = "cafe"
	if err := AddMachine(dir, m); err != nil {
		t.Fatal(err)
	}
	list, _ := ListMachines(dir)
	if len(list) != 1 || list[0].HostPubHex != "cafe" {
		t.Fatalf("expected 1 updated machine, got %+v", list)
	}
}

func TestGetMissingMachineErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := GetMachine(dir, "nope"); err == nil {
		t.Fatal("expected error for unknown machine")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/client/ -run "TestIdentity|TestAdd|TestGetMissing" -v`
Expected: FAIL — `undefined: LoadOrCreateIdentity`.

- [ ] **Step 3: Write `store.go`**

```go
// go/internal/client/store.go
package client

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/srcful/terminal-relay/go/internal/noise"
)

// Identity is the client's SSH-style owner keypair (owner.json).
type Identity struct {
	OwnerPrivHex string `json:"owner_priv"`
	OwnerPubHex  string `json:"owner_pub"`
}

// Machine is a known agent (machines.json), pinned by host pubkey.
type Machine struct {
	Name       string `json:"name"`
	MachineID  string `json:"machine_id"`
	HostPubHex string `json:"host_pub"`
	SignalURL  string `json:"signal_url"`
}

func identityPath(dir string) string { return filepath.Join(dir, "owner.json") }
func machinesPath(dir string) string { return filepath.Join(dir, "machines.json") }

// LoadOrCreateIdentity reads owner.json, creating a fresh owner keypair on first use.
func LoadOrCreateIdentity(dir string) (*Identity, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	id := &Identity{}
	if data, err := os.ReadFile(identityPath(dir)); err == nil {
		if err := json.Unmarshal(data, id); err != nil {
			return nil, err
		}
	}
	if id.OwnerPrivHex == "" {
		priv, pub, err := noise.GenerateStatic()
		if err != nil {
			return nil, err
		}
		id.OwnerPrivHex = hex.EncodeToString(priv)
		id.OwnerPubHex = hex.EncodeToString(pub)
		data, _ := json.MarshalIndent(id, "", "  ")
		if err := os.WriteFile(identityPath(dir), data, 0o600); err != nil {
			return nil, err
		}
	}
	_ = os.Chmod(identityPath(dir), 0o600)
	return id, nil
}

func (i *Identity) OwnerPriv() []byte { b, _ := hex.DecodeString(i.OwnerPrivHex); return b }
func (i *Identity) OwnerPub() []byte  { b, _ := hex.DecodeString(i.OwnerPubHex); return b }

// AddMachine inserts or updates a known machine by name.
func AddMachine(dir string, m Machine) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	list, _ := ListMachines(dir)
	updated := false
	for i := range list {
		if list[i].Name == m.Name {
			list[i] = m
			updated = true
			break
		}
	}
	if !updated {
		list = append(list, m)
	}
	data, _ := json.MarshalIndent(list, "", "  ")
	return os.WriteFile(machinesPath(dir), data, 0o600)
}

func ListMachines(dir string) ([]Machine, error) {
	data, err := os.ReadFile(machinesPath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var list []Machine
	err = json.Unmarshal(data, &list)
	return list, err
}

func GetMachine(dir, name string) (*Machine, error) {
	list, err := ListMachines(dir)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Name == name {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("unknown machine %q (add it with `tr add-machine`)", name)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/client/ -run "TestIdentity|TestAdd|TestGetMissing" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/client/store.go go/internal/client/store_test.go
git commit -m "feat(client): owner keystore + known-machines registry"
```

---

## Task 2: Terminal bridge core

**Files:** Create `go/internal/client/bridge.go`, `go/internal/client/bridge_test.go`

- [ ] **Step 1: Write the failing test (fake echo agent over a pipe)**

```go
// go/internal/client/bridge_test.go
package client

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// syncWriter is an io.Writer safe for concurrent use, for assertions.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestClientBridgePumpsTerminalOverNoise(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	clientPriv, clientPub, _ := noise.GenerateStatic()
	agentPriv, agentPub, _ := noise.GenerateStatic()
	clientMC, agentMC := peer.Pipe()

	// Fake "agent": Noise responder that sends HELLO, echoes DATA, records RESIZE.
	gotResize := make(chan [2]uint16, 1)
	go func() {
		sess, err := peer.RunResponder(ctx, agentMC, agentPriv, clientPub)
		if err != nil {
			return
		}
		hello, _ := json.Marshal(map[string]string{"name": "fake"})
		_ = agentMC.Send(mustEnc(sess, noise.EncodeHello(hello)))
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
			switch typ {
			case noise.FrameData:
				_ = agentMC.Send(mustEnc(sess, noise.EncodeData(payload))) // echo
			case noise.FrameResize:
				c, r, _ := noise.DecodeResize(payload)
				select {
				case gotResize <- [2]uint16{c, r}:
				default:
				}
			}
		}
	}()

	clientSess, err := peer.RunInitiator(ctx, clientMC, clientPriv, agentPub)
	if err != nil {
		t.Fatal(err)
	}

	in := newBlockingReader()
	out := &syncWriter{}
	resizes := make(chan Size, 1)
	bridgeDone := make(chan error, 1)
	go func() {
		bridgeDone <- ClientBridge(ctx, in, out, resizes, Size{Cols: 80, Rows: 24}, clientMC, clientSess)
	}()

	// Initial RESIZE must reach the agent.
	select {
	case rs := <-gotResize:
		if rs != [2]uint16{80, 24} {
			t.Fatalf("initial resize = %v", rs)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no initial resize")
	}

	// Type a line; expect it echoed to out (HELLO must NOT appear in out).
	in.feed([]byte("hello-bridge"))
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains([]byte(out.String()), []byte("hello-bridge")) {
			if bytes.Contains([]byte(out.String()), []byte("fake")) {
				t.Fatal("HELLO metadata leaked into terminal output")
			}
			return // success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("echo not seen in out; got %q", out.String())
}
```

- [ ] **Step 2: Add the test helpers (same file)**

Append to `bridge_test.go`:
```go
import "encoding/json"

func mustEnc(sess *noise.Session, framed []byte) []byte {
	ct, err := sess.Encrypt(framed)
	if err != nil {
		panic(err)
	}
	return ct
}

// blockingReader is an io.Reader fed on demand; Read blocks until data or close.
type blockingReader struct {
	ch chan []byte
}

func newBlockingReader() *blockingReader { return &blockingReader{ch: make(chan []byte, 16)} }
func (b *blockingReader) feed(p []byte)  { b.ch <- p }
func (b *blockingReader) Read(p []byte) (int, error) {
	chunk, ok := <-b.ch
	if !ok {
		return 0, io.EOF
	}
	return copy(p, chunk), nil
}
```
Add the imports `"io"` (and keep `"encoding/json"`) to the test file's import block.

- [ ] **Step 3: Run test to verify it fails**

Run: `cd go && go test ./internal/client/ -run TestClientBridge -v`
Expected: FAIL — `undefined: ClientBridge` / `undefined: Size`.

- [ ] **Step 4: Write `bridge.go`**

```go
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
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd go && go test ./internal/client/ -run TestClientBridge -v`
Expected: PASS — stdin echoes back through Noise; HELLO is not leaked; initial RESIZE delivered.

- [ ] **Step 6: Commit**

```bash
git add go/internal/client/bridge.go go/internal/client/bridge_test.go
git commit -m "feat(client): terminal bridge core (stdin/stdout <-> Noise frames)"
```

---

## Task 3: Attach (signaling + offer + Noise initiator)

**Files:** Create `go/internal/client/attach.go`

- [ ] **Step 1: Write `attach.go`**

```go
// go/internal/client/attach.go
package client

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

// Attach connects to the signaling server as the owner, negotiates a P2P
// DataChannel with the named machine's agent, runs the Noise KK initiator, and
// returns the established session. Call cleanup when done.
func Attach(ctx context.Context, m Machine, id *Identity, stun []string) (mc *peer.DataChannel, sess *noise.Session, cleanup func(), err error) {
	ownerPubHex := id.OwnerPubHex
	wsURL := "ws" + strings.TrimPrefix(m.SignalURL, "http") +
		"/attach?owner_id=" + url.QueryEscape(ownerPubHex) +
		"&machine_id=" + url.QueryEscape(m.MachineID)

	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial signaling: %w", err)
	}
	closeWS := func() { _ = c.CloseNow() }

	off, opened, err := peer.NewOfferer(stun)
	if err != nil {
		closeWS()
		return nil, nil, nil, err
	}
	cleanup = func() { _ = off.Close(); closeWS() }

	offerSDP, err := peer.CreateOffer(off)
	if err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	offerMsg, _ := json.Marshal(signal.SignalMsg{Type: signal.TypeOffer, SDP: offerSDP})
	if err := c.Write(ctx, websocket.MessageText, offerMsg); err != nil {
		cleanup()
		return nil, nil, nil, err
	}

	_, data, err := c.Read(ctx)
	if err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	var ans signal.SignalMsg
	if json.Unmarshal(data, &ans) != nil || ans.Type != signal.TypeAnswer {
		cleanup()
		if ans.Type == signal.TypeError {
			return nil, nil, nil, fmt.Errorf("signaling: %s", ans.Reason)
		}
		return nil, nil, nil, fmt.Errorf("unexpected signaling reply: %s", string(data))
	}
	if err := peer.AcceptAnswer(off, ans.SDP); err != nil {
		cleanup()
		return nil, nil, nil, err
	}

	octx, ocancel := context.WithTimeout(ctx, 20*time.Second)
	defer ocancel()
	select {
	case mc = <-opened:
	case <-octx.Done():
		cleanup()
		return nil, nil, nil, fmt.Errorf("no direct P2P path to %q (strict P2P, no relay fallback)", m.Name)
	}

	hostPub, err := hex.DecodeString(m.HostPubHex)
	if err != nil {
		cleanup()
		return nil, nil, nil, fmt.Errorf("bad host pubkey for %q: %w", m.Name, err)
	}
	sess, err = peer.RunInitiator(ctx, mc, id.OwnerPriv(), hostPub)
	if err != nil {
		cleanup()
		return nil, nil, nil, fmt.Errorf("noise handshake (wrong key / not paired?): %w", err)
	}
	return mc, sess, cleanup, nil
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd go && go build ./internal/client/`
Expected: builds (exercised by the E2E in Task 6).

- [ ] **Step 3: Commit**

```bash
git add go/internal/client/attach.go
git commit -m "feat(client): Attach (signaling + offer + Noise initiator)"
```

---

## Task 4: Raw-mode TTY wiring

**Files:** Create `go/internal/client/term.go`; Modify `go/go.mod`

- [ ] **Step 1: Add x/term**

Run: `cd go && go get golang.org/x/term@latest`
Expected: `golang.org/x/term` added.

- [ ] **Step 2: Write `term.go`**

```go
// go/internal/client/term.go
package client

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// RunInteractive puts the real terminal into raw mode, wires SIGWINCH to RESIZE,
// and runs the bridge against stdin/stdout. Restores the terminal on exit.
func RunInteractive(ctx context.Context, mc *peer.DataChannel, sess *noise.Session, machineName string) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("tr attach requires a TTY (stdin is not a terminal)")
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer func() { _ = term.Restore(fd, old) }()
	fmt.Fprintf(os.Stderr, "[tr] attached to %s — detach with the client, Ctrl-C inside the shell\r\n", machineName)

	cols, rows, err := term.GetSize(fd)
	if err != nil {
		cols, rows = 80, 24
	}

	resizes := make(chan Size, 1)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			c, r, e := term.GetSize(fd)
			if e == nil {
				select {
				case resizes <- Size{Cols: uint16(c), Rows: uint16(r)}:
				default:
				}
			}
		}
	}()

	return ClientBridge(ctx, os.Stdin, os.Stdout, resizes, Size{Cols: uint16(cols), Rows: uint16(rows)}, mc, sess)
}
```

- [ ] **Step 3: Verify it compiles**

Run: `cd go && go build ./internal/client/`
Expected: builds.

- [ ] **Step 4: Commit**

```bash
git add go/internal/client/term.go go/go.mod go/go.sum
git commit -m "feat(client): raw-mode TTY wiring (x/term + SIGWINCH)"
```

---

## Task 5: `tr` binary

**Files:** Create `go/cmd/tr/main.go`

- [ ] **Step 1: Write `main.go`**

```go
// go/cmd/tr/main.go
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

	"github.com/srcful/terminal-relay/go/internal/client"
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
	case "keygen":
		cmdKeygen(os.Args[2:])
	case "add-machine":
		cmdAddMachine(os.Args[2:])
	case "list":
		cmdList(os.Args[2:])
	case "attach":
		cmdAttach(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tr <keygen|add-machine|list|attach> [flags]")
	os.Exit(2)
}

func cmdKeygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	_ = fs.Parse(args)
	id, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("owner public key:\n  %s\n\nPin it on each machine:\n  tr-agent pair-dev --owner-pub %s\n", id.OwnerPubHex, id.OwnerPubHex)
}

func cmdAddMachine(args []string) {
	fs := flag.NewFlagSet("add-machine", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	name := fs.String("name", "", "machine name")
	id := fs.String("id", "", "machine id (from `tr-agent enroll`)")
	hostPub := fs.String("host-pub", "", "machine host public key (hex, from `tr-agent enroll`)")
	signalURL := fs.String("signal", "http://localhost:8443", "signaling server base URL")
	_ = fs.Parse(args)
	if *name == "" || *id == "" || *hostPub == "" {
		fatal(fmt.Errorf("--name, --id and --host-pub are required"))
	}
	m := client.Machine{Name: *name, MachineID: *id, HostPubHex: strings.ToLower(*hostPub), SignalURL: *signalURL}
	if err := client.AddMachine(*dir, m); err != nil {
		fatal(err)
	}
	fmt.Printf("added machine %q (%s) via %s\n", m.Name, m.MachineID, m.SignalURL)
}

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	_ = fs.Parse(args)
	list, err := client.ListMachines(*dir)
	if err != nil {
		fatal(err)
	}
	if len(list) == 0 {
		fmt.Println("no machines yet — add one with `tr add-machine`")
		return
	}
	for _, m := range list {
		fmt.Printf("%-16s %s  %s\n", m.Name, m.MachineID, m.SignalURL)
	}
}

func cmdAttach(args []string) {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) != 1 {
		fatal(fmt.Errorf("usage: tr attach <machine>"))
	}
	idn, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		fatal(err)
	}
	m, err := client.GetMachine(*dir, rest[0])
	if err != nil {
		fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mc, sess, cleanup, err := client.Attach(ctx, *m, idn, nil) // nil STUN = host candidates (local)
	if err != nil {
		fatal(err)
	}
	defer cleanup()

	if err := client.RunInteractive(ctx, mc, sess, m.Name); err != nil && ctx.Err() == nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
```

- [ ] **Step 2: Build + smoke**

Run:
```bash
cd go && go build -o /tmp/tr ./cmd/tr && /tmp/tr keygen --dir /tmp/tr-test && /tmp/tr list --dir /tmp/tr-test
```
Expected: prints an owner public key + pair-dev hint, then "no machines yet".

- [ ] **Step 3: Commit**

```bash
git add go/cmd/tr/main.go
git commit -m "feat(client): tr binary (keygen | add-machine | list | attach)"
```

---

## Task 6: Local end-to-end — `tr` client drives a real shell over P2P

**Files:** Create `go/internal/client/e2e_test.go`; modify `Makefile`, `README.md`

- [ ] **Step 1: Write the E2E test (real `tr-signal` + real `tr-agent` + real `sh`)**

```go
// go/internal/client/e2e_test.go
package client

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

func TestEndToEndTrClientDrivesRealShell(t *testing.T) {
	srv := httptest.NewServer(signal.New().Handler())
	defer srv.Close()

	// Client identity.
	clientDir := t.TempDir()
	id, err := LoadOrCreateIdentity(clientDir)
	if err != nil {
		t.Fatal(err)
	}

	// Agent: keystore in its own dir, pin the client owner, run the runtime (sh).
	agentDir := t.TempDir()
	acfg, err := agent.LoadOrInit(agentDir, "e2e-box", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.PinOwner(agentDir, id.OwnerPubHex); err != nil {
		t.Fatal(err)
	}
	acfg, _ = agent.LoadOrInit(agentDir, "e2e-box", srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rt := agent.NewRuntime(acfg, []string{"sh"}, nil)
	go func() { _ = rt.Up(ctx) }()
	time.Sleep(300 * time.Millisecond)

	// Register the machine in the client (as `tr add-machine` would).
	m := Machine{Name: "box", MachineID: acfg.MachineID, HostPubHex: acfg.HostPubHex, SignalURL: srv.URL}

	mc, sess, cleanup, err := Attach(ctx, m, id, nil)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer cleanup()

	// Drive the bridge with scripted I/O (no TTY): feed a command, capture output.
	in := newBlockingReader()
	out := &syncWriter{}
	resizes := make(chan Size, 1)
	go func() { _ = ClientBridge(ctx, in, out, resizes, Size{Cols: 80, Rows: 24}, mc, sess) }()

	in.feed([]byte("echo TR_CLIENT_OK\n"))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains([]byte(out.String()), []byte("TR_CLIENT_OK")) {
			return // SUCCESS: tr client -> tr-signal -> real sh over P2P
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("never saw command output; got:\n%s", out.String())

	_ = hex.EncodeToString // keep import if unused after edits
	_ = noise.FrameData
}
```

- [ ] **Step 2: Run the E2E**

Run: `cd go && go test ./internal/client/ -run TestEndToEndTrClient -v`
Expected: PASS — the `tr` client (Attach + ClientBridge) drives a real `sh` on a real `tr-agent` over a P2P DataChannel through `tr-signal`. (Remove the trailing `_ = ...` keep-alive lines if the imports are already used; they are only there to avoid an unused-import error if you trim the test.)

- [ ] **Step 3: Whole suite + race**

Run: `cd go && go test ./... && go test -race -count=1 ./internal/client/ ./internal/agent/`
Expected: PASS, race-free.

- [ ] **Step 4: Add `tr` to the Makefile build + a README section**

In `Makefile`, extend the `build` target:
```makefile
build:
	cd go && go build -o ../bin/tr-signal ./cmd/tr-signal
	cd go && go build -o ../bin/tr-agent ./cmd/tr-agent
	cd go && go build -o ../bin/tr ./cmd/tr
```

In `README.md`, append:
```markdown
## CLI client (Plan 4)

`go/cmd/tr` — `tr attach <machine>` opens a P2P terminal to one of your machines
from your own terminal. Identity is a local owner key (`~/.terminal-relay/owner.json`);
machines are pinned by host key in `machines.json`.

Local loop (all on one Mac):

    make build
    ./bin/tr-signal --addr :8443 &
    # machine side:
    ./bin/tr-agent enroll --signal http://localhost:8443     # prints machine_id + host_pub
    ./bin/tr keygen                                          # prints owner pub
    ./bin/tr-agent pair-dev --owner-pub <owner-pub>          # machine trusts you
    ./bin/tr-agent up --shell sh &                           # (or tmux for persistence)
    # client side:
    ./bin/tr add-machine --name box --id <machine_id> --host-pub <host_pub> --signal http://localhost:8443
    ./bin/tr attach box                                      # real shell over P2P

The full client path is proven hermetically by
`go test ./internal/client/ -run TestEndToEnd`.
```

- [ ] **Step 5: Verify build + commit**

Run: `make build`
Expected: `bin/tr` is produced alongside `bin/tr-signal` and `bin/tr-agent`.

```bash
git add go/internal/client/e2e_test.go Makefile README.md
git commit -m "test(client): local E2E — tr drives a real shell over P2P; build tr"
```

---

## Self-review (completed during planning)

- **Spec coverage:** Implements the spec's component #4 "`tr` CLI client (primary)"
  for the single-machine case: derive owner key, signal, P2P DataChannel, Noise
  initiator, bridge ⇄ local terminal in raw mode, with the agent's HELLO consumed
  and RESIZE wired to SIGWINCH. Identity is the SSH-style local owner key; dev
  pairing is manual both ways (`tr keygen` ↔ `tr-agent pair-dev`; `tr-agent enroll`
  ↔ `tr add-machine`), with QR/token pairing deferred to Plan 5.
- **Deferred (explicit):** the multi-machine **focus-switcher** (hold N sessions,
  hotkey to switch) is Plan 4b, built directly on `Attach` + `ClientBridge`
  (each machine = one `Attach`; the switcher routes stdin to the focused session
  and that session's output to stdout, nudging a RESIZE on switch to make tmux
  redraw). Not in this plan to keep the usable single-attach terminal shippable first.
- **Testability:** the bridge **core** (`ClientBridge`) takes `io.Reader`/`io.Writer`
  + a resize channel, so it is fully tested without a TTY (`bridge_test.go` with a
  fake echo agent; `e2e_test.go` against a real `tr-agent`+`tr-signal`+`sh`). Only
  `term.go` touches the real terminal (raw mode) and is build-only.
- **Placeholder scan:** no TBD/TODO; complete code in every code step; exact
  commands + expected output in every run step.
- **Type consistency:** `Identity`/`LoadOrCreateIdentity`/`OwnerPriv`/`OwnerPub`,
  `Machine`/`AddMachine`/`GetMachine`/`ListMachines`, `Size`, `ClientBridge`/
  `sendFrame`, `Attach`, `RunInteractive`, reuse of `peer.NewOfferer/CreateOffer/
  AcceptAnswer/RunInitiator/DataChannel/Pipe`, `noise` frame codec + `Session`,
  `signal.SignalMsg`/`Type*` are consistent across files and tests. `owner_id` in
  the attach URL is the owner pubkey hex, matching the agent's registration key
  (`PairedOwners[0]`).
- **Invariants:** STUN-only (nil = host candidates locally), never TURN; terminal
  data never crosses `tr-signal` (only SDP); `internal/noise`/`internal/identity`
  untouched.
```
