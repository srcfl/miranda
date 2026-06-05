# terminal-relay — Plan 5: one-tap pairing (token / QR, NNpsk0)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** replace the manual key exchange (`enroll` → copy machine_id+host_pub → `keygen` → `pair-dev` → `add-machine`) with **one tap**: the agent prints a pairing **code + QR**; the client runs `trm pair <code>`. Under the hood a one-time 128-bit token is the PSK of an **NNpsk0** handshake brokered (blind) through the signaling server; inside it the two sides exchange and pin their static keys. MITM-resistant via the token, no fingerprint comparison.

**Architecture:** New `internal/pairing` (NNpsk0 over a message conn, exchanging static keys + metadata in the payloads). The signaling server gains a `/pair` **room bridge** (matches two parties by `roomID = H(token)`, forwards opaque frames, never sees the token or plaintext). New CLI verbs: `tr-agent pair` (responder + token/QR) and `trm pair <code>` (initiator → writes `machines.json`). The deferred Plan-3 "interactive pairing" lands here; the browser's JS side comes with Plan 6.

**Tech Stack:** Go (same module), `github.com/flynn/noise` (NNpsk0), `github.com/coder/websocket`, `github.com/mdp/qrterminal/v3` for the QR. Reuses `internal/noise` keys + `internal/peer.MsgConn`.

**Implementer rules (same as before):** Reuse Plans 1–4; do not modify `internal/noise`/`internal/identity`. Adapt flynn/noise / coder/websocket / qrterminal API specifics to installed versions if a call fails to compile, keeping semantics identical; note adaptations. The signaling server must stay blind (only `roomID` + opaque frames cross it; never the token, never plaintext). Run every command for real; never fake green.

---

## Crypto design (read first)

- Token: 16 random bytes (128-bit), single-use, short TTL.
- `psk = SHA256("terminal-relay/pair/psk" || token)` (32 bytes — Noise PSK length).
- `roomID = hex(SHA256("terminal-relay/pair/room" || token)[:16])` — domain-separated
  from the psk, so the server learning `roomID` reveals neither the token nor the psk
  (128-bit preimage resistance). The server routes by `roomID` only.
- Handshake: `Noise_NNpsk0_25519_ChaChaPoly_SHA256`, prologue `terminal-relay/pair/v1`.
  Two messages; psk0 makes even the first message's payload confidential.
  - msg1 (client→agent) payload = the client's **owner public key** (32 bytes).
  - msg2 (agent→client) payload = JSON `{host_pub, machine_id, name}`.
  Both sides pin what they receive. Only holders of the token can complete the
  handshake, so the exchanged keys are authentic.
- Pairing code = base64url(JSON `{s: signalURL, t: hex(token)}`) — self-contained
  so `trm pair <code>` needs no extra flags.

## File structure

```
go/
  internal/pairing/
    pairing.go      pairing_test.go   # NNpsk0 RunInitiator/RunResponder + key derivation
    code.go         code_test.go      # pairing-code encode/decode, roomID, psk
    wsconn.go                         # coder/websocket -> peer.MsgConn adapter (client/agent side)
  internal/signal/
    pair.go         pair_test.go      # /pair room bridge (blind)
  cmd/tr-agent/main.go                # `tr-agent pair` (token + QR + NNpsk0 responder, pin owner)
  cmd/trm/main.go                     # `trm pair <code>` (NNpsk0 initiator -> AddMachine)
  internal/pairing/e2e_test.go        # agent pair + client pair through tr-signal, then attach works
```

---

## Task 1: Pairing code, roomID, psk

**Files:** Create `go/internal/pairing/code.go`, `go/internal/pairing/code_test.go`

- [ ] **Step 1: Write the failing test**

