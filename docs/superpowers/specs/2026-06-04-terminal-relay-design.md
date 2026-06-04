# terminal-relay — Design (v1)

**Date:** 2026-06-04
**Status:** Approved design, pending implementation plan
**Scope:** v1 — smallest vertical slice

## One-line

A terminal you reach from any browser (including iPhone Safari) by authenticating
with a passkey — like SSH, without thinking SSH. The session is persistent, the
relay is blind (end-to-end encrypted), and your passkey is the only thing you carry.

## Goal (v1)

From a browser, authenticate with a passkey and attach to **one persistent
terminal** on **one machine**, with the stream **end-to-end encrypted** so the
relay can never read keystrokes or output. Disconnect and reconnect resume the
exact same session (scrollback intact). Long-running processes (e.g. Claude Code,
Codex) survive the browser sleeping or the network dropping.

## Non-goals (explicitly out of v1 — YAGNI)

- Multi-machine selector in the browser
- Multiple named terminals per machine
- "Kiosk" terminals that auto-launch a command (e.g. `claude`)
- Sharing a terminal with another person
- File transfer / upload
- Session-list UI

All of the above are future work, each its own spec → plan cycle. v1 proves the
hard vertical (passkey + prf + Noise + blind relay + tmux) on the narrowest surface.

## Relationship to forty-two-watts

Standalone project with its **own relay**. It borrows patterns and code freely from
`forty-two-watts` (`ftw-relay` outbound-registration/routing, `ftw-pair`
long-poll loop, idgen, config-dir conventions, the `owner-access` WebAuthn work)
but keeps an independent security boundary. A general-purpose remote terminal is
more sensitive than energy telemetry and should not be coupled to that product's
codebase or release cycle.

## Architecture

```
  iPhone / laptop              relay (public)            target machine
  ┌───────────────┐   WSS  ┌──────────────────┐  WSS  ┌────────────────────┐
  │ xterm.js      │ ─────▶ │  rendezvous +     │ ◀──── │ tr-agent            │
  │ passkey-login │ ◀───── │  blind byte-pipe, │ ────▶ │  ⤷ tmux/PTY (shell) │
  └───────────────┘        │  route per owner  │       └────────────────────┘
        ▲                  └──────────────────┘         dials OUT → NAT-safe
        └─ passkey synced via iCloud Keychain → present on all your devices
```

Three components, each with one clear job:

### 1. `tr-agent` (Go) — on the target machine

- **`tr-agent enroll`** — generates a static host keypair (X25519 for Noise,
  stored in `~/.terminal-relay/`), connects to the relay in pairing mode, and
  prints a pairing **link + ASCII QR** containing a one-time token (see Pairing).
- **`tr-agent up`** — dials an outbound WSS to the relay, registers
  `{owner_id, machine_id}`, and holds the connection (long-lived). On an incoming
  attach it performs the Noise `KK` responder handshake, then runs
  `tmux new -A -s main` behind a PTY and bridges `PTY ⇄ Noise ⇄ WSS`.
- **tmux is the persistence engine.** The session survives agent restarts and
  client disconnects. Reconnect is just `tmux attach` again; tmux redraws the
  current screen. (Hard dependency on `tmux` on the target machine — documented
  in the README; checked at `enroll` time with a clear error if missing.)

### 2. `tr-relay` (Go) — public rendezvous, blind pipe

- Holds agents' outbound WSS connections. Routing table: `owner_id → {machine_id → conn}`.
- A browser opens a WSS, names `{owner_id, machine_id}`; the relay bridges a
  stream to that agent and pipes opaque frames both ways.
- **Learns only:** `owner_id` (opaque 32-byte pseudonym, not a human name),
  `machine_id` (opaque), and unavoidable metadata (liveness, frame timing/size).
  **Never** learns: keystrokes, output, machine display names, or what runs.
- Deployed behind Cloudflare on a stable domain. Transport is WSS (not the
  long-poll request/response model from `ftw-relay`, which is wrong for
  interactive low-latency I/O). Routing/registration patterns are lifted from
  `ftw-relay`.

### 3. Browser client (vanilla JS + xterm.js) — served from `term.<domain>`

