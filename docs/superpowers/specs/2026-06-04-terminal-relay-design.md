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

## Domains

- **PoC (now):** served under `srcfl.xyz` (already owned) — web client at
  `term.srcfl.xyz`, relay at `relay.srcfl.xyz`. WebAuthn RP ID = `srcfl.xyz`.
- **Future (production):** `passterm.io` (~$50/yr) once the PoC proves out — web
  client at `passterm.io`, relay at `relay.passterm.io`. Buy only after the PoC.
- **Consequence of moving domains:** the passkey Relying Party — and therefore the
  `prf` output — is scoped to the RP ID. Changing from `srcfl.xyz` to `passterm.io`
  changes the derived `owner_id`, so all passkeys must be re-registered and all
  agents re-paired. Fine for a throwaway PoC, but srcfl.xyz pairings do **not**
  migrate; treat that phase as disposable.

## Architecture

The data plane is **peer-to-peer**. The public server is a *signaling* server
only: it brokers the WebRTC offer/answer + ICE candidates that let the browser
and the agent find each other, then steps out of the way. Terminal bytes flow
directly browser ⇄ agent over a WebRTC DataChannel — **the server never carries a
single byte of terminal data.** Our Noise `KK` channel runs *inside* the
DataChannel, so even the peers' DTLS layer cannot be MITM'd via the signaling
server.

```
  iPhone / laptop          signaling server          target machine
  ┌───────────────┐  WSS  ┌──────────────────┐  WSS ┌────────────────────┐
  │ RTCPeerConn   │ ────▶ │ broker SDP + ICE  │ ◀─── │ tr-agent (pion)     │
  │ xterm + Noise │ ◀──── │ (NO terminal data)│ ───▶ │  ⤷ tmux/PTY + Noise │
  └──────┬────────┘       └──────────────────┘       └─────────┬──────────┘
         │                  agent dials OUT → NAT-safe         │
         └════ WebRTC DataChannel (DTLS), direct P2P ══════════┘
               ↑ hole-punched via STUN; Noise KK runs INSIDE it
```

**Strict P2P, no TURN.** ICE uses STUN (which only reveals your public IP:port —
it does not tunnel) to hole-punch a direct path. If no direct path can be found
(symmetric NAT / some CGNAT), the attach fails with a clear "can't reach directly
from this network" message rather than falling back to a relayed tunnel. This is
the deliberate purity choice: the server touches data **never**. (Honest
trade-off: in P2P each peer learns the other's IP address — that is how a direct
path works — and the signaling server sees ICE candidates. We trade
IP-anonymity-from-the-peer for content-secrecy-from-the-server plus a direct,
low-latency path.)

Three components, each with one clear job:

### 1. `tr-agent` (Go) — on the target machine

- **`tr-agent enroll`** — generates a static host keypair (X25519 for Noise,
  stored in `~/.terminal-relay/`), connects to the relay in pairing mode, and
  prints a pairing **link + ASCII QR** containing a one-time token (see Pairing).
- **`tr-agent up`** — dials an outbound WSS to the **signaling server**, registers
  `{owner_id, machine_id}`, and holds it (long-lived) to receive offers. On an
  incoming attach it negotiates a WebRTC DataChannel directly with the browser
  (via `github.com/pion/webrtc`), performs the Noise `KK` responder handshake
  *inside* that channel, then runs `tmux new -A -s main` behind a PTY and bridges
  `PTY ⇄ Noise ⇄ DataChannel`. The signaling WSS carries only SDP/ICE, never
  terminal data.
- **tmux is the persistence engine.** The session survives agent restarts and
  client disconnects. Reconnect is just `tmux attach` again; tmux redraws the
  current screen. (Hard dependency on `tmux` on the target machine — documented
  in the README; checked at `enroll` time with a clear error if missing.)

### 2. `tr-signal` (Go) — public signaling server (NOT a tunnel)

- Holds agents' outbound WSS connections. Routing table: `owner_id → {machine_id → conn}`.
- A browser opens a WSS, names `{owner_id, machine_id}`; the server **brokers the
  WebRTC handshake** — it relays the SDP offer/answer and ICE candidates between
  the two peers, and nothing else. Once the DataChannel is up P2P, its job for
  that session is done.