```go
// go/internal/pairing/code_test.go
package pairing

import (
	"bytes"
	"testing"
)

func TestCodeRoundTrip(t *testing.T) {
	tok := bytes.Repeat([]byte{0xAB}, 16)
	code := EncodeCode("http://localhost:8443", tok)
	signal, got, err := DecodeCode(code)
	if err != nil {
		t.Fatal(err)
	}
	if signal != "http://localhost:8443" || !bytes.Equal(got, tok) {
		t.Fatalf("round trip mismatch: %q %x", signal, got)
	}
}

func TestRoomAndPskAreDistinct(t *testing.T) {
	tok := bytes.Repeat([]byte{0x01}, 16)
	room := RoomID(tok)
	psk := pskFromToken(tok)
	if len(psk) != 32 {
		t.Fatalf("psk must be 32 bytes, got %d", len(psk))
	}
	// roomID must not equal/derive trivially from the psk.
	if room == "" || bytes.Contains(psk, []byte(room)) {
		t.Fatal("roomID and psk are not domain-separated")
	}
	// Same token -> stable room + psk.
	if RoomID(tok) != room || !bytes.Equal(pskFromToken(tok), psk) {
		t.Fatal("derivation not deterministic")
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, _, err := DecodeCode("not-a-code"); err == nil {
		t.Fatal("expected decode error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/pairing/ -run "TestCode|TestRoom|TestDecode" -v`
Expected: FAIL — `undefined: EncodeCode`.

- [ ] **Step 3: Write `code.go`**

```go
// go/internal/pairing/code.go
package pairing

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// NewToken returns a fresh 16-byte (128-bit) single-use pairing token.
func NewToken() []byte {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b
}

type codePayload struct {
	S string `json:"s"` // signal URL
	T string `json:"t"` // token hex
}

// EncodeCode produces a self-contained, copy-pasteable pairing code.
func EncodeCode(signalURL string, token []byte) string {
	data, _ := json.Marshal(codePayload{S: signalURL, T: hex.EncodeToString(token)})
	return base64.RawURLEncoding.EncodeToString(data)
}

// DecodeCode parses a pairing code into its signal URL and token.
func DecodeCode(code string) (signalURL string, token []byte, err error) {
	data, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil {
		return "", nil, fmt.Errorf("bad pairing code: %w", err)
	}
	var p codePayload
	if err := json.Unmarshal(data, &p); err != nil {
		return "", nil, fmt.Errorf("bad pairing code: %w", err)
	}
	token, err = hex.DecodeString(p.T)
	if err != nil || len(token) != 16 {
		return "", nil, fmt.Errorf("bad pairing code token")
	}
	return p.S, token, nil
}

// pskFromToken derives the 32-byte Noise PSK (domain-separated from roomID).
func pskFromToken(token []byte) []byte {
	h := sha256.Sum256(append([]byte("terminal-relay/pair/psk"), token...))
	return h[:]
}

// RoomID derives the public rendezvous id (domain-separated from the psk).
func RoomID(token []byte) string {
	h := sha256.Sum256(append([]byte("terminal-relay/pair/room"), token...))
	return hex.EncodeToString(h[:16])
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/pairing/ -run "TestCode|TestRoom|TestDecode" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/pairing/code.go go/internal/pairing/code_test.go
git commit -m "feat(pairing): pairing code + domain-separated room/psk derivation"
```

---

## Task 2: NNpsk0 pairing handshake

**Files:** Create `go/internal/pairing/pairing.go`, `go/internal/pairing/pairing_test.go`

- [ ] **Step 1: Write the failing test (Go↔Go over a pipe)**

