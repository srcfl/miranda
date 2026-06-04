# terminal-relay — Implementation Roadmap

> Spec: `docs/superpowers/specs/2026-06-04-terminal-relay-design.md`

The v1 vertical slice is built as **four sequential plans**. Each produces
working, testable software on its own and unblocks the next. The order is chosen
to de-risk the scariest part (cross-language E2E crypto) before anything depends
on it.

| # | Plan | Goal | Depends on | Acceptance |
|---|------|------|-----------|------------|
| 1 | **Crypto core + interop** | A proven `Noise_KK_25519_ChaChaPoly_SHA256` channel in **both** Go and browser JS, a typed frame codec in both, and the `prf`→owner-key derivation — all certified by a cross-language interop test. | — | Go and JS reproduce identical Noise KK wire bytes from fixed vectors and decrypt each other; prf-derivation matches across languages. |
| 2 | **Relay (blind rendezvous)** | `tr-relay`: WSS server that registers agents by `{owner_id, machine_id}` and bridges a browser stream to the right agent, piping opaque frames. Knows nothing about content. | 1 (frame envelope only) | Two in-process WSS dummies (one "agent", one "browser") attach by id and exchange opaque bytes through the relay; relay logs show no plaintext. |
| 3 | **Agent (PTY + tmux + pairing)** | `tr-agent`: `enroll` (host key, QR/link pairing responder) and `up` (outbound WSS, Noise KK responder, `tmux new -A -s main` over a PTY, frame bridge). | 1, 2 | A Go "browser" (Noise KK initiator) attaches through the relay, runs `echo hi`, sees `hi`; disconnect/reconnect resumes the same tmux session. |
| 4 | **Browser client** | The `term.srcfl.xyz` SPA: passkey + `prf` → owner key, KK initiator (JS module from Plan 1), pairing UI (scan/open link), xterm.js render + resize, auto-reconnect. | 1, 2, 3 | Real iPhone Safari: create passkey once, scan a machine's QR to pair, attach to a live shell, lock phone / reconnect and resume. |

**Why this split:** Plan 1 is pure crypto with zero infrastructure — fast feedback,
no servers. Plan 2 is pure routing with zero crypto (the relay is blind by
design) — testable with dummies. Plan 3 brings them together server-side and is
fully testable headless (Go initiator) before any browser exists. Plan 4 is the
only plan that needs a real browser/passkey, so it comes last when everything it
calls is already proven.

**Plans 2–4 will be written in full detail after Plan 1's spike validates the
crypto approach** — their internals (especially the JS Noise module's exact
shape) depend on what Plan 1 establishes. Writing them in detail now would be
speculative.
