# Web Client Resilience — Auto-Reconnect + Error UX (A2 hardening)

**Status:** Approved design (2026-06-10). Sub-project of A2 (browser client), north-star Core track.
**Builds on:** the connect-race fix (PR #26) and the 2026-06-10 live browser smoke that exposed these gaps.

> **Stance (from the brainstorm):** ship a focused, correct resilience layer — *"the best
> feedback we'll get is from users later."* Keep it scoped; let real usage drive further
> polish. YAGNI on anything beyond reconnect + a clear status/error surface.

---

## Problem

The browser `attach()` establishes one P2P session and never recovers it. Verified in the
live smoke + `web/src/app.js`:

- **No reconnect.** When the DataChannel/PeerConnection drops mid-session, the recv loop
  exits silently (`try { ct = await recv(); } catch { return; }`); the terminal freezes
  with no notice. The agent (Go) tears down its side on peer loss, but **tmux keeps the
  session server-side**, so a fresh attach lands in the exact same shell — we just never
  re-attach.
- **Terse errors.** Failures surface as `connect failed: <msg>` / `pairing failed: <msg>`
  in a status div, or only in `window.__diag` / the console. No persistent at-a-glance
  connection state.

## Goals

1. **Seamless auto-reconnect** on a dropped session: reuse the same xterm (scrollback
   preserved), reconnect with backoff, land back in the same tmux session.
2. **A clear status/error surface:** a small topbar status pill (connected / reconnecting /
   failed→retry) plus in-terminal `[mir] …` transition lines; actionable pairing/attach
   error messages with a retry affordance.

## Non-goals (now)

- Offline queueing of keystrokes while disconnected (tmux + redraw on reconnect is enough).
- Reconnecting the *signalling* WebSocket for its own sake — it is per-attempt and only used
  to broker one offer/answer; each reconnect makes a fresh one.
- Multi-machine reconnect policy tuning, telemetry, or a debug panel. Lean on user feedback.

---

## Design

### Architecture — durable terminal, retryable connection

Split today's monolithic `attach()` into a durable half and a retryable half:

- **`viewTerminal` (durable, built once):** the xterm, a topbar **status pill**, the resize
  listeners, and a mutable `current` ref pointing at the live session's `send`. It starts
  `runSession()` and tears everything down on close.
- **`connectOnce(machine, term, { onConnected })` (one P2P session):** establishes ws + pc +
  dc + Noise `KK`, wires the recv loop and `term.onData/onResize → current.send`, calls
  `onConnected()` once the DataChannel + Noise are live, and returns a promise that
  **resolves when the session ends** (DC/PC `disconnected|failed|closed`) or **rejects on a
  setup error** (no P2P path, signal error, handshake failure). Returns a `teardown()`.
- **`runSession({ connectOnce, onState, isVisible, waitVisible, sleep, maxSetupAttempts })`
  (reconnect loop, DOM-free):** the state machine that drives reconnection.

`term.onData`/`onResize` are bound **once** (in `viewTerminal`) and route through the mutable
`current` ref, so keystrokes always reach the live channel and survive reconnects without
rebinding handlers.

This mirrors the agent's Go side (`serveOwner` reconnect-with-backoff) — same shape on both
ends of the wire.

### State machine (`runSession`)

```
states: connecting | connected | reconnecting | failed
attempt = 0; everConnected = false

loop (until stopped):
  if !isVisible(): await waitVisible()                  // don't spin on a backgrounded tab
  onState(everConnected ? 'reconnecting' : 'connecting', attempt)
  try:
    await connectOnce(machine, term, { onConnected: () => {
      everConnected = true; attempt = 0; onState('connected', 0)
    }})
    // resolved => the session ENDED (a drop). Fall through to backoff + retry.
  catch (e):
    if (!everConnected && ++attempt >= maxSetupAttempts):
      onState('failed', attempt)                        // show "couldn't connect ⊘ [retry]"
      await retrySignal()                               // manual retry resets attempt + everConnected
      continue
  await sleep(backoff(attempt))
  if everConnected: attempt = Math.min(attempt + 1, 8)   // flapping grows backoff; capped so delay + displayed count stay bounded
```

- **Transient drop** (was connected → lost): `attempt` was reset to 0 on connect, so the
  first reconnect is prompt; repeated flapping grows the backoff. Retries indefinitely while
  visible (the user can close or it reconnects).
- **Never-connected setup failure:** backoff grows; after `maxSetupAttempts` show `failed`
  and stop auto-retrying until the user taps the pill (manual `retryNow()` resets state).

Controller returns `{ stop(), retryNow() }`. `stop()` is called on `viewTerminal` close /
machine switch.

### Backoff (`web/src/net/backoff.js`)

Pure, injectable randomness for deterministic tests:

```js
// full-jitter exponential backoff, capped.
export function backoff(attempt, { base = 500, factor = 2, cap = 10000, random = Math.random } = {}) {
  const ceil = Math.min(cap, base * factor ** attempt);
  return Math.round(random() * ceil);            // 0..ceil (full jitter)
}
```

Defaults: base 500 ms, ×2, cap 10 s. `maxSetupAttempts` = 6 (≈25 s of trying before
"couldn't connect").

### Status surface

- **Topbar pill** in `viewTerminal`'s topbar (next to the title / switch button):
  `● connected` · `⟳ reconnecting (n)` / `⟳ connecting (n)` · `⊘ couldn't connect` (tap →
  `retryNow()`). Colour/opacity convey state; tap target is the whole pill when actionable.
- **In-terminal lines** on transitions: `term.write('\r\n[mir] connection lost — reconnecting…\r\n')`,
  `\r\n[mir] reconnected\r\n`, `\r\n[mir] couldn't reconnect — tap ⟳ to retry\r\n`. These are
  cosmetic; tmux redraws the real screen on reconnect.
- **Pairing/attach errors** (`viewPair`, initial connect): replace terse text with a clear
  message + a **retry button** (already partially present in `viewPair`; make consistent).

### Data flow (unchanged per session)

keystroke → `term.onData` → `current.send` → Noise encrypt → DataChannel → agent → PTY.
Drop detected via `pc.onconnectionstatechange` / `dc.onclose` → `connectOnce` resolves →
`runSession` backs off → reconnects → new Noise session → **tmux redraws the same session** →
pill `●`.

---

## Components / files

| File | Change | Responsibility |
|---|---|---|
| `web/src/net/backoff.js` | create | pure full-jitter exponential backoff |
| `web/src/net/reconnect.js` | create | `runSession(...)` loop/state machine, DOM-free |
| `web/src/app.js` | modify | extract `connectOnce` from `attach()`; `viewTerminal` builds durable xterm + status pill + `current` ref + runs `runSession`; consistent error+retry UI |
| `web/test/backoff.test.js` | create | delays grow, respect cap, jitter bounds (injected random) |
| `web/test/reconnect.test.js` | create | retry-on-drop, backoff sequence, pause-when-hidden, stop-after-N-setup-failures, manual-retry resets — with fake connectOnce + clock + visibility |

`connectOnce` keeps the WebRTC/DOM specifics (verified by the live smoke); the *policy*
(`backoff`, `runSession`) is DOM-free and unit-tested. `awaitSocketOpen` (PR #26) stays.

## Testing

- **Unit:** `backoff` (sequence, cap, jitter within `[0, ceil]`), `reconnect` (the five
  behaviours above) with injected `sleep`/`isVisible`/`waitVisible`/`random` + a fake
  `connectOnce` that can resolve (drop), reject (setup fail), or call `onConnected`.
- **Live smoke** (the harness from the 2026-06-10 verification): attach in the browser, then
  `kill` the `mir up` agent mid-session → expect pill `⟳ reconnecting` + `[mir] connection
  lost…`; restart the agent → expect auto-reconnect, pill `●`, and the **same tmux session**
  redrawn. Then kill the relay too → setup failures → `⊘ couldn't connect` after N → tap →
  reconnects once the relay is back.
- **Regression:** `cd web && npm test` stays green (now incl. backoff + reconnect suites).

---

## Decisions (resolved in the brainstorm)

1. **Reconnect model → seamless auto-reconnect, reuse the xterm** (tmux resumes the session).
2. **Status surface → topbar pill + in-terminal `[mir] …` lines** (in-context + at-a-glance);
   pairing/attach errors get a clear message + retry.
3. **Backoff policy → full-jitter exponential** (500 ms ×2, cap 10 s), retry while visible,
   pause when hidden, manual retry always available; `maxSetupAttempts` = 6 before `failed`.

## Risks / open questions

- **Transient vs terminal error classification:** v1 treats all `connectOnce` rejections the
  same (retry up to N). A wrong-keys handshake failure would retry pointlessly until `failed`;
  acceptable for v1 (rare; surfaced after ~25 s). Revisit if users hit it.
- **iOS Safari backgrounding** suspends timers; `waitVisible` resumes on `visibilitychange`,
  but a long sleep may be killed by the OS — reconnect simply fires on the next foreground.
- **Flapping networks:** capped backoff + jitter prevents a tight loop; the pill shows the
  attempt count so the state is legible.