```go
// go/internal/pairing/pairing_test.go
package pairing

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

func TestPairingExchangesAndPinsKeys(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	token := NewToken()
	_, ownerPub, _ := noise.GenerateStatic() // client owner key
	_, hostPub, _ := noise.GenerateStatic()  // agent host key

	clientMC, agentMC := peer.Pipe()
	info := AgentInfo{HostPubHex: hex.EncodeToString(hostPub), MachineID: "m123", Name: "box"}

	gotOwner := make(chan []byte, 1)
	go func() {
		op, err := RunResponder(ctx, agentMC, token, info)
		if err != nil {
			return
		}
		gotOwner <- op
	}()

	got, err := RunInitiator(ctx, clientMC, token, ownerPub)
	if err != nil {
		t.Fatalf("initiator: %v", err)
	}
	if got.HostPubHex != info.HostPubHex || got.MachineID != "m123" || got.Name != "box" {
		t.Fatalf("client got wrong agent info: %+v", got)
	}
	select {
	case op := <-gotOwner:
		if hex.EncodeToString(op) != hex.EncodeToString(ownerPub) {
			t.Fatal("agent pinned the wrong owner key")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent never received owner key")
	}
}

func TestPairingFailsWithWrongToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	_, ownerPub, _ := noise.GenerateStatic()
	_, hostPub, _ := noise.GenerateStatic()
	clientMC, agentMC := peer.Pipe()
	info := AgentInfo{HostPubHex: hex.EncodeToString(hostPub), MachineID: "m", Name: "n"}

	go func() { _, _ = RunResponder(ctx, agentMC, NewToken(), info) }() // different token

	if _, err := RunInitiator(ctx, clientMC, NewToken(), ownerPub); err == nil {
		t.Fatal("expected pairing to fail with mismatched tokens")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/pairing/ -run TestPairing -v`
Expected: FAIL — `undefined: RunResponder`.

- [ ] **Step 3: Write `pairing.go`**

```go
// go/internal/pairing/pairing.go
package pairing

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"github.com/flynn/noise"

	"github.com/srcful/terminal-relay/go/internal/peer"
)

var cipherSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

const prologue = "terminal-relay/pair/v1"

// AgentInfo is what the agent reveals to the client during pairing.
type AgentInfo struct {
	HostPubHex string `json:"host_pub"`
	MachineID  string `json:"machine_id"`
	Name       string `json:"name"`
}

func newHandshake(initiator bool, token []byte) (*noise.HandshakeState, error) {
	return noise.NewHandshakeState(noise.Config{
		CipherSuite:           cipherSuite,
		Pattern:               noise.HandshakeNN,
		Initiator:             initiator,
		Prologue:              []byte(prologue),
		PresharedKey:          pskFromToken(token),
		PresharedKeyPlacement: 0, // NNpsk0
		Random:                rand.Reader,
	})
}

// RunInitiator is the client side: it sends ownerPub and returns the agent's info.
func RunInitiator(ctx context.Context, mc peer.MsgConn, token, ownerPub []byte) (*AgentInfo, error) {
	hs, err := newHandshake(true, token)
	if err != nil {
		return nil, err
	}
	msg1, _, _, err := hs.WriteMessage(nil, ownerPub)
	if err != nil {
		return nil, err
	}
	if err := mc.Send(msg1); err != nil {
		return nil, err
	}
	msg2, err := mc.Recv(ctx)
	if err != nil {
		return nil, err
	}
	payload, _, _, err := hs.ReadMessage(nil, msg2)
	if err != nil {
		return nil, fmt.Errorf("pairing handshake failed (wrong code?): %w", err)
	}
	var info AgentInfo
	if err := json.Unmarshal(payload, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// RunResponder is the agent side: it returns the client's owner key and sends info.
func RunResponder(ctx context.Context, mc peer.MsgConn, token []byte, info AgentInfo) ([]byte, error) {
	hs, err := newHandshake(false, token)
	if err != nil {
		return nil, err
	}
	msg1, err := mc.Recv(ctx)
	if err != nil {
		return nil, err
	}
	ownerPub, _, _, err := hs.ReadMessage(nil, msg1)
	if err != nil {
		return nil, fmt.Errorf("pairing handshake failed (wrong code?): %w", err)
	}
	infoJSON, _ := json.Marshal(info)
	msg2, _, _, err := hs.WriteMessage(nil, infoJSON)
	if err != nil {
		return nil, err
	}
	if err := mc.Send(msg2); err != nil {
		return nil, err
	}
	return ownerPub, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/pairing/ -run TestPairing -v`