- **WebAuthn** registers a passkey with the `prf` extension (Relying Party = the
  stable web origin). `prf` support is detected at registration; if absent, the
  flow is blocked with clear guidance (use a platform passkey or 1Password) so the
  user is never locked out.
- On login, the `prf` output is run through HKDF to deterministically derive the
  **owner keypair**. Because the passkey syncs via iCloud Keychain (or Google /
  1Password), the same owner keypair is reproduced on every device — this is the
  "automatically synced via passkeys" property.
- Opens a WSS to the relay, runs the Noise `KK` initiator handshake (owner static
  key + the agent host key pinned during pairing), then renders the PTY stream in
  xterm.js. Keystrokes → Noise → WSS; window resize → an E2E control frame.
- Crypto: `@noble/curves` + `@noble/ciphers` + `@noble/hashes`
  (X25519 / ChaCha20-Poly1305 / HKDF). Noise suite: `Noise_KK_25519_ChaChaPoly_SHA256`.

## Security model

The crux of the project. A terminal is the crown jewels (you type secrets, run
agents with API keys, read source), so the relay must be **blind**.

### Identity derivation

- **Owner identity = derived from passkey `prf`.** `prf` secret → HKDF → owner
  keypair (X25519). The **public** key is your stable `owner_id`: it is pinned on
  agents at pairing and used as the relay routing namespace. The **private** key
  never leaves the device — it is re-derived from the passkey on each device. This
  is why your terminal "just exists" on every device without per-device setup.
- **Agent identity = static host keypair** (X25519), generated once at `enroll`,
  pinned by your browser at pairing (trust-on-first-use, like an SSH host key).

### Steady-state handshake: Noise `KK`

Both parties already hold each other's static public key (you pinned the agent's
at pairing; the agent pinned your `owner_id` at pairing). `Noise_KK` gives mutual
authentication + forward secrecy with no reliance on the `prf` secret as a
transported PSK — `prf`'s only job is to regenerate the owner private key per
device. A handshake failure (wrong key / MITM) aborts loudly; this is the
security tripwire.

### What the relay learns

`owner_id`, `machine_id`, and connection metadata (timing, sizes, liveness).
Never plaintext, never display names. The relay is a dumb, opaque router — it
could in principle be operated by a third party without exposing terminal content.

### Threat model (v1)

- **Compromised / malicious relay:** can deny service or learn metadata, but
  cannot read or inject terminal traffic (E2E + Noise mutual auth).
- **Network attacker / MITM:** defeated by Noise `KK` (pinned static keys) in
  steady state and by the one-time pairing token in the pairing exchange.
- **Phishing:** passkey is bound to the web origin (WebAuthn RP), phishing-resistant.
- **Out of scope for v1:** a compromised target machine (the agent runs as you and
  has a real shell — that is the point); compromised iCloud Keychain (same trust
  the user already accepts for passkey roaming).

## Pairing — "simple is king"

Goal: browser learns + pins the agent host key, agent pins your `owner_id`, and
nothing can be MITM'd by the blind relay — in **one tap**, no code typing, no
fingerprint comparison.

```
  On the machine:                       On any device with your passkey:
  $ tr-agent enroll
  ┌─────────────────────────┐          1. open the link (or scan the QR)
  │  Scan or open:           │          2. Touch ID  (your passkey)
  │  https://term.x/pair#a9F…│   ───▶   3. "Pair macbook?"  → Approve
  │  [ ASCII QR code ]       │
  └─────────────────────────┘          ✓ paired
```

- The agent prints a link **and** a scannable ASCII QR containing a one-time,
  high-entropy token (128-bit, single-use, short TTL).
- The token lives in the **URL fragment** (`#…`), which is never sent to the relay
  server — so the relay stays blind even during pairing.
- The token is the PSK that authenticates the pairing key-exchange in **both**
  directions. Because only the agent and your browser know it, MITM is closed
  without any human fingerprint comparison.
- Inside that authenticated exchange the browser receives + pins the agent host
  key, and the agent receives + pins your `owner_id`. This also solves the
  "fresh device needs the agent host key pinned for `KK`" problem — pairing is
  exactly where that pin happens.

