# C1 + C2 — Locator seam + LAN-direct (QUIC + mDNS)

**Status:** Approved (2026-06-13). Implements **C1 + C2** of the mesh track in
`2026-06-10-north-star-mesh-wallet-identity-design.md`. CLI-only (the browser cannot do
mDNS or raw QUIC; it keeps using the relay). Entirely Go-side — **no web, no byte-identical
crypto gate touched.**

**Goal:** a `mir` client reaches a `mir up` node **on the same LAN with no relay** —
zero-config discovery (mDNS) + a direct QUIC transport — by inserting a `Locator` seam
under the existing Noise-KK session. The relay/WAN path is **unchanged**.

**Decisions (review, 2026-06-13):**
1. **Scope:** C1 + C2 together.
2. **LAN transport:** QUIC (self-signed + skip-verify; real auth is Noise-KK + binding
   inside). Not WebRTC, not raw TCP.
3. **Discovery:** mDNS/DNS-SD via a small zeroconf dependency.
4. **QUIC-everywhere is the destination, not this step.** WAN's hard part is NAT traversal
   (ICE), which QUIC alone doesn't solve; WebRTC already does it and ships. A future
   `QUICHolePunchLocator` (DCUtR + circuit-relay, north-star C4) drops into the same seam —
   captured here as a stated future, **not built now.**

---

## How a client reaches an agent today (precise)

- **Discovery = none.** The client knows a machine only from its stored `Machine` record
  (`client/store.go`): `{name, machine_id, host_pub, signal_url}` — no IP/host address.
- **Connect = relay brokers SDP, then P2P.** `client/attach.go` dials the relay
  `/attach` WebSocket, exchanges an SDP offer/answer, opens a WebRTC `DataChannel`, then
  runs Noise-KK over it. **Terminal traffic never touches the relay** — it is P2P + Noise
  once the DataChannel is live.
- **Agent = outbound only.** `agent/runtime.go` only *dials out* to the relay's
  `/agent/signal`; it has **no listener**, no mDNS, no LAN presence.
- **The seam already exists.** Noise-KK (`peer.RunInitiator/RunResponder`) and the PTY mux
  (`agent.RunAgentSession`) are transport-agnostic — they speak only `peer.MsgConn`
  (`Send([]byte)`, `Recv(ctx) ([]byte, error)`). The WebRTC `DataChannel` is one
  implementation. **A QUIC stream is another** — the crypto and session code need zero
  changes.

---

## C1 — the `Locator` seam (pure refactor, no behavior change)

A locator turns a `Machine` into a live, pre-Noise transport. It lives **in the `client`
package** (`go/internal/client/locator.go`), not a separate package — `Attach` composes
locators, so a separate `locator` package importing `client` for `Machine`/`Identity` would
be an import cycle. Keeping it in `client` is cycle-free and the implementations already
need `client`'s types.

```go
// go/internal/client/locator.go (package client)
type Locator interface {
    // Dial reaches m and returns a live MsgConn (post-transport, pre-Noise) plus a
    // cleanup. ErrUnreachable signals "I can't reach it" so Attach falls through to
    // the next locator; any other error aborts (it's a real failure on a reachable path).
    Dial(ctx context.Context, m Machine, id *Identity, ice []peer.ICEServer) (peer.MsgConn, func(), error)
}
var ErrUnreachable = errors.New("locator: machine not reachable by this path")
```

- **`RelayLocator`** wraps today's `attach.go` body (WS + offer/answer + ICE) and returns
  the opened `DataChannel` as the `MsgConn`. The binding still rides the SDP offer
  (B1.4.2). **Byte-identical behavior.**
- **`Attach`** becomes: try locators in order, take the first `MsgConn`, run
  `peer.RunInitiator(ctx, mc, id.OwnerPriv(), hostPub)`. The Noise + session loop is lifted
  out of `attach.go` unchanged and runs over whichever `MsgConn` a locator returned.

This is the whole of C1: no new transport yet, relay path preserved, but `Attach` now
composes locators. Unit-test: `RelayLocator` against the in-memory relay test harness still
drives a full session.

---

## C2 — LAN-direct (`LANLocator`: mDNS + QUIC)

### Discovery (mDNS / DNS-SD)
- **Agent (`mir up`)** registers `_miranda._udp.local`, instance name = `machine_id`, the
  QUIC listen port, and TXT `mid=<machine_id>`. (Ephemeral port, advertised — no fixed-port
  config.)
- **Client** browses `_miranda._udp`, matches each instance's `mid` against its known
  `Machine` records, resolves A/AAAA + port. Discovery yields only an **address** — never
  trust.

### Transport (QUIC)
- **TLS is QUIC's requirement, not our trust.** The agent generates an **ephemeral
  self-signed cert** at startup; the client dials with `InsecureSkipVerify: true` + ALPN
  `miranda/lan/v1`. Real authentication is Noise-KK (the pinned `host_pub`) + the wallet
  binding **inside** the QUIC stream — a TLS/QUIC MITM cannot complete Noise-KK without
  `host_priv`. (Redundant QUIC-TLS encryption under Noise is accepted: Noise is the real
  layer; QUIC-TLS is dumb transport.)
- **`quicConn` implements `peer.MsgConn`** over one bidirectional stream with 4-byte
  big-endian length-prefixed frames (a QUIC stream is a byte stream, so message boundaries
  are framed). ~40 lines.