Expected: PASS — keys exchanged + pinned with the right token; rejected with the wrong token.

- [ ] **Step 5: Commit**

```bash
git add go/internal/pairing/pairing.go go/internal/pairing/pairing_test.go go/go.mod go/go.sum
git commit -m "feat(pairing): NNpsk0 key-exchange handshake"
```

---

## Task 3: WebSocket → MsgConn adapter

**Files:** Create `go/internal/pairing/wsconn.go`

- [ ] **Step 1: Write `wsconn.go`**

```go
// go/internal/pairing/wsconn.go
package pairing

import (
	"context"

	"github.com/coder/websocket"

	"github.com/srcful/terminal-relay/go/internal/peer"
)

// wsConn adapts a coder/websocket connection to peer.MsgConn (binary messages),
// so the NNpsk0 handshake can run over the signaling /pair channel.
type wsConn struct{ c *websocket.Conn }

// DialPair connects to the signaling server's /pair room and returns it as a MsgConn.
func DialPair(ctx context.Context, signalURL, roomID string) (peer.MsgConn, func(), error) {
	wsURL := toWS(signalURL) + "/pair?room=" + roomID
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, nil, err
	}
	return &wsConn{c: c}, func() { _ = c.CloseNow() }, nil
}

func (w *wsConn) Send(b []byte) error {
	return w.c.Write(context.Background(), websocket.MessageBinary, b)
}

func (w *wsConn) Recv(ctx context.Context) ([]byte, error) {
	_, data, err := w.c.Read(ctx)
	return data, err
}

// toWS rewrites http(s):// to ws(s)://.
func toWS(base string) string {
	if len(base) >= 4 && base[:4] == "http" {
		return "ws" + base[4:]
	}
	return base
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd go && go build ./internal/pairing/`
Expected: builds.

- [ ] **Step 3: Commit**

```bash
git add go/internal/pairing/wsconn.go
git commit -m "feat(pairing): websocket -> MsgConn adapter + DialPair"
```

---

## Task 4: Signaling server `/pair` room bridge

**Files:** Create `go/internal/signal/pair.go`, `go/internal/signal/pair_test.go`

- [ ] **Step 1: Write the failing test**

```go
// go/internal/signal/pair_test.go
package signal

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestPairBridgeForwardsBothWays(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	ctx := context.Background()
	dial := func() *websocket.Conn {
		c, _, err := websocket.Dial(ctx, wsURL(srv.URL, "/pair", map[string]string{"room": "abc"}), nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	a := dial()
	b := dial()

	if err := a.Write(ctx, websocket.MessageBinary, []byte("a->b")); err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, got, err := b.Read(rctx)
	if err != nil || string(got) != "a->b" {
		t.Fatalf("b got %q err %v", got, err)
	}

	if err := b.Write(ctx, websocket.MessageBinary, []byte("b->a")); err != nil {
		t.Fatal(err)
	}
	_, got, err = a.Read(rctx)
	if err != nil || string(got) != "b->a" {
		t.Fatalf("a got %q err %v", got, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/signal/ -run TestPairBridge -v`
Expected: FAIL — `/pair` not handled (read error / no forwarding).

- [ ] **Step 3: Write `pair.go`**

