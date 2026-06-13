# C1 + C2 — Locator seam + LAN-direct (QUIC + mDNS) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** `mir attach` reaches a `mir up` node on the same LAN with no relay — mDNS
discovery + a direct QUIC transport — via a `Locator` seam under the unchanged Noise-KK
session. Relay/WAN path unchanged. CLI-only, Go-only.

**Architecture:** a `Locator` (in package `client`) turns a `Machine` into a live
`peer.MsgConn`; `Attach` composes `[lanLocator, relayLocator]` and runs Noise-KK over the
first that connects. LAN uses QUIC (self-signed + skip-verify; real auth = Noise-KK +
binding) and discovers addresses through a pluggable `resolver` (mDNS in prod, injected in
tests). Spec: `docs/superpowers/specs/2026-06-13-c1-c2-lan-direct-locator-design.md`.

**Tech stack:** Go; `github.com/quic-go/quic-go`, `github.com/grandcat/zeroconf`.

**Cross-cutting facts:**
- The transport seam is `peer.MsgConn` (`Send([]byte) error`, `Recv(ctx) ([]byte, error)`).
  Noise (`peer.RunInitiator/RunResponder`) and `agent.RunAgentSession` are unchanged.
- The agent's binding verify+pin helper `ownerPubFromBinding(bindingJSON, owner string)
  ([]byte, error)` already exists (B1.4.1, `agent/runtime.go`) — reuse it for LAN frame 0.
- `Machine` = `{name, machine_id, host_pub, signal_url}` (`client/store.go`); `Identity`
  has `OwnerPriv()`, `WalletAddress`, `BindingJSON`, `HasWallet()`.
- Discovery yields only an address; trust stays Noise-KK pin + wallet binding.

---

## Task 1 — C1: Locator seam + RelayLocator (pure refactor)

**Files:**
- Create: `go/internal/client/locator.go`
- Modify: `go/internal/client/attach.go`
- Test: `go/internal/client/locator_test.go` (+ existing e2e must stay green)

- [ ] **Step 1: Define the seam.** `locator.go`:
  ```go
  package client

  import (
      "context"
      "errors"
      "github.com/srcful/terminal-relay/go/internal/peer"
  )

  // ErrUnreachable means this locator can't reach the machine; Attach falls through
  // to the next locator. Any other error aborts (a real failure on a reachable path).
  var ErrUnreachable = errors.New("locator: machine not reachable by this path")

  // Locator turns a Machine into a live MsgConn (post-transport, pre-Noise).
  type Locator interface {
      Dial(ctx context.Context, m Machine, id *Identity, ice []peer.ICEServer) (peer.MsgConn, func(), error)
  }
  ```

- [ ] **Step 2: Extract RelayLocator from attach.go.** Move the WS-dial + offer/answer + ICE
  wait (today's `attach.go` body up to the opened `DataChannel`) into:
  ```go
  type relayLocator struct{}

  func (relayLocator) Dial(ctx context.Context, m Machine, id *Identity, ice []peer.ICEServer) (peer.MsgConn, func(), error) {
      // ... today's attach.go: dial /attach WS, send offer {SDP, Binding: id.BindingJSON},
      // accept answer, wait for the DataChannel; on timeout return ErrUnreachable.
      // Return (mc, cleanup, nil).
  }
  ```
  Keep the offer's `Binding: id.BindingJSON` exactly as today.

- [ ] **Step 3: Recompose Attach.** `Attach` builds `locators := []Locator{relayLocator{}}`
  (LAN prepended in Task 6), iterates: `mc, cleanup, err := loc.Dial(...)`; on
  `ErrUnreachable` continue, on other error return it, on success break. Then the **lifted,
  unchanged** Noise + return:
  ```go
  hostPub, err := hex.DecodeString(m.HostPubHex) // as today
  sess, err = peer.RunInitiator(ctx, mc, id.OwnerPriv(), hostPub)
  // ... return mc, sess, cleanup, nil  (same signature as today)
  ```
  Preserve `Attach`'s current exported signature.

- [ ] **Step 4: Run.** `cd go && go test ./internal/client/` — the existing e2e
  (`TestEndToEnd*`) must stay green (behavior-preserving). Add a small `locator_test.go`
  asserting `ErrUnreachable` fall-through with a stub locator. `go vet`, `gofmt`.

- [ ] **Step 5: Commit.** `refactor(client): C1 Locator seam + RelayLocator (no behavior change)`

---

## Task 2 — C2.0: QUIC MsgConn

**Files:**
- Create: `go/internal/client/quicconn.go` + `quicconn_test.go`
- Modify: `go/go.mod` (add `github.com/quic-go/quic-go`)

- [ ] **Step 1: Add the dep.** `cd go && go get github.com/quic-go/quic-go@latest`.

- [ ] **Step 2: Test first (round-trip framing).** `quicconn_test.go`: stand up a QUIC
  listener on `127.0.0.1:0` (helper makes a self-signed `tls.Config` with ALPN
  `miranda/lan/v1`), dial it, wrap both ends' streams in `quicConn`, and assert several
  `Send`/`Recv` frames (incl. an empty frame and a 70 KB frame) round-trip exactly and in
  order.

- [ ] **Step 3: Implement.** `quicconn.go`:
  ```go
  // quicConn adapts one QUIC bidi stream to peer.MsgConn with 4-byte big-endian
  // length-prefixed frames (a QUIC stream is a byte stream; framing preserves the
  // message boundaries Noise/agent code expects).
  type quicConn struct {
      stream quic.Stream
      conn   quic.Connection
  }
  func (q *quicConn) Send(b []byte) error {
      var hdr [4]byte
      binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
      if _, err := q.stream.Write(hdr[:]); err != nil { return err }
      _, err := q.stream.Write(b)
      return err
  }
  func (q *quicConn) Recv(ctx context.Context) ([]byte, error) {
      // honor ctx via stream.SetReadDeadline on ctx.Done (or a goroutine); read 4-byte
      // length then exactly that many bytes (io.ReadFull). Reject absurd lengths (> maxFrame).
  }
  ```
  Add `const maxFrame = 1 << 20`. Helpers: `selfSignedTLS() *tls.Config` (server, ALPN) and
  `clientTLS() *tls.Config` (`InsecureSkipVerify: true`, same ALPN) — shared by Tasks 3/4.

- [ ] **Step 4: Run.** `cd go && go test ./internal/client/ -run QUIC -v`; `go vet`, `gofmt`.

- [ ] **Step 5: Commit.** `feat(client): C2.0 QUIC MsgConn (length-framed stream)`

---

## Task 3 — C2.1: LANLocator (resolver + QUIC dial + frame0)

**Files:**
- Create: `go/internal/client/lan_locator.go` + `lan_locator_test.go`
- Modify: `go/go.mod` (add `github.com/grandcat/zeroconf`)

- [ ] **Step 1: Resolver seam (for deterministic tests).**
  ```go
  // resolver maps a machine_id to a dialable "host:port" on the LAN. mdnsResolver is
  // the prod impl; tests inject a static one so the QUIC/Noise path is exercised without
  // multicast (flaky in CI).
  type resolver interface {
      resolve(ctx context.Context, machineID string) (addr string, err error) // ErrUnreachable on miss
  }
  ```

- [ ] **Step 2: Test first.** `lan_locator_test.go`: an in-process QUIC echo/agent stub on
  loopback + a static `resolver` returning its address. Assert `lanLocator.Dial` (a) sends
  frame 0 = `id.BindingJSON` (the stub reads and checks it), (b) returns a usable
  `quicConn`, and (c) returns `ErrUnreachable` when the resolver misses.

- [ ] **Step 3: Implement Dial.**
  ```go
  type lanLocator struct{ res resolver }
  func (l lanLocator) Dial(ctx context.Context, m Machine, id *Identity, _ []peer.ICEServer) (peer.MsgConn, func(), error) {
      if !id.HasWallet() { return nil, nil, ErrUnreachable } // LAN needs a binding
      addr, err := l.res.resolve(ctx, m.MachineID)
      if err != nil { return nil, nil, ErrUnreachable }
      conn, err := quic.DialAddr(ctx, addr, clientTLS(), nil)
      if err != nil { return nil, nil, ErrUnreachable }
      stream, err := conn.OpenStreamSync(ctx)
      if err != nil { _ = conn.CloseWithError(0, ""); return nil, nil, ErrUnreachable }
      mc := &quicConn{stream: stream, conn: conn}
      if err := mc.Send([]byte(id.BindingJSON)); err != nil { /* close */ return nil, nil, ErrUnreachable }
      cleanup := func() { _ = conn.CloseWithError(0, "") }
      return mc, cleanup, nil
  }
  ```

- [ ] **Step 4: mdnsResolver (prod).** Using `grandcat/zeroconf`, browse `_miranda._udp` for
  ~`resolveTimeout` (1.5 s), match an entry whose TXT `mid=` (or instance) equals
  `machineID`, return `net.JoinHostPort(entry.AddrIPv4[0], entry.Port)`; `ErrUnreachable` on
  timeout/miss. (No unit test for live multicast here — covered by the skippable mDNS
  integration test in Task 7.)

- [ ] **Step 5: Run.** `cd go && go test ./internal/client/ -run LAN -v`; `go vet`, `gofmt`.

- [ ] **Step 6: Commit.** `feat(client): C2.1 LANLocator (mDNS resolve + QUIC + frame0 binding)`

---

## Task 4 — C2.2: agent QUIC listener + mDNS advertise + accept

**Files:**
- Create: `go/internal/agent/lan.go` + `lan_test.go`
- Modify: `go/internal/agent/runtime.go` (start LAN alongside the relay loop)

- [ ] **Step 1: Factor the post-pin path.** Extract the relay `handleOffer`'s
  post-DataChannel logic into a shared helper so LAN reuses it verbatim:
  ```go
  // serveAuthenticated runs RunResponder(host_priv, ownerPub) then the PTY session over mc.
  func (rt *Runtime) serveAuthenticated(ctx context.Context, mc peer.MsgConn, ownerPub []byte) error
  ```
  `handleOffer` calls it after `ownerPubFromBinding`; LAN calls it too.

- [ ] **Step 2: Test first.** `lan_test.go`: start `rt.startLAN` on `127.0.0.1:0` (expose the
  chosen addr), then from a test client QUIC-dial + send a **valid** binding frame0 for a
  pinned owner + run `peer.RunInitiator` and assert the Noise session establishes and a PTY
  echo round-trips. Negative: an **unpinned/forged** binding closes the stream pre-Noise.

- [ ] **Step 3: Implement lan.go.**
  ```go
  func (rt *Runtime) startLAN(ctx context.Context) (stop func(), err error) {
      ln, err := quic.ListenAddr("0.0.0.0:0", selfSignedTLS(), nil) // ephemeral port
      // zeroconf.Register("<machine_id>", "_miranda._udp", "local.", port,
      //    []string{"mid=" + rt.cfg.MachineID}, nil)
      // accept loop: for { conn := ln.Accept(ctx); go rt.lanAccept(ctx, conn) }
  }

  func (rt *Runtime) lanAccept(ctx context.Context, conn quic.Connection) {
      if !rt.admit() { _ = conn.CloseWithError(0, "busy"); return } // reuse DoS bound
      defer rt.release()
      stream, err := conn.AcceptStream(ctx); if err != nil { return }
      mc := &quicConn{stream: stream, conn: conn}
      bindingJSON, err := mc.Recv(ctx); if err != nil { return }                 // frame 0
      sb, err := identity.ParseSignedBinding(bindingJSON); if err != nil { return }
      if !rt.cfg.IsOwnerPinned(sb.Wallet) { return }                              // pinned?
      ownerPub, err := ownerPubFromBinding(string(bindingJSON), sb.Wallet); if err != nil { return }
      _ = rt.serveAuthenticated(ctx, mc, ownerPub)                                // shared path
  }
  ```
  (`quicConn`/`selfSignedTLS` live in package `client`; move the shared QUIC helpers to a
  small internal package, e.g. `go/internal/quicmsg`, imported by both `client` and `agent`,
  to avoid duplication and any client↔agent import. Adjust Task 2/3 imports accordingly.)

- [ ] **Step 4: Run.** `cd go && go test ./internal/agent/ -run LAN -v`; full
  `go test ./internal/agent/`; `go vet`, `gofmt`.

- [ ] **Step 5: Commit.** `feat(agent): C2.2 QUIC LAN listener + mDNS advertise + binding-gated accept`

---

## Task 5 — C2.3: wiring (attach order + flags) + mir up start

**Files:**
- Modify: `go/internal/client/attach.go` (prepend lanLocator)
- Modify: `go/internal/agent/runtime.go` or the `mir up` command (`go/internal/cli/*`) — start
  LAN unless `--no-lan`
- Modify: `go/internal/cli/*` — `mir up --no-lan`, `mir attach --relay-only`
- Test: `go/internal/cli/*_test.go` (flag parsing)

- [ ] **Step 1:** `Attach` builds `[]Locator{lanLocator{res: newMDNSResolver()}, relayLocator{}}`
  unless `--relay-only`, then `[]Locator{relayLocator{}}`. Thread the flag through Attach's
  call site.
- [ ] **Step 2:** `mir up` starts `rt.startLAN` unless `--no-lan`; ensure clean shutdown
  (call `stop()` on ctx cancel; unregister mDNS).
- [ ] **Step 3:** Flag-parse tests for `--no-lan` / `--relay-only`. Run
  `cd go && go test ./internal/cli/`.
- [ ] **Step 4: Commit.** `feat(cli): C2.3 LAN-first attach, mir up --no-lan, mir attach --relay-only`

---

## Task 6 — C2.4: e2e (no relay) + netsim + docs

**Files:**
- Create: `go/internal/client/lan_e2e_test.go` (or under `agent/`)
- Create/modify: `deploy/netsim/*` (LAN-within-a-Docker-network path)
- Modify: `SECURITY.md` (LAN-direct paragraph), `README.md` (mention LAN-direct + `--no-lan`)
- Test (skippable): real mDNS register+browse on loopback (build tag or `testing.Short` skip)

- [ ] **Step 1: e2e — full path, no relay.** Start `rt.startLAN` (real agent runtime, real
  PTY echo), construct a client with a **static resolver** pointed at the listener, and run
  `Attach` → assert a shell command round-trips end-to-end with **no `mir-signal` process**.
  Negative: with a bad binding, attach fails and falls through (here, to a non-existent relay
  → clean error).
- [ ] **Step 2: mDNS integration (skippable).** A `TestMDNSResolveLoopback` that registers
  via zeroconf and browses for it; `t.Skip` under `-short` or when multicast is unavailable,
  so CI stays deterministic.
- [ ] **Step 3: netsim.** Extend `deploy/netsim` with two nodes on one Docker network doing
  LAN-direct (mDNS resolves, relay container absent/blocked); document the make target.
- [ ] **Step 4: docs.** `SECURITY.md` LAN-direct threat note (listener surface, mitigations);
  `README.md` one line + `--no-lan`.
- [ ] **Step 5: full gates.** `cd go && go test ./... && go vet ./... && gofmt -l .` clean;
  `cd web && npm test` (must be unaffected — sanity).
- [ ] **Step 6: Commit.** `test(lan): C2.4 relay-less e2e + netsim + SECURITY/README`

---

## Self-review notes
- **Spec coverage:** Tasks 1–6 cover C1 (seam+relay refactor), C2.0 (QUIC MsgConn), C2.1
  (LANLocator), C2.2 (agent listener/advertise/accept), C2.3 (wiring/flags), C2.4
  (e2e/netsim/docs). QUIC-WAN/DCUtR is a stated non-goal.
- **Import hygiene:** shared QUIC helpers (`quicConn`, TLS configs) live in a small
  `internal/quicmsg` package imported by both `client` and `agent` (no client↔agent cycle);
  the `Locator` interface lives in `client` (Attach composes it).
- **Type consistency:** `Locator.Dial(ctx, m Machine, id *Identity, ice)`, `ErrUnreachable`,
  `resolver.resolve`, `quicConn{stream, conn}`, `serveAuthenticated(ctx, mc, ownerPub)`,
  reuse of `ownerPubFromBinding` are referenced consistently across tasks.
- **Determinism:** mDNS multicast is isolated behind `resolver`; all core tests use loopback
  QUIC + injected addresses. Live mDNS is a single skippable test.