First-time friction, total: create a passkey on `term.x` **once, ever** (Touch
ID); then per machine: run the agent, scan, Touch ID. After that, attaching is
just "open `term.x` and it's there."

## Attach data flow

```
browser → relay:  open {owner_id, machine_id}
relay   → agent:  incoming attach (bridged stream)
browser ⇄ agent:  Noise KK handshake          (relay sees ciphertext only)
agent:            tmux new -A -s main  →  PTY
loop:  keystrokes → encrypt → WSS → relay → agent → decrypt → PTY stdin
       PTY stdout → encrypt → agent → relay → browser → decrypt → xterm.write
resize: xterm → E2E control frame → agent → SIGWINCH / tmux resize
disconnect: WSS drops → tmux session persists → reconnect re-attaches, tmux redraws
```

### Frame protocol (inside the Noise-encrypted channel)

A minimal typed framing carries: `DATA` (raw PTY bytes, both directions),
`RESIZE` (cols, rows), and a `HELLO` (agent → browser, carries the machine
display name and shell info so the relay never sees them). Everything except the
WSS/relay routing envelope is inside the Noise transport.

## Error handling

- **`prf` unsupported** → detected at passkey registration; block with guidance.
- **Agent offline** → relay returns "machine offline"; browser shows status, retries with backoff.
- **WSS drop** → browser auto-reconnects with backoff; tmux preserves state; terminal redraws on reattach.
- **Handshake failure** (wrong key / MITM) → abort with a fingerprint/identity warning. Tripwire, not a silent retry.
- **Relay restart** → agents re-register with backoff; browsers reconnect.
- **tmux missing on target** → caught at `enroll` with an actionable message.

## Testing strategy

Designed for AI-effective feedback loops (change → run → see result → adjust).

1. **De-risk the highest-risk piece first: JS ↔ Go Noise/`prf` interop.** A spike
   that proves the browser Noise `KK` implementation and the Go `flynn/noise`
   implementation interoperate with `prf`-derived keys. Everything rests on this;
   build it before anything else.
2. **Go unit tests:** Noise `KK` wrapper, frame codec, relay routing table.
3. **In-process integration test:** relay + agent in-process plus a Go "browser"
   (Noise initiator) → attach, write bytes to a fake shell, assert echo. Proves
   the crypto + routing vertical without a browser.
4. **Manual acceptance:** real iPhone Safari attach to a real shell, including
   disconnect/reconnect resuming the tmux session.

## Tech stack & repo layout

- **Go** for `tr-agent` and `tr-relay` (matches forty-two-watts; reuse patterns).
  `flynn/noise` on the Go side. `creack/pty` (or equivalent) for PTY. A terminal
  QR lib (e.g. `qrterminal`) for the pairing QR.
- **Browser:** vanilla JS (matches the existing `web/` style), `xterm.js`,
  `@noble/curves` + `@noble/ciphers` + `@noble/hashes`, the WebAuthn API for
  passkey + `prf`.
- **tmux** on the target machine (documented dependency).

```
terminal-relay/
  go/
    cmd/tr-agent/
    cmd/tr-relay/
    internal/noise/      # KK wrapper + framing
    internal/relay/      # routing
    internal/pty/        # tmux attach + PTY bridge
  web/                   # xterm.js client, WebAuthn, Noise-in-JS
  docs/superpowers/specs/
  README.md
  CLAUDE.md
```

## Suggested v1 milestones

1. **Spike:** JS ↔ Go Noise `KK` + `prf`-derived key interop (de-risk).
2. **Relay:** WSS rendezvous + blind routing table + agent registration.
3. **Agent:** outbound WSS, `KK` responder, PTY + `tmux new -A -s main` bridge.
4. **Pairing:** one-time-token link + ASCII QR; browser pin of host key, agent pin of `owner_id`.
5. **Browser:** passkey + `prf` → owner key; `KK` initiator; xterm.js render + resize; auto-reconnect.
6. **Acceptance:** iPhone Safari attach, disconnect/reconnect resumes the session.

## Open risks

- `prf` extension maturity across authenticators (mitigated by detection + guidance).
- JS Noise `KK` correctness/interop (mitigated by the milestone-1 spike).
- tmux as a hard dependency (accepted for v1; documented).