```go
// go/internal/signal/pair.go
package signal

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// pairWaiter is a connection waiting in a room for its partner.
type pairWaiter struct {
	conn    *websocket.Conn
	partner chan *websocket.Conn
}

type pairRooms struct {
	mu      sync.Mutex
	waiting map[string]*pairWaiter
}

func newPairRooms() *pairRooms { return &pairRooms{waiting: map[string]*pairWaiter{}} }

// rendezvous returns the partner conn (and true if THIS conn should drive the
// bridge). The first arrival waits; the second hands itself to the first and
// returns immediately to keep its socket open.
func (p *pairRooms) rendezvous(room string, c *websocket.Conn) (*websocket.Conn, bool) {
	p.mu.Lock()
	if w, ok := p.waiting[room]; ok {
		delete(p.waiting, room)
		p.mu.Unlock()
		w.partner <- c
		return w.conn, false // partner drives; this side just keeps its socket open
	}
	w := &pairWaiter{conn: c, partner: make(chan *websocket.Conn, 1)}
	p.waiting[room] = w
	p.mu.Unlock()

	select {
	case other := <-w.partner:
		return other, true // we drive the bridge
	case <-time.After(2 * time.Minute):
		p.mu.Lock()
		if p.waiting[room] == w {
			delete(p.waiting, room)
		}
		p.mu.Unlock()
		return nil, false
	}
}

// handlePair bridges two parties in the same room, forwarding opaque binary
// frames (NNpsk0 pairing messages) until either closes. The token never reaches
// the server — only roomID = H(token) and ciphertext.
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	room := r.URL.Query().Get("room")
	if room == "" {
		http.Error(w, "missing room", http.StatusBadRequest)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	other, drive := s.pair.rendezvous(room, c)
	if other == nil {
		c.Close(websocket.StatusGoingAway, "pair timeout")
		return
	}
	if !drive {
		<-r.Context().Done() // partner drives the bridge; keep this socket open
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go pairCopy(ctx, c, other, cancel)
	pairCopy(ctx, other, c, cancel)
}

func pairCopy(ctx context.Context, src, dst *websocket.Conn, done func()) {
	for {
		_, data, err := src.Read(ctx)
		if err != nil {
			done()
			return
		}
		if err := dst.Write(ctx, websocket.MessageBinary, data); err != nil {
			done()
			return
		}
	}
}
```

- [ ] **Step 4: Wire `/pair` + the rooms into the server**

In `go/internal/signal/server.go`: add a `pair *pairRooms` field to `Server`, initialize it in `New()` (`pair: newPairRooms()`), and register the route in `Handler()`:
```go
	mux.HandleFunc("/pair", s.handlePair)
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd go && go test ./internal/signal/ -run TestPairBridge -v && go test ./internal/signal/`
Expected: PASS (and the existing signal tests still pass).

- [ ] **Step 6: Commit**

```bash
git add go/internal/signal/pair.go go/internal/signal/pair_test.go go/internal/signal/server.go
git commit -m "feat(signal): blind /pair room bridge (rendezvous by roomID)"
```

---

## Task 5: `tr-agent pair` (responder + token + QR)

**Files:** Modify `go/cmd/tr-agent/main.go`

- [ ] **Step 1: Add the `pair` subcommand**

Add `case "pair": cmdPair(os.Args[2:])` to the `main()` switch (and to the usage line), then add:

```go
func cmdPair(args []string) {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	name := fs.String("name", hostname(), "machine display name")
	signalURL := fs.String("signal", "http://localhost:8443", "signaling server base URL")
	_ = fs.Parse(args)

	cfg, err := agent.LoadOrInit(*dir, *name, *signalURL)
	if err != nil {
		fatal(err)
	}

	token := pairing.NewToken()
	code := pairing.EncodeCode(*signalURL, token)

	fmt.Println("Pair this machine — run on your client:")
	fmt.Printf("\n  trm pair %s\n\n", code)
	qrterminal.GenerateHalfBlock(code, qrterminal.L, os.Stdout)
	fmt.Printf("\nwaiting for pairing (2 min)…\n")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	mc, closeConn, err := pairing.DialPair(ctx, *signalURL, pairing.RoomID(token))
	if err != nil {
		fatal(err)
	}
	defer closeConn()

	info := pairing.AgentInfo{HostPubHex: cfg.HostPubHex, MachineID: cfg.MachineID, Name: cfg.MachineName}
	ownerPub, err := pairing.RunResponder(ctx, mc, token, info)
	if err != nil {
		fatal(err)
	}
	ownerHex := hex.EncodeToString(ownerPub)
	if err := agent.PinOwner(*dir, ownerHex); err != nil {
		fatal(err)
	}
	fmt.Printf("✓ paired — trusting owner %s…\n", ownerHex[:16])
}
```

