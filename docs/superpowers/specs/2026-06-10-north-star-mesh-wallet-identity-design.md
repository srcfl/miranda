# Miranda North Star — Wallet Identity, Unified Node, Decentralized Mesh

**Status:** Draft for review (2026-06-10). Umbrella vision; not a single implementation plan.
**Author:** Fredrik + Claude (brainstorm session 2026-06-10).
**Supersedes nothing.** Builds on `2026-06-04-terminal-relay-design.md` (core) and
`2026-06-08-distribution-and-self-update-design.md` (release/update).

> **How to read this doc.** This is the *north star* — one coherent end-state that
> ties together three sub-projects. It is deliberately an umbrella: each track below
> (A unified node, B wallet identity, C mesh) gets its **own** spec → plan →
> implementation cycle. Nothing here authorizes a single mega-PR. The point is the
> *dividing line* between the simple core we ship now and the vision we layer later,
> and a trust model that holds at every step.

---

## The problem (re-anchored)

You have agents (Claude Code, Codex) and shells scattered across a laptop, an office
Mac mini, and a Linux box. Reaching the *right* one today means `ssh`, tunnels,
port-forwarding, key-juggling, and quiet swearing. The thing you actually want:

> Open **anything** — your phone's browser, a CLI on another machine — and there is
> the target machine's shell/agent, **alive, exactly as you left it.** Securely.
> Without SSH. Regardless of network. Passkey-authenticated. Trustless.

The core of this already exists (v0.1.0): CLI, peer-to-peer WebRTC + Noise `KK`,
`tmux` persistence, a blind relay. This doc is about (1) finishing and simplifying
that core, and (2) an optional vision layer — a wallet-rooted, Solana-compatible
identity and a decentralized mesh — that adds ecosystem visibility and "as serverless
as physics allows" reach **without compromising the simple, trustless core**.

---

## The dividing line: Core vs Vision

This is the spine of the whole doc.

| | **Core** (simple, insanely good, ~80% done) | **Vision** (ambition, visibility, later) |
|---|---|---|
| What | Secure SSH-less persistent terminals + agents, browser & CLI, any network, passkey, trustless | Solana-compatible wallet identity; decentralized mesh discovery |
| Status | Mostly built in v0.1.0; needs unify + browser + polish | New work, sequenced, each step independently shippable |
| Depends on Vision? | **No.** The whole promise is delivered by Core alone. | Builds *on top of* Core; never regresses it |
| Principle | "Simple Over Clever", "Robust Over Feature-Rich" | "Crypto-enabled, not crypto-dependent", "Local Over Cloud" |

You can stop after Core and have a complete product. Vision is opt-in ambition.

---

## Decisions (resolved forks — for traceability)

These were settled in the 2026-06-10 brainstorm:

1. **Design altitude → North-star umbrella** (this doc), then per-track specs/plans.
2. **Wallet model → passkey-derived** Ed25519/Solana key (no external wallet now;
   forward-compatible to bring-your-own via signed delegation later).
3. **Mesh scope → personal-first** (one wallet = all your devices), with a clean
   *seam* for cross-wallet sharing later. Cross-wallet is **not** built now.
4. **Discovery north star → maximalist:** a relay-less DHT keyed by wallet pubkey is a
   *stated goal*, reached via federated relays and LAN-direct as stepping stones.
   (Honest: even the DHT end-state needs relays for NAT traversal — they stay blind.)
5. **Backup/HD → full BIP39 + SLIP-0010 HD wallet:** exportable 24-word phrase
   (opt-in), HD sub-accounts, importable into Phantom. Default stays passkey-only.

---

## 1. Identity & crypto backbone

### Entity model (maps onto Sourceful's `WALLET → SITE → DEVICE → DER`)

```
WALLET   = Solana/Ed25519 key   → root of ownership, global ID (base58), no issuer
   │  signs device binding
DEVICE   = a machine running a mir node → holds an X25519 transport key
   │
SESSION  = one live attach (Noise KK inside WebRTC)
```

`SITE` (a logical grouping of devices — "home"/"office") is left as a **future seam**,
not built now. `DER` is n/a for a terminal. The `WALLET → DEVICE` root-of-ownership
mapping is the part that matters and it is exact.

### Key derivation — one passkey root, domain-separated keys