- **Learns only:** `owner_id` (opaque 32-byte pseudonym), `machine_id` (opaque),
  the SDP/ICE blobs (which include the peers' IP candidates — inherent to P2P),
  and liveness metadata. **Never** carries or sees terminal data — that path is
  the direct P2P DataChannel it is not part of.
- Deployed behind Cloudflare at `relay.srcfl.xyz`. Tiny and stateless per session;
  it can restart between sessions without affecting live DataChannels (they are
  already direct). Registration/notification patterns are lifted from `ftw-relay`.

### 3. Browser client (vanilla JS + xterm.js) — served from `term.srcfl.xyz`

- **WebAuthn** registers a passkey with the `prf` extension (Relying Party = the
  stable web origin). `prf` support is detected at registration; if absent, the
  flow is blocked with clear guidance (use a platform passkey or 1Password) so the
  user is never locked out.
- On login, the `prf` output is run through HKDF to deterministically derive the
  **owner keypair**. Because the passkey syncs via iCloud Keychain (or Google /
  1Password), the same owner keypair is reproduced on every device — this is the
  "automatically synced via passkeys" property.
- Opens a WSS to the signaling server, negotiates a WebRTC DataChannel directly
  with the agent (`RTCPeerConnection`), then runs the Noise `KK` initiator
  handshake (owner static key + the agent host key pinned during pairing) *inside*
  the DataChannel, and renders the PTY stream in xterm.js. Keystrokes → Noise →
  DataChannel; window resize → an E2E control frame.
- Crypto: `@noble/curves` + `@noble/ciphers` + `@noble/hashes`
  (X25519 / ChaCha20-Poly1305 / HKDF). Noise suite: `Noise_KK_25519_ChaChaPoly_SHA256`.

## Security model

The crux of the project. A terminal is the crown jewels (you type secrets, run
agents with API keys, read source), so terminal data goes **peer-to-peer** and
the signaling server is **untrusted** — it never carries data, and (per below) it
cannot MITM the connection it brokers.

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

**Why Noise is not redundant over WebRTC's DTLS.** The DataChannel is DTLS-
encrypted, but DTLS is authenticated only by certificate fingerprints exchanged
in the SDP — which travels through the untrusted signaling server. A malicious
server could swap fingerprints and MITM the DTLS. Noise `KK` with pinned static
keys closes that: the server cannot forge the static-key authentication, so it
cannot MITM the channel it brokers. Noise is the layer that actually authenticates
the peers; DTLS is just transport.

### What the signaling server learns

`owner_id`, `machine_id`, the SDP/ICE blobs (which expose the peers' candidate IP
addresses — inherent to establishing a direct P2P path), and liveness metadata.
It never carries or sees terminal data, never sees display names, and cannot
MITM the Noise channel. It could be operated by a third party without exposing
terminal content.

### Threat model (v1)

- **Compromised / malicious signaling server:** can deny service, learn metadata
  (including candidate IPs), or attempt to MITM the WebRTC handshake — but cannot
  read or inject terminal traffic. It carries no data (P2P), and Noise `KK` mutual
  auth defeats a DTLS fingerprint-swap MITM.
- **Network attacker / MITM:** defeated by Noise `KK` (pinned static keys) in
  steady state and by the one-time pairing token in the pairing exchange.
- **Phishing:** passkey is bound to the web origin (WebAuthn RP), phishing-resistant.
- **IP exposure (accepted):** peers learn each other's IP addresses, inherent to
  direct P2P. Accepted in exchange for the server never touching data.
- **Out of scope for v1:** a compromised target machine (the agent runs as you and
  has a real shell — that is the point); compromised iCloud Keychain (same trust
  the user already accepts for passkey roaming).

## Pairing — "simple is king"

Goal: browser learns + pins the agent host key, agent pins your `owner_id`, and
nothing can be MITM'd by the untrusted signaling server — in **one tap**, no code
typing, no fingerprint comparison.

```
  On the machine:                          On any device with your passkey:
  $ tr-agent enroll
  ┌───────────────────────────────┐       1. open the link (or scan the QR)
  │  Scan or open:                 │       2. Touch ID  (your passkey)
  │  https://term.srcfl.xyz/pair#a9…│      3. "Pair macbook?"  → Approve
  │  [ ASCII QR code ]             │
  └───────────────────────────────┘       ✓ paired
```

- The agent prints a link **and** a scannable ASCII QR containing a one-time,
  high-entropy token (128-bit, single-use, short TTL).
- The token lives in the **URL fragment** (`#…`), which is never sent to the
  signaling server — so the server stays out of it even during pairing.
- The token is the PSK that authenticates the pairing key-exchange in **both**
  directions. Because only the agent and your browser know it, MITM is closed
  without any human fingerprint comparison.
- Inside that authenticated exchange the browser receives + pins the agent host
  key, and the agent receives + pins your `owner_id`. This also solves the
  "fresh device needs the agent host key pinned for `KK`" problem — pairing is
  exactly where that pin happens.

First-time friction, total: create a passkey on `term.srcfl.xyz` **once, ever**
(Touch ID); then per machine: run the agent, scan, Touch ID. After that, attaching
is just "open `term.srcfl.xyz` and it's there."

## Attach data flow

```
SIGNALING (via server, SDP/ICE only — no terminal data):
  browser → server:  open {owner_id, machine_id}
  server  → agent:   incoming attach notification
  browser ⇄ agent:   exchange SDP offer/answer + ICE candidates (STUN hole-punch)

P2P (direct DataChannel, server no longer involved):
  DataChannel opens browser ⇄ agent (DTLS)
  browser ⇄ agent:   Noise KK handshake  (inside the DataChannel)
  agent:             tmux new -A -s main  →  PTY
  loop:  keystrokes → Noise.encrypt → DataChannel → agent → decrypt → PTY stdin
         PTY stdout → Noise.encrypt → agent → DataChannel → browser → decrypt → xterm
  resize: xterm → E2E control frame → agent → SIGWINCH / tmux resize
  disconnect: DataChannel drops → tmux persists → reconnect re-signals + re-attaches
  no direct path found (strict P2P): fail with a clear "can't reach directly" error
```

### Frame protocol (inside the Noise-encrypted channel)

A minimal typed framing carries: `DATA` (raw PTY bytes, both directions),
`RESIZE` (cols, rows), and a `HELLO` (agent → browser, carries the machine
display name and shell info). Everything except the SDP/ICE signaling envelope is
inside the Noise transport on the P2P DataChannel.

## Error handling

- **`prf` unsupported** → detected at passkey registration; block with guidance.
- **Agent offline** → signaling server returns "machine offline"; browser shows status, retries with backoff.
- **No direct P2P path** (strict P2P, ICE fails) → clear "can't reach directly from this network" error; no tunnel fallback.
- **DataChannel drop** → browser re-signals and re-attaches with backoff; tmux preserves state; terminal redraws on reattach.
- **Handshake failure** (wrong key / MITM) → abort with a fingerprint/identity warning. Tripwire, not a silent retry.
- **Signaling server restart** → agents re-register with backoff; browsers reconnect; live DataChannels are unaffected (already P2P).
- **tmux missing on target** → caught at `enroll` with an actionable message.

## Testing strategy

Designed for AI-effective feedback loops (change → run → see result → adjust).

1. **De-risk #1 — crypto interop (done in Plan 1):** JS ↔ Go Noise/`prf` interop,
   proven by deterministic byte-for-byte vectors.
2. **De-risk #2 — WebRTC P2P:** prove a pion↔pion DataChannel can be established
   *through* the signaling server (strict P2P, STUN-only, no TURN) and that Noise
   runs inside it — all headless, before any browser exists.
3. **Go unit tests:** Noise `KK` wrapper, frame codec, signaling routing table.
4. **In-process integration test:** signaling server + two pion peers (one as
   agent, one as browser-stand-in) → establish DataChannel, Noise handshake,
   write bytes to a fake shell, assert echo.
5. **Manual acceptance:** real iPhone Safari attach to a real shell, including
   disconnect/reconnect resuming the tmux session.

## Tech stack & repo layout

- **Go** for `tr-agent` and `tr-signal` (matches forty-two-watts; reuse patterns).
  `flynn/noise` for the crypto, `github.com/pion/webrtc` for the P2P DataChannel,
  `coder/websocket` for the signaling channel, `creack/pty` for the PTY, a QR lib
  (e.g. `qrterminal`) for pairing.
- **Browser:** vanilla JS (matches the existing `web/` style), native
  `RTCPeerConnection` + `RTCDataChannel`, `xterm.js`, `@noble/curves` +
  `@noble/ciphers` + `@noble/hashes`, the WebAuthn API for passkey + `prf`.
- **tmux** on the target machine (documented dependency).
- **STUN** for hole-punching (public STUN or self-hosted); **no TURN** (strict P2P).

```
terminal-relay/
  go/
    cmd/tr-agent/
    cmd/tr-signal/
    internal/noise/      # KK wrapper + framing (Plan 1, done)
    internal/identity/   # prf -> owner key (Plan 1, done)
    internal/signal/     # signaling routing + WebRTC offer/answer broker
    internal/peer/       # pion DataChannel + Noise glue (shared by agent + tests)
    internal/pty/        # tmux attach + PTY bridge
  web/                   # RTCPeerConnection client, xterm.js, WebAuthn, Noise-in-JS
  docs/superpowers/specs/
  README.md
  CLAUDE.md
```

## Suggested v1 milestones

1. **Crypto interop (Plan 1 — DONE):** JS ↔ Go Noise `KK` + `prf` key, byte-vectors.
2. **Signaling + WebRTC spike (Plan 2):** `tr-signal` brokers SDP/ICE; prove a
   pion↔pion DataChannel through it (strict P2P, STUN-only) with Noise inside.
3. **Agent (Plan 3):** pion peer + `KK` responder + PTY + `tmux new -A -s main`,
   driven by signaling; pairing responder (token link + ASCII QR).
4. **Browser (Plan 4):** passkey + `prf` → owner key; `RTCPeerConnection` + Noise
   initiator inside the DataChannel; xterm.js render + resize; pairing UI; reconnect.
5. **Acceptance:** iPhone Safari attach, disconnect/reconnect resumes the session.

## Open risks

- `prf` extension maturity across authenticators (mitigated by detection + guidance).
- **WebRTC browser↔pion interop** (new highest risk; mitigated by the Plan 2 spike
  headlessly, then the real browser path in Plan 4).
- **Strict P2P reachability** — some NATs/CGNAT will not hole-punch; accepted for
  v1 with a clear error (TURN deliberately excluded).
- tmux as a hard dependency (accepted for v1; documented).
