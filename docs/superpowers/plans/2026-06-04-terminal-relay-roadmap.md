# terminal-relay — Implementation Roadmap

> Spec: `docs/superpowers/specs/2026-06-04-terminal-relay-design.md`

The v1 vertical slice is built as **four sequential plans**. Each produces
working, testable software on its own and unblocks the next. The order de-risks
the two scary parts (cross-language E2E crypto, then WebRTC P2P) before anything
depends on them.

**Architecture reminder:** the data plane is **peer-to-peer** (WebRTC
DataChannel, strict P2P, STUN-only, no TURN). The public server only *signals*
(brokers SDP/ICE) — it never carries terminal data. Noise `KK` runs *inside* the
DataChannel so the untrusted signaling server cannot read or MITM the session.

| # | Plan | Goal | Depends on | Acceptance |
|---|------|------|-----------|------------|
| 1 | **Crypto core + interop** ✅ | `Noise_KK_25519_ChaChaPoly_SHA256` in Go + browser JS, frame codec, `prf`→owner key, certified by byte-for-byte vectors. | — | DONE — Go & JS reproduce identical Noise KK bytes; prf-derivation matches across languages. Merged to `main`. |
| 2 | **Signaling server + WebRTC spike** | `tr-signal`: WSS server that brokers SDP offer/answer + ICE between a browser and an agent by `{owner_id, machine_id}` — and **no terminal data**. Plus a headless proof that two `pion` peers establish a direct DataChannel *through* it (strict P2P, STUN-only) with the Plan-1 Noise channel running inside. | 1 | Two in-process pion peers (agent-role + browser-stand-in) signal through `tr-signal`, open a P2P DataChannel, run Noise KK inside it, and round-trip bytes; the server is shown to forward only SDP/ICE, never data. |
| 3 | **Agent (PTY + tmux + pairing)** | `tr-agent`: `enroll` (host key, QR/link pairing responder) and `up` (signaling WSS, pion DataChannel, Noise KK responder, `tmux new -A -s main` over a PTY). | 1, 2 | The Plan-2 browser-stand-in peer attaches to a real `tr-agent` through `tr-signal`, runs `echo hi`, sees `hi`; disconnect/reconnect resumes the same tmux session. |
| 4 | **Browser client** | The `term.srcfl.xyz` SPA: passkey + `prf` → owner key, `RTCPeerConnection` + Noise initiator inside the DataChannel, pairing UI (scan/open link), xterm.js render + resize, auto-reconnect. | 1, 2, 3 | Real iPhone Safari: create passkey once, scan a machine's QR to pair, attach to a live shell over direct P2P, lock phone / reconnect and resume. |

**Why this split:** Plan 1 (done) is pure crypto, no infra. Plan 2 stands up the
signaling server and de-risks WebRTC P2P entirely headlessly with `pion` on both
ends — no browser needed — proving the new transport before the agent or browser
depend on it. Plan 3 makes one end a real agent (PTY/tmux/pairing). Plan 4 is the
only plan that needs a real browser/passkey, so it comes last, when the WebRTC +
Noise + signaling path it drives is already proven.

**Plans 3–4 are written in full detail after Plan 2's spike validates the WebRTC
approach** — their internals (the exact pion/Noise glue, the signaling message
shapes the browser must speak) depend on what Plan 2 establishes. Writing them in
detail now would be speculative.

## Change log

- **2026-06-04:** Pivoted the data plane from a *tunneling relay* to *strict P2P
  WebRTC*. The public server is now signaling-only and never carries data. Plan 1
  is unaffected (Noise now runs inside the DataChannel instead of through a relay
  pipe). Plan 2 was rewritten from "blind byte-pipe relay" to "signaling server +
  WebRTC P2P spike."