```
passkey prf (32 bytes)  =  BIP39 entropy (256-bit)
        │
        ├─▶ 24-word phrase (BIP39)                         ← revealed ON DEMAND, opt-in backup
        │        └─▶ PBKDF2 → seed → SLIP-0010 m/44'/501'/i'/0'  → Solana HD accounts (WALLET, identity)
        │
        └─▶ HKDF(info = "miranda/owner/x25519/v1") → X25519 transport  (byte-identical to today)
```

Design decisions, explicit:

- **Two independent keys from one root — no birational Ed25519→X25519 conversion.**
  We never reuse the same key bytes for both signing and DH. The wallet (Ed25519) and
  the transport (X25519) share only the 32-byte prf root, via separate KDF domains.
- **The transport X25519 derivation stays byte-identical to today** (same salt/info as
  `go/internal/identity/owner.go` uses now). We *add* the Ed25519/BIP39 track and
  re-anchor the namespace on the Solana address; the existing Noise interop vectors do
  not churn.
- **`prf` is the entropy, not a separate seed.** The 24-word phrase is a deterministic
  *rendering* of the passkey-derived secret, not a second root. Default: never shown.
  Opt-in: `mir wallet export-phrase` reveals it; restoring from the phrase reconstructs
  the prf entropy → re-derives everything **without** the passkey. Treat the phrase as
  maximum-sensitivity — it backs up the whole identity (wallet *and* transport).
- **Phantom-importable** via the standard path `m/44'/501'/0'/0'`. A real, fundable,
  explorer-visible Solana address — that is the "extra visibility", off-chain.
- **CLI identity unifies with browser:** the non-passkey CLI identity also becomes a
  BIP39 wallet (a generated, exportable phrase). Both are standard Solana HD wallets.

### Binding (the seam for everything)

The wallet key (Ed25519) **signs** an X25519 transport key → a small self-cert:
*"wallet W authorizes transport key X for device D."* A node serving `mir up` publishes
this signed record; any device holding the wallet can fetch and verify it, then accept
the bound transport key for Noise `KK`. The same primitive later enables (a) cross-wallet
capability grants and (b) an external bring-your-own wallet signing the binding —
without breaking anything.

### Auth

Pairing/attach includes a **wallet signature over a fresh challenge** (sign-in-with-Solana
style; off-chain, verifiable, ecosystem-standard). The relay still sees only ciphertext
plus wallet-addressed routing metadata (base58 instead of hex). **Blind-relay invariant
intact.**

### Crypto discipline & invariants (do not break)

- Ed25519 + BIP39 + SLIP-0010 + base58 added to **both** Go (`crypto/ed25519` stdlib +
  the `noble`/`scure` equivalents) and JS (`@noble/curves/ed25519`, already imported,
  plus `@scure/bip39` and a SLIP-0010 helper from the same audited family).
- New `testdata/` vectors gate byte-identical derivation, signing, and the binding
  signature, exactly as the Noise vectors do today. `UPDATE_VECTORS=1` regenerates.
- The passkey-prf invariant holds unchanged: the private key is never stored; it is
  re-derived per device from the synced passkey (or restored from the phrase).

### Open question for the B1 spec (flagged, not decided here)

Whether the Noise-KK *client* side stays the shared owner-X25519 (minimal churn:
wallet sits on top as ID + signs per-device host-key bindings) or moves to per-device
X25519 keys uniformly (more symmetric, larger pinning change). The umbrella position:
wallet anchors identity and signs bindings over X25519 transport keys; the data plane
(WebRTC + Noise KK) is unchanged. The B1 spec picks the exact key topology.

---

## 2. Unified symmetric node + the relay's role

### One binary, symmetric node

Today `mir` (client) and `mir-agent` (server) are two binaries. The north star is one
`mir` binary; every node can both *serve* and *attach*.

```
mir up                 serve this machine        (was: mir-agent up)
mir attach <name...>   reach your machines       (unchanged)
mir list               your devices              (now from the mesh registry)
mir pair [<code>]      cross-wallet introduction (no-arg = be pairable; with code = pair)
mir wallet <…>         address | export-phrase | import-phrase | accounts
mir self-update        unchanged
```

`mir-signal` stays a **separate binary** (decided): different operational profile
(a long-lived public service), and keeping it separate keeps the node binary small and
the trust boundary crisp.