Add the needed imports to `cmd/tr-agent/main.go`: `context`, `encoding/hex`, `github.com/mdp/qrterminal/v3`, and `github.com/srcful/terminal-relay/go/internal/pairing`. Run `go get github.com/mdp/qrterminal/v3@latest` first.

- [ ] **Step 2: Build + smoke (prints a code + QR, then waits)**

Run:
```bash
cd go && go get github.com/mdp/qrterminal/v3@latest && go build -o /tmp/tr-agent ./cmd/tr-agent
/tmp/tr-agent pair --dir /tmp/pair-agent --name box --signal http://localhost:8443 &
sleep 1; kill %1 2>/dev/null
```
Expected: prints `trm pair <code>` and a QR block, then waits (we kill it).

- [ ] **Step 3: Commit**

```bash
git add go/cmd/tr-agent/main.go go/go.mod go/go.sum
git commit -m "feat(agent): tr-agent pair — token + QR + NNpsk0 responder"
```

---

## Task 6: `trm pair <code>` + end-to-end

**Files:** Modify `go/cmd/trm/main.go`; Create `go/internal/pairing/e2e_test.go`; modify `README.md`

- [ ] **Step 1: Add the `pair` subcommand to `trm`**

Add `case "pair": cmdPair(os.Args[2:])` to `main()` (and the usage line), then:

```go
func cmdPair(args []string) {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) != 1 {
		fatal(fmt.Errorf("usage: trm pair <code>   (the code printed by `tr-agent pair`)"))
	}
	signalURL, token, err := pairing.DecodeCode(rest[0])
	if err != nil {
		fatal(err)
	}
	idn, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	mc, closeConn, err := pairing.DialPair(ctx, signalURL, pairing.RoomID(token))
	if err != nil {
		fatal(err)
	}
	defer closeConn()

	info, err := pairing.RunInitiator(ctx, mc, token, idn.OwnerPub())
	if err != nil {
		fatal(err)
	}
	m := client.Machine{Name: info.Name, MachineID: info.MachineID, HostPubHex: info.HostPubHex, SignalURL: signalURL}
	if err := client.AddMachine(*dir, m); err != nil {
		fatal(err)
	}
	fmt.Printf("✓ paired machine %q — try: trm attach %s\n", m.Name, m.Name)
}
```

Add imports to `cmd/trm/main.go`: `context`, `github.com/srcful/terminal-relay/go/internal/pairing` (and `time` is already imported).

- [ ] **Step 2: Write the end-to-end test**

```go
// go/internal/pairing/e2e_test.go
package pairing_test

import (
	"context"
	"encoding/hex"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/pairing"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

func TestPairThroughSignalingServer(t *testing.T) {
	srv := httptest.NewServer(signal.New().Handler())
	defer srv.Close()

	token := pairing.NewToken()
	code := pairing.EncodeCode(srv.URL, token)

	_, ownerPub, _ := noise.GenerateStatic()
	_, hostPub, _ := noise.GenerateStatic()
	info := pairing.AgentInfo{HostPubHex: hex.EncodeToString(hostPub), MachineID: "mid42", Name: "box"}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	// Agent side (responder).
	gotOwner := make(chan []byte, 1)
	go func() {
		mc, closeConn, err := pairing.DialPair(ctx, srv.URL, pairing.RoomID(token))
		if err != nil {
			return
		}
		defer closeConn()
		op, err := pairing.RunResponder(ctx, mc, token, info)
		if err == nil {
			gotOwner <- op
		}
	}()
	time.Sleep(150 * time.Millisecond) // let the agent register the room first

	// Client side (initiator), decoding the code as `trm pair` would.
	signalURL, tok, err := pairing.DecodeCode(code)
	if err != nil {
		t.Fatal(err)
	}
	mc, closeConn, err := pairing.DialPair(ctx, signalURL, pairing.RoomID(tok))
	if err != nil {
		t.Fatal(err)
	}
	defer closeConn()
	got, err := pairing.RunInitiator(ctx, mc, tok, ownerPub)
	if err != nil {
		t.Fatalf("client pair: %v", err)
	}
	if got.MachineID != "mid42" || got.HostPubHex != info.HostPubHex {
		t.Fatalf("client pinned wrong info: %+v", got)
	}
	select {
	case op := <-gotOwner:
		if hex.EncodeToString(op) != hex.EncodeToString(ownerPub) {
			t.Fatal("agent pinned wrong owner")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent never paired")
	}
}
```