### LAN wire (replaces the SDP offer's job of delivering the binding + pin)
1. Client QUIC-dials the mDNS-resolved `IP:port`, opens a bidi stream.
2. **Frame 0 = the binding record** (`id.BindingJSON`, the same B1.4 `{v,wallet,device,
   x25519,ts,sig}`), sent as the first `MsgConn.Send`.
3. Agent's accept handler does `Recv()` for frame 0, then **reuses
   `ownerPubFromBinding(bindingJSON, binding.wallet)` from B1.4.1**: `IsOwnerPinned(wallet)`
   → `VerifyBinding` → pin `binding.x25519`. A bad/unpinned/forged binding closes the
   stream with a clear error.
4. Noise-KK over the **same** stream: client `RunInitiator(id.OwnerPriv(), host_pub)`
   (the X25519 transport key + the pinned agent `host_pub` from the `Machine` record),
   agent `RunResponder(host_priv, pinnedX25519)`. Byte-for-byte the current code.
5. `RunAgentSession` / client session loop — unchanged.

Because the binding is just the first framed message and Noise messages are the subsequent
ones on the same ordered stream, `RunInitiator/RunResponder` are byte-for-byte the current
code.

### Agent presence (`mir up`)
Alongside the existing relay serve loop, `mir up` starts: a QUIC listener + the zeroconf
registration. Each accepted connection runs the frame0-verify → pin → `RunResponder` →
`RunAgentSession` path above (a shared helper with the relay path's post-pin logic).
**LAN is on by default** (`mir up --no-lan` opts out); the relay path always runs too.

### Attach ordering (client)
`Attach` composes `[LANLocator, RelayLocator]`. `LANLocator.Dial` does an mDNS lookup with a
short timeout (~1.5 s); on a hit it QUIC-dials + sends frame0 and returns the `quicConn`; on
no hit / dial failure it returns `ErrUnreachable` and `Attach` falls through to the relay
(today's path). A `mir attach --no-lan` / `--relay-only` flag forces the relay path.

---

## Trust model (unchanged) + new surface

- **Same invariant: locate but never impersonate.** Noise-KK pins `host_pub` (from the
  `Machine` record) and the agent pins the owner via the wallet binding. mDNS spoofing or a
  rogue LAN host yields at worst a **failed handshake (DoS)** — never impersonation or
  plaintext. A wrong `host_pub` fails the client's Noise-KK; an unpinned/forged wallet fails
  the agent's verify.
- **New attack surface: the agent now listens on the LAN.** Mitigations: reject non-pinned
  owners immediately (cheap, pre-Noise, on frame 0), bound concurrent LAN handshakes (reuse
  the agent's `admit()` semaphore), and `--no-lan` to disable. The QUIC listener binds to
  all interfaces but only LAN peers can route to it on a typical home/office network.
- **mDNS info leak:** advertises that a miranda node with `machine_id` exists on the LAN.
  `machine_id` is already opaque (random hex); acceptable for a personal LAN. `--no-lan`
  removes the advertisement entirely.
- **`SECURITY.md`** gains a short "LAN-direct" paragraph when this lands.

---

## Dependencies (non-crypto, Go-only)
- `github.com/quic-go/quic-go` — QUIC transport.
- `github.com/grandcat/zeroconf` — mDNS/DNS-SD register + browse.

Neither touches the byte-identical crypto path (base58/SLIP-0010/BIP39/Noise vectors). The
browser is unaffected (no web changes).

---

## Implementation order (TDD, small commits)
- **C1** `locator` package + `Locator` interface + `ErrUnreachable`; `RelayLocator` wrapping
  today's attach; refactor `Attach` to compose locators + run the lifted Noise/session loop.
  Relay e2e tests stay green (behavior-preserving).
- **C2.0** `quicConn` implementing `peer.MsgConn` (length-framed) + a round-trip frame test.
- **C2.1** `LANLocator.Dial`: mDNS browse → match `machine_id` → QUIC-dial → send frame0
  (binding) → return `quicConn`; `ErrUnreachable` on miss. Unit test with an in-process
  advertiser.
- **C2.2** agent: QUIC listener + zeroconf register in `mir up`; accept handler
  (frame0-verify → pin → `RunResponder` → `RunAgentSession`), sharing the post-pin helper
  with the relay path; `admit()` bound.
- **C2.3** wiring: `Attach` order `[LAN, Relay]`; `mir up --no-lan`, `mir attach
  --relay-only`.
- **C2.4** e2e: `mir up` + `mir attach` over QUIC on loopback with **no relay running**;
  mDNS resolve test; bad/missing binding rejected; relay fallback when LAN is absent. Extend
  `deploy/netsim` with an mDNS-within-a-Docker-network LAN path.

Each step is independently shippable; C1 alone is a pure refactor.

---

## Non-goals (now)
- **QUIC over WAN / NAT hole-punching (DCUtR).** Stated future locator; not built. The seam
  is shaped for it.
- **Browser LAN-direct.** Browsers can't do mDNS/raw QUIC; the browser keeps the relay.
- **Cross-wallet LAN sharing** (Track D seam only).
- **Dropping WebRTC/pion.** The WAN path stays on WebRTC until DCUtR is built and proven.