### Self-certifying device registry (the payoff of wallet-rooted identity)

In a one-wallet mesh, every device shares the same wallet (passkey sync or phrase). A
new device is therefore trusted *immediately*; it only needs to publish a wallet-signed
device record (`device-name → device-X25519, signed by the wallet`) to the mesh. Any of
your devices can fetch and verify these.

```
add a device  =  restore wallet (passkey/phrase)  +  self-publish a signed record
              →  it appears everywhere; no manual add-machine, no SAS between your own devices
```

This *is* the README's magic ("there are your machines, alive"). The existing NNpsk0 +
safety-number pairing is **repurposed** for the future cross-wallet seam (where a SAS
between trust domains genuinely matters); between your own devices it is unnecessary.

### Discovery seam (what makes the staged mesh tractable)

```go
// the node composes several locators; the data plane (WebRTC + Noise) is identical regardless
type Locator interface { Find(ctx, wallet, device) (path, error) }
    RelayLocator  // today → federated
    MDNSLocator   // LAN-direct, free P2P
    DHTLocator    // north star: Kademlia keyed by wallet pubkey
```

The node tries LAN → relay → DHT. Adding a stage is a new `Locator`, not a rewrite.

### The relay's shrunk, still-blind role (all three at once, all blind)

- **Rendezvous / introducer** — brokers the WebRTC offer/answer, now wallet-addressed
  and federated.
- **DHT bootstrap** — the hosted relays double as bootstrap peers.
- **Circuit-relay / TURN** — unavoidable NAT infrastructure; Noise inside → it sees only
  ciphertext.

Wallet-signed records + Noise `KK` mean relays and DHT nodes can **locate but never
impersonate**. Same invariant, extended.

### tmux: the shared-server property (and its honest edges)

Verified in code: the node runs plain `tmux` against the **default per-user socket**,
no `-L`/`-S` override. `mir up` runs `tmux new -A -s main` (`go/internal/agent/pty.go`)
with the inherited environment, and the overview enumerates the **whole server** via
`tmux list-windows -a` (`go/internal/agent/windows.go`). Consequences:

- **Bidirectional, shared userspace:** a session you start by hand locally shows up in
  Miranda, and a Miranda-created session shows up in your local `tmux ls`. Switch /
  select / rename / kill / new all act on that one server.