- [ ] **Step 3: Run the E2E + full suite**

Run: `cd go && go test ./internal/pairing/ -v && go test ./... && go test -race -count=1 ./internal/pairing/ ./internal/signal/`
Expected: PASS — agent + client pair through `tr-signal` by roomID, exchange + pin keys; race-free.

- [ ] **Step 4: Update the README pairing section**

Replace the manual dev-pin instructions with:
```markdown
### Pairing (Plan 5)

One tap instead of copying keys by hand:

    # on the machine:
    tr-agent pair --signal http://localhost:8443      # prints a code + QR, then waits
    # on the client:
    trm pair <code>                                   # done — machine added

Under the hood a one-time token is the PSK of an NNpsk0 handshake brokered (blind)
through `tr-signal` by `roomID = H(token)`; the two sides exchange and pin their
static keys. The signaling server never sees the token or any key.
```

- [ ] **Step 5: Commit**

```bash
git add go/cmd/trm/main.go go/internal/pairing/e2e_test.go README.md
git commit -m "feat(trm): trm pair <code>; E2E pairing through the signaling server"
```

---

## Self-review (completed during planning)

- **Spec coverage:** Implements the spec's "Pairing — simple is king" for the CLI:
  one-time token (now a copy-paste code + QR), MITM-resistant via the token (psk),
  no fingerprint comparison; browser learns/pins the host key and the agent pins the
  `owner_id`. The signaling server stays blind (only `roomID = H(token)` + opaque
  NNpsk0 frames cross it). The browser's JS NNpsk0 side is Plan 6.
- **Security:** `roomID` and `psk` are domain-separated SHA256 over the 128-bit
  token, so the server learning `roomID` reveals neither the token nor the psk.
  NNpsk0 (psk placement 0) authenticates both parties by the token and encrypts
  even the first message, so the exchanged static keys are authentic; a wrong token
  fails the handshake (`TestPairingFailsWithWrongToken`).
- **Placeholder scan:** no TBD/TODO; complete code in every code step; exact
  commands + expected output in every run step.
- **Type consistency:** `NewToken`/`EncodeCode`/`DecodeCode`/`RoomID`/`pskFromToken`,
  `AgentInfo{HostPubHex,MachineID,Name}`, `RunInitiator`/`RunResponder`, `DialPair`/
  `wsConn`, `pairRooms`/`rendezvous`/`handlePair`/`pairCopy`, reuse of
  `peer.MsgConn`/`peer.Pipe`, `noise.GenerateStatic`, `client.AddMachine`/`Machine`/
  `LoadOrCreateIdentity`/`OwnerPub`, `agent.LoadOrInit`/`PinOwner` are consistent
  across files and tests. The `/pair` route is added to `Handler()` and the `pair`
  field initialized in `New()`.
- **Invariants:** no terminal data and no token through the server; `internal/noise`/
  `internal/identity` untouched; `tr-agent pair-dev` stays as a fallback.
```
