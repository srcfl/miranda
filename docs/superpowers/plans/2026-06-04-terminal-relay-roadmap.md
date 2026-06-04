# terminal-relay — Implementation Roadmap

> Spec: `docs/superpowers/specs/2026-06-04-terminal-relay-design.md`

**The product:** not just "remote terminal from a browser" — a **cross-machine
multiplexer**: one unified terminal workspace spanning all your machines (MacBook
+ office Mac mini + Linux box), reached by passkey, end-to-end encrypted, strictly
peer-to-peer. "Like tmux, but the actual terminals run on different machines."

**Two-level model (additive; the foundation does not change):**
1. **Per machine = tmux** — each machine runs `tr-agent` with its own `main` tmux
   session: persistence + windows/panes within that host, multi-attach so local +
   phone + CLI all see the same live session.
2. **Across machines = the `tr` CLI client** — a lightweight switcher holding
   several P2P sessions at once, one machine in focus, hop between them. No
   tmux-in-tmux: tmux owns *a machine's* windows; `tr` owns *which machine*.

**Data plane:** peer-to-peer (WebRTC DataChannel, strict P2P, STUN-only, no TURN).
The public server only *signals*; it never carries terminal data. Noise `KK` runs
inside the DataChannel so the untrusted signaling server cannot read or MITM.

| # | Plan | Goal | Status | Acceptance |
|---|------|------|--------|------------|
| 1 | **Crypto core + interop** | `Noise_KK` in Go + JS, frame codec, `prf`→owner key, byte-vector certified. | ✅ merged | Go & JS reproduce identical Noise bytes; prf matches. |
| 2 | **Signaling + WebRTC P2P** | `tr-signal` (SDP broker, no data) + pion↔pion DataChannel through it, Noise inside, strict P2P. | ✅ merged | Two pion peers signal through `tr-signal`, open a P2P DataChannel, Noise round-trips. |
| 3 | **Agent (real shell over P2P)** | `tr-agent`: keystore, PTY+tmux, frame bridge, runtime; local `make dev`. | ✅ merged | Browser-stand-in → `tr-signal` → **real `sh` over P2P**, hermetic E2E green. |
| 4 | **`tr` CLI client** | `tr attach <machine>`: derive owner key, signal, P2P, Noise initiator, bridge the DataChannel ⇄ the local terminal in raw mode → then the **multi-machine focus-switcher** (hold N sessions, hotkey/picker to switch). Reuses the Go peer entirely (no browser deps). | next | From a real terminal, `tr attach` a local `tr-agent` and drive a live shell over P2P; switch between ≥2 machines. |
| 5 | **Browser client + QR pairing** | The `term.srcfl.xyz` SPA: passkey + `prf`, `RTCPeerConnection` + Noise-JS + xterm; plus the interactive **token/QR pairing** (NNpsk0) co-developed here (the browser is pairing's other end). | later | Real iPhone Safari: passkey once, scan a QR to pair, attach to a live shell over P2P. |

**Cross-cutting (folded into Plans 4–5):**
- **Docker/OrbStack network-sim harness** — coturn (STUN-only) + agents in separate
  NAT'd networks to exercise real hole-punching, multiple machines, multiple
  clients, and the strict-P2P failure path — all locally before deploy.
- **Pairing hardening** — the signaling server's answer-ownership check; per-device
  vs prf key model finalization.
- **iPhone Safari** — the one path needing HTTPS (deploy / TLS tunnel) = last mile.

**Why CLI before browser:** the CLI client reuses 100% of the Go peer (offerer +
Noise initiator + DataChannel) already built and needs no WebAuthn/prf/xterm — so
it is the fastest path to a *real, usable, locally-testable* terminal, and it is
the native multiplexer the product is really about.

## Change log

- **2026-06-04 (pivot):** data plane → strict P2P WebRTC; the public server is
  signaling-only (Plan 2 rewritten from a tunneling relay).
- **2026-06-04 (vision):** the client is a **cross-machine multiplexer** (`tr` CLI,
  "tmux where terminals run on different machines", focus-switch). CLI-first
  (Plan 4) before the browser (Plan 5); QR pairing moves to Plan 5.