- **Same-user requirement:** tmux servers are keyed by `$UID`. Running the node as a
  dedicated service user or root (e.g. a system-level systemd unit) gives a *different*
  server — your personal sessions would not be visible. **Run `mir up` as your own
  user** to get the unified view. (This is open issue **#21 "non-root agent".**)
- **Default socket only:** sessions started with a named socket (`tmux -L work`) or a
  custom `-S`/`TMUX_TMPDIR` are a separate server and won't appear.
- **No tmux-in-tmux:** launching `mir up` from inside a tmux nests badly (README).

Design consequence: "run `mir up` as your own user" is the documented default for the
unified node; a hardened multi-user deployment trades the shared-server magic away
deliberately.

### Back-compat

`mir-agent` becomes a thin compatibility shim (a symlink/wrapper that warns and forwards
to `mir up`/`mir …`), removed after a deprecation window. The config directory
(`~/.terminal-relay`) is kept to avoid migration churn (the Go module path is internally
unchanged regardless).

---

## 3. Mesh evolution, trust model, testing

### Trust model, extended to every step (this *is* the project)

- **Self-certifying identity:** wallet pubkey = ID, no issuer. Records are wallet-signed
  → relays/DHT/mDNS responders can **locate but never impersonate**.
- **Data plane unchanged:** WebRTC + Noise `KK` inside every path → relay/DHT see only
  ciphertext.
- **What decentralization *adds* is only an availability risk (DoS), never impersonation
  or eavesdropping.** In a personal mesh you look up only your *own* signed records — a
  hostile DHT node can withhold (DoS) but cannot forge a record (signature fails) or
  read traffic (Noise). This sharply lowers the DHT trust requirement versus a
  general-purpose DHT.
- **`SECURITY.md` is extended** with the mesh threat model when Track C lands.

### Sequenced roadmap — Core → Vision, each track free-standing

```
CORE (simple, insanely good — ~80% done in v0.1.0)
  A1  Unified node binary  (mir up/attach/list/self-update, mir-agent shim)   ← small, low risk
  A2  Finish the browser client (the phone)        ← the real remaining core gap
  A3  Polish persistence / reconnect               ← mostly done
  ▶ After A: the whole promise is delivered (secure, SSH-less, persistent, browser+CLI,
    any network, passkey, trustless)

VISION — keystone: identity (additive, "extra visibility")
  B1  Wallet identity: passkey-prf → BIP39 → SLIP-0010 Solana HD; X25519 byte-identical;
      wallet signs binding; `mir wallet …`; export/import phrase; new testdata vectors
  B2  Wallet-signed device registry (replaces add-machine + same-wallet pairing)

VISION — mesh (each step = a drop-in Locator; Core never regresses)
  C1  Locator interface + put today's relay behind RelayLocator   ← pure seam, no behavior change
  C2  LAN-direct (MDNSLocator)        ← free P2P on the same network; immediate serverless win
  C3  Federated blind relays          ← multiple introducers, wallet-addressed, run-your-own + gossip
  C4  DHT (DHTLocator)                ← north star: Kademlia on wallet pubkey; relays = bootstrap + circuit-relay

PARKED (future; the seam exists)
  D   Cross-wallet capability delegation (share access between wallets) — enabled by B1's wallet signature
```

No step forces the next. Track A delivers the simple product; B adds Solana visibility
without touching the core; C decentralizes incrementally.

### Testing strategy (same discipline as distribution/self-update)

- **Crypto:** `testdata/` byte-identical Go↔JS vectors gate the new Ed25519 / BIP39 /
  SLIP-0010 / binding-signature derivations (the existing gate, extended;
  `UPDATE_VECTORS=1` to regenerate).
- **NAT/mesh:** `deploy/netsim` already reproduces real NAT traversal in Docker; extend
  it per Locator (mDNS within a Docker network, federated relays, DHT bootstrap).
- **Persistence/reconnect:** keep the existing e2e tests green
  (`runtime_reconnect_test`, `e2e_mux_test`).
- **TDD per task:** RED → GREEN → commit, as in the last feature.

---

## Non-goals (now)

- **Cross-wallet sharing** (Track D) — seam only; not built.
- **Anything on-chain.** The wallet is for ID/auth/visibility. No transactions, no
  contracts, no token-gating.
- **A general-purpose DHT** beyond locating your own (and later, granted) signed records.
- **Renaming the config dir / Go module path.** Out of scope; coupled to a separate
  rename change if ever done.
- **Hardware-wallet / external-wallet custody.** Forward-compatible, not in scope now.

## Risks & open questions

- **Exportable phrase = new opt-in attack surface.** Default security is unchanged
  (passkey-only until you export), but the phrase is as powerful as the passkey. Mitigate
  with clear UX, reveal-once, and explicit warnings.
- **Namespace migration** from X25519-hex `owner_id` to base58 Solana address touches the
  relay routing key and the agent's pinned-owner format. Needs a migration story (derive
  both from one prf; accept both during a window). Detailed in the B1 spec.
- **DHT still needs relays for NAT** (circuit-relay/DCUtR). The "relay-less" claim is about
  *discovery*, not connectivity. Be precise in docs and `SECURITY.md`.
- **DHT attack surface** (eclipse/Sybil) — bounded for a personal mesh by self-certifying
  lookups, but real for the future cross-wallet case.
- **Crypto-library parity** Go↔JS for BIP39/SLIP-0010 must be byte-identical or the
  vectors fail. Pick the libraries deliberately (stay in the `noble`/`scure` family).
- **tmux same-user requirement** (issue #21) is load-bearing for the "all my sessions,
  everywhere" magic; document it and treat non-root/service-user deployment as an explicit
  trade-off.

## How this maps to Sourceful's thesis

`WALLET → SITE → DEVICE → DER` is the company's entity hierarchy. Miranda instantiates
the load-bearing half of it — `WALLET → DEVICE` as a passkey-derived, Solana-compatible
root of ownership — in a real, shipping product, off-chain and crypto-*enabled* rather
than crypto-*dependent*. It is a concrete, visible proof point for the broader Grid
Intelligence story: the same wallet-rooted identity model, exercised on terminals first.
