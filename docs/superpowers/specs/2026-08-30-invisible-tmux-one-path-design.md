# Invisible tmux — one path, a warm client, an entry that shows your machines

> Successor to the v0.8-beta roadmap's "feel" track. Brief from Fredrik
> (2026-08-30): Miranda should feel the way Tailscale and ZeroTier feel — you
> stop thinking about it. It IS tmux, spanning machines and networks. One
> direct path that punches through whatever it must, one relay fallback, and
> nothing else. An entry screen that shows your machines. And session sharing,
> decentralized, on what is already built.

## The evidence (recon 2026-08-30)

- The LAN transport (mDNS + QUIC) is 558 LOC of transport-only code. Everything
  above `peer.MsgConn` is shared with the relay path. ICE host candidates are
  NOT disabled on the relay path (`peer.go` sets only timeouts), so
  relay-signaled WebRTC already forms a direct host↔host pair on a shared LAN.
  The QUIC path's only unique capability is attach with the relay unreachable.
- The locator race staggers the relay 200 ms behind the LAN probe
  (`relayHeadStart`, `attach.go:21`). Netsim's best-case attach is 229 ms —
  roughly 200 ms of that is the stagger. **The second transport costs ~87 % of
  our best-case latency and its own benefit was never measured** (netsim has no
  LAN scenario).
- A cold `mir attach` runs three sequential HTTPS round trips (revocations →
  registry → TURN credentials) before `client.Attach` even starts. Unmeasured,
  and likely the dominant term on a real WAN link. Nothing is warm between
  invocations.
- `mir` with no args prints static prose and exits 2. Bare `mir attach` errors.
  The CLI mux shows focus only in the xterm window title. The CLI discards the
  `FrameWindows` overview the agent already pushes.
- No per-attach tmux options exist, but the hook does: every attach spawns its
  own tmux client process whose PID→tty the agent already resolves.

## Decisions

### D1 — One transport, two modes (delete the second transport)

The data plane is WebRTC DataChannel with Noise-KK inside, signaled through the
blind relay. Two modes, exactly as ICE defines them:

1. **Direct, always preferred**: host candidates (same LAN), srflx (hole-punch
   across the internet). ICE already prefers direct pairs; peers do whatever
   they can to find each other.
2. **Relayed, the only fallback**: TURN, minted by our relay, when punching
   fails.

Consequences:

- Delete `internal/quicmsg`, `client/lan_locator.go`, `agent/lan.go`, the
  zeroconf + quic-go dependencies, and the locator race — one locator, zero
  stagger. Every attach starts immediately.
- Add a same-subnet scenario to netsim proving the host-candidate pair and its
  latency. The claim "direct on LAN" becomes a measured fact instead of a
  second transport.
- `mir up --no-lan` and `mir attach --relay-only` become no-ops → deprecate
  with a friendly note (accepted, warn, ignore) for one beta cycle.
- **What we give up**: attach while the relay is unreachable (CLI-only today,
  unmeasured, LAN-only). Recorded as a non-goal for now; the mesh track
  (federated relays / DHT / local signaling over mDNS) is the future home of
  offline, and it will reuse this single transport rather than resurrect a
  second one.
- Browser and CLI now share one path story. QUIC-native P2P remains a future
  note (DCUtR-style), not a parallel route.

### D2 — A warm client, no daemon (yet)

Tailscale's secret is a daemon. Ours can wait — three cheaper steps first:

1. **Collapse the pre-attach round trips**: fetch revocations, registry, and
   TURN credentials in parallel; cache each under the client state dir with a
   short TTL (revocations/registry ~30 s, TURN credentials until ~2 min before
   expiry); serve stale-while-revalidate so a warm cache means zero blocking
   round trips.
2. **The entry screen is the warm place** (D3): while `mir` is open it holds
   live sessions the way the SPA pool does — bounded, LRU, instant switch.
3. A background daemon (persistent registration, pre-punched connections) only
   if 1+2 miss the feel bar. Decision deferred, measured by netsim's cold-vs-
   warm numbers.

### D3 — `mir` opens your machines (a menu, not a multiplexer)

`mir` with no arguments on a TTY opens a live overview: your machines, one per
row — name, online/offline, retired, new-device flag, warm-session state — with
single-key actions: Enter attach, `r` rename, `x` retire, `s` share (D5),
`q` quit. Attaching from the overview joins the same process's warm pool; the
existing byte-level mux gains an overview key (prefix + `0`) to come back.

Guard rails, honoring the standing positions (`docs/product.md:59`, "no
tmux-in-tmux"):

- The overview is a **picker**. tmux owns windows and panes; the mux owns which
  machine; the overview owns which machine to go to first. No cell buffer, no
  panes, no layout engine — rows of text, a cursor, and keys, built on the raw
  terminal we already drive (x/term; no TUI framework unless rows-and-a-cursor
  genuinely cannot carry it).
- Non-TTY / `mir --help` keeps the static guide. Every existing command works
  unchanged; the overview is the zero-argument default, nothing else moves.
- Bare `mir attach` stops erroring: it attaches the last-used machine, or the
  only machine, or falls into the overview.
- The CLI starts consuming `FrameWindows` (it already arrives): the overview
  shows a one-line session/window summary per warm machine, and the mux gains
  a minimal focus line on switch (escape-sequence one-liner, not a status bar).

What the overview cannot show today — "last seen 2 h ago" — stays unshown. The
relay is stateless by design; presence is live membership, and we do not add
server-side memory for a nicety.

### D4 — tmux-native, per-attach (grouped sessions)

Every attach today runs `tmux new -A -s main`: all clients share one session,
so two viewers fight over the current window and the smallest screen wins.
Change the per-attach launch to a **grouped session**:

    tmux new-session -t main -s mir-<attach-id>

Same windows, same processes — but each attach gets its own current window and
its own size, and per-attach session options become possible (that is where a
read-only guest's `status off` or a distinct prefix would live, without ever
touching what the machine's local user sees). The agent already resolves each
attach's tmux client by PID; it gains cleanup of its `mir-*` session on
detach. tmux remains the only multiplexer in the system.

### D5 — Shared sessions (guests), decentralized

The owner mints an invite; a guest joins with a code or link; the relay stays
blind and stateless; everything expires and everything can be revoked. Built
from parts that already exist — pairing's NNpsk0 bootstrap, wallet-signed
grants, the signed-revocation channel, and D4's grouped sessions.

- **Mint**: `mir share <machine> [--ttl 1h] [--write] [--session <name>]` (and
  the SPA equivalent) creates a one-time invite code/link plus a **grant**: an
  owner-signed statement {guest may attach to machine M (session S), mode
  read-only|write, not-after T}. The invite bootstraps the guest exactly like
  pairing (blind NNpsk0 room, safety number); what gets pinned on the agent is
  the guest's key **bound to the grant**, not an owner.
- **Join**: `mir join <code>` or the web link. The guest sees only that
  machine, that session.
- **Enforce at the PTY, not by promise**: a read-only guest's attach launches
  `tmux attach -t <grouped-session> -r`; write mode is explicit opt-in. The
  agent checks `not-after` on every attach and drops live guest sessions when
  the grant expires or is revoked.
- **Revoke**: `mir share revoke` rides the existing signed-revocation channel.
  Expiry is enforced by the agent — never by the relay, which learns nothing
  beyond the usual opaque room metadata.
- Grants are per-machine and offline-verifiable (signed by the owner the agent
  already pins). No accounts, no server state, no third parties.

Security review before build: grant format, replay, TTL bounds (default 1 h,
max 24 h), interplay with machine revocation, and what a hostile guest can
reach (answer must be: that tmux session, nothing else — no registry, no other
machines, no share-minting).

## Slices

| # | Slice | Track |
|---|---|---|
| T1 | Delete the LAN transport; one locator, zero stagger; netsim same-subnet scenario proving direct-on-LAN; deprecate `--no-lan`/`--relay-only` | Transport |
| T2 | Pre-attach round trips: parallel + cached (stale-while-revalidate); netsim measures cold vs warm attach end-to-end including these | Warmth |
| O1 | `mir` overview: live machine list, single-key actions, warm pool, bare-attach default, FrameWindows in the CLI | Entry |
| D4 | Grouped per-attach sessions + detach cleanup | tmux-native |
| G1 | Guest grants: spec-level security review → mint/join/revoke, read-only default, agent-enforced expiry | Sharing |

Order: T1 → T2 → O1 → D4 → G1. T1/T2 first — every later slice inherits their
latency. G1 last and gated on its security review.

## Not doing

- No client daemon (yet) — revisit after T2+O1 with netsim numbers.
- No server-side "last seen" — the relay stays stateless.
- No TUI framework, no panes, no second multiplexer, no local-tmux nesting.
- No second transport, ever again, without a measured capability gap.
