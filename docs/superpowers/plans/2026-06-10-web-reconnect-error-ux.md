# Web Client Resilience (Auto-Reconnect + Error UX) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the browser terminal survive a dropped P2P session — auto-reconnect (reusing the same xterm, landing back in the same tmux session) with a clear status pill + in-terminal notices.

**Architecture:** Split today's monolithic `attach()` into a *durable* half (xterm + topbar status pill + a mutable `current.send` ref, owned by `viewTerminal`) and a *retryable* half (`connectOnce`, one ws+pc+dc+Noise session that resolves when it drops). A DOM-free `runSession` loop calls `connectOnce` with full-jitter backoff, pausing while the page is hidden. Pure policy (`backoff`, `runSession`) is unit-tested; the WebRTC/DOM glue is verified by a live browser smoke.

**Tech Stack:** vanilla ES modules, `node --test` (the existing `web/test` harness), xterm.js, WebRTC, the existing `internal/*` Noise/frame JS. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-06-10-web-reconnect-error-ux-design.md`

---

## Conventions

- **All web commands run from `web/`:** `cd /Users/fredde/repositories/miranda/web && npm test`.
- Tasks 1–2 are pure logic → full TDD (RED → GREEN → commit). Tasks 3–4 restructure DOM/WebRTC
  code that is not import-safe under node (xterm + `/vendor/jsqr.js` imports), so they are
  **behavior-preserving refactors gated by the live smoke** described in Task 6 — not unit tests.
- Branch (created at execution time via using-git-worktrees): `web-reconnect`.
- The live-smoke harness is the one from the 2026-06-10 verification: `mir-signal --addr :8765
  --webroot web` + `mir pair`/`mir up --dir /tmp/mir-smoke --shell sh` + drive the SPA in a
  browser via the `window.__*` hooks. Pick a free port if :8765 is taken.

---

## File Structure

| File | Responsibility |
|---|---|
| `web/src/net/backoff.js` (create) | pure full-jitter exponential backoff |
| `web/src/net/reconnect.js` (create) | `runSession(...)` reconnect loop/state machine, DOM-free |
| `web/test/backoff.test.js` (create) | backoff sequence, cap, jitter bounds |
| `web/test/reconnect.test.js` (create) | reconnect-on-drop, backoff growth, pause-when-hidden, give-up, manual retry |
| `web/src/app.js` (modify) | extract `connectOnce`; `viewTerminal` owns the durable xterm + pill + `current` ref + runs `runSession`; consistent connect-error + retry UI |

---

## Task 1: `backoff.js` — pure full-jitter backoff

**Files:**
- Create: `web/src/net/backoff.js`
- Test: `web/test/backoff.test.js`

- [ ] **Step 1: Write the failing test**

Create `web/test/backoff.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert';
import { backoff } from '../src/net/backoff.js';

test('full jitter stays within [0, ceil] and ceil grows then caps', () => {
  // random=1 returns the ceiling: base*factor**attempt, capped at cap.
  const max = (n) => backoff(n, { base: 500, factor: 2, cap: 10000, random: () => 1 });
  assert.equal(max(0), 500);
  assert.equal(max(1), 1000);
  assert.equal(max(2), 2000);
  assert.equal(max(5), 10000); // 500*32=16000 -> capped
  assert.equal(max(9), 10000); // stays capped (no overflow)
});

test('random=0 yields 0 (full jitter floor)', () => {
  assert.equal(backoff(3, { random: () => 0 }), 0);
});

test('result is always an integer within bounds for arbitrary random', () => {
  for (const r of [0.1, 0.37, 0.5, 0.99]) {
    const v = backoff(2, { base: 500, factor: 2, cap: 10000, random: () => r });
    assert.ok(Number.isInteger(v));
    assert.ok(v >= 0 && v <= 2000, `${v} out of [0,2000]`);
  }
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /Users/fredde/repositories/miranda/web && npm test 2>&1 | grep -A2 backoff`
Expected: FAIL — `Cannot find module '../src/net/backoff.js'`.

- [ ] **Step 3: Implement `backoff.js`**

Create `web/src/net/backoff.js`:

```js
// backoff returns a full-jitter exponential delay in ms: a random value in
// [0, min(cap, base*factor**attempt)]. Full jitter (vs equal jitter) avoids
// reconnect thundering-herd and tight loops on a flapping link. random is
// injectable so the policy is deterministically testable.
export function backoff(attempt, { base = 500, factor = 2, cap = 10000, random = Math.random } = {}) {
  const ceil = Math.min(cap, base * factor ** attempt);
  return Math.round(random() * ceil);
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd /Users/fredde/repositories/miranda/web && npm test 2>&1 | tail -6`
Expected: PASS (all suites, incl. the 3 new backoff tests).

- [ ] **Step 5: Commit**

```bash
cd /Users/fredde/repositories/miranda && git add web/src/net/backoff.js web/test/backoff.test.js && git commit -m "feat(web): full-jitter exponential backoff helper"
```

---

## Task 2: `reconnect.js` — `runSession` loop/state machine

**Files:**
- Create: `web/src/net/reconnect.js`
- Test: `web/test/reconnect.test.js`

- [ ] **Step 1: Write the failing test**

Create `web/test/reconnect.test.js`. A `deferred()` helper + a scripted fake `connectOnce` drive the async loop deterministically; `sleep` is immediate so the loop advances on microtasks; `flush()` lets queued microtasks run.

```js
import test from 'node:test';
import assert from 'node:assert';
import { runSession } from '../src/net/reconnect.js';

const flush = async (n = 20) => { for (let i = 0; i < n; i++) await Promise.resolve(); };
const deferred = () => { let resolve, reject; const promise = new Promise((res, rej) => { resolve = res; reject = rej; }); return { promise, resolve, reject }; };

function harness({ visible = true } = {}) {
  const states = [];
  const calls = [];
  let visibleNow = visible;
  let visGate = null;
  return {
    states, calls,
    setVisible(v) { visibleNow = v; if (v && visGate) { visGate.resolve(); visGate = null; } },
    opts: {
      onState: (s, a) => states.push([s, a]),
      isVisible: () => visibleNow,
      waitVisible: () => { visGate = deferred(); return visGate.promise; },
      sleep: () => Promise.resolve(),
      backoffFor: (a) => a, // identity: lets us read the attempt counter via sleep args if needed
    },
  };
}

test('reconnects after a drop and resets the attempt counter on connect', async () => {
  const h = harness();
  const sessions = [deferred(), deferred()];
  let i = 0;
  const connectOnce = (onConnected) => { onConnected(); return sessions[i++].promise; };
  const ctl = runSession({ connectOnce, ...h.opts });
  await flush();
  sessions[0].resolve();            // first session drops
  await flush();
  assert.deepEqual(h.states, [['connecting', 0], ['connected', 0], ['reconnecting', 0], ['connected', 0]]);
  ctl.stop();
});

test('grows the attempt counter on setup failures, then gives up as failed', async () => {
  const h = harness();
  const connectOnce = () => Promise.reject(new Error('no path')); // never connects
  const ctl = runSession({ connectOnce, maxSetupAttempts: 3, ...h.opts });
  await flush(60);
  const failed = h.states.find(([s]) => s === 'failed');
  assert.ok(failed, 'reaches failed state');
  assert.equal(failed[1], 3, 'after maxSetupAttempts');
  ctl.stop();
});

test('does not attempt while hidden; resumes when visible', async () => {
  const h = harness({ visible: false });
  let called = 0;
  const connectOnce = (onConnected) => { called++; onConnected(); return deferred().promise; };
  const ctl = runSession({ connectOnce, ...h.opts });
  await flush();
  assert.equal(called, 0, 'parked while hidden');
  h.setVisible(true);
  await flush();
  assert.equal(called, 1, 'connects once visible');
  ctl.stop();
});

test('retryNow() after failed resets state and reconnects', async () => {
  const h = harness();
  let mode = 'reject';
  const connectOnce = (onConnected) => mode === 'reject'
    ? Promise.reject(new Error('x'))
    : (onConnected(), deferred().promise);
  const ctl = runSession({ connectOnce, maxSetupAttempts: 2, ...h.opts });
  await flush(40);
  assert.ok(h.states.some(([s]) => s === 'failed'));
  mode = 'connect';
  ctl.retryNow();
  await flush();
  assert.equal(h.states.at(-1)[0], 'connected');
  ctl.stop();
});

test('stop() halts the loop', async () => {
  const h = harness();
  let called = 0;
  const connectOnce = (onConnected) => { called++; onConnected(); return deferred().promise; };
  const ctl = runSession({ connectOnce, ...h.opts });
  await flush();
  ctl.stop();
  const n = called;
  await flush();
  assert.equal(called, n, 'no further connects after stop');
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /Users/fredde/repositories/miranda/web && npm test 2>&1 | grep -A2 reconnect`
Expected: FAIL — `Cannot find module '../src/net/reconnect.js'`.

- [ ] **Step 3: Implement `reconnect.js`**

Create `web/src/net/reconnect.js`:

```js
// runSession drives reconnection for one attach. It is DOM-free: every effect is
// injected, so the policy is unit-testable. connectOnce(onConnected) must RESOLVE
// when an established session ENDS (a drop) and REJECT if it never connected
// (setup failure); it must call onConnected() once the channel is live.
//
//   onState(state, attempt) state in connecting|connected|reconnecting|failed
//   isVisible() -> bool ; waitVisible() -> Promise (resolves next time visible)
//   sleep(ms) -> Promise ; backoffFor(attempt) -> ms ; maxSetupAttempts -> int
//
// Returns { stop, retryNow }.
export function runSession({ connectOnce, onState, isVisible, waitVisible, sleep, backoffFor, maxSetupAttempts = 6 }) {
  let stopped = false;
  let everConnected = false;
  let attempt = 0;
  let retryGate = null; // resolve fn while parked in the failed state

  const retryNow = () => { const r = retryGate; retryGate = null; if (r) r(); };

  (async () => {
    while (!stopped) {
      if (!isVisible()) await waitVisible();
      if (stopped) break;
      onState(everConnected ? 'reconnecting' : 'connecting', attempt);
      try {
        await connectOnce(() => { everConnected = true; attempt = 0; onState('connected', 0); });
        // resolved => an established session ended. Fall through to backoff + retry.
      } catch {
        if (!everConnected && ++attempt >= maxSetupAttempts) {
          onState('failed', attempt);
          await new Promise((res) => { retryGate = res; }); // wait for retryNow() or stop()
          everConnected = false; attempt = 0;
          continue; // retry immediately
        }
      }
      if (stopped) break;
      await sleep(backoffFor(attempt));
      if (everConnected) attempt = Math.min(attempt + 1, 8); // flapping grows backoff (bounded)
    }
  })();

  return {
    stop: () => { stopped = true; retryNow(); },
    retryNow,
  };
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd /Users/fredde/repositories/miranda/web && npm test 2>&1 | tail -8`
Expected: PASS — all suites incl. the 5 reconnect tests.

- [ ] **Step 5: Commit**

```bash
cd /Users/fredde/repositories/miranda && git add web/src/net/reconnect.js web/test/reconnect.test.js && git commit -m "feat(web): runSession reconnect loop/state machine (DOM-free, tested)"
```

---

## Task 3: Refactor `attach()` → caller-owned terminal + `connectOnce`

Behavior-preserving restructure: move the durable xterm setup out to the caller and turn the
per-session work into `connectOnce(machine, term, current, onConnected)` that resolves on drop.
After this task the app still does a **single** attach (no reconnect yet) and must still work.

**Files:**
- Modify: `web/src/app.js` (the `attach` function ~48–158 and `viewTerminal`'s call to it ~423)

- [ ] **Step 1: Replace `attach()` with `connectOnce()` + a thin `attach()` wrapper**

In `web/src/app.js`, replace the whole `export async function attach(...) { ... }` body with the
two functions below. `connectOnce` takes an existing `term` and a `current` ref object
(`{ send: null }`); it sets `current.send` when live, runs the recv loop, and resolves when the
DataChannel/PeerConnection drops. The inner Noise-KK handshake and frame-decode logic move
**verbatim** from the old `attach`. A thin `attach()` keeps existing callers/tests working.

```js
// connectOnce establishes ONE P2P session into an existing `term`, wiring keystrokes
// through the shared `current` ref. It resolves when the session ENDS (DataChannel or
// PeerConnection closed) and rejects on a setup error before the channel is live.
// onConnected() is called once the Noise channel is ready. onWindows(snapshot) gets
// each tmux FrameWindows snapshot. Returns nothing; the caller owns `term` + cleanup.
export async function connectOnce(machine, term, current, onConnected, onWindows) {
  const owner = ownerKey();
  const ownerHex = bytesToHex(owner.pub);
  const diag = { step: 'start', ws: 'init', gather: '', iceConn: '', conn: '', dc: 'init' };
  window.__diag = diag;

  const ws = new WebSocket(
    wsBase(machine.signal) + '/attach?owner_id=' + encodeURIComponent(ownerHex) +
    '&machine_id=' + encodeURIComponent(machine.machine_id),
  );
  const wsOpen = awaitSocketOpen(ws).then(
    () => { diag.ws = 'open'; },
    (e) => { diag.ws = 'error'; throw e; },
  );
  const pc = new RTCPeerConnection({ iceServers: await iceServers(machine.signal) });
  const dc = pc.createDataChannel('terminal');
  dc.binaryType = 'arraybuffer';
  pc.oniceconnectionstatechange = () => { diag.iceConn = pc.iceConnectionState; };

  // ended resolves once, when the session drops. The connection-state handler and
  // dc.onclose both feed it so a drop is detected however it manifests; it also
  // unblocks a pending recv() so the read loop exits.
  let endSession;
  const ended = new Promise((res) => { endSession = res; });
  pc.onconnectionstatechange = () => {
    diag.conn = pc.connectionState;
    if (['disconnected', 'failed', 'closed'].includes(pc.connectionState)) endSession();
  };
  dc.onclose = () => endSession();

  ws.onmessage = async (ev) => {
    const m = JSON.parse(ev.data);
    if (m.type === 'answer') { diag.step = 'got-answer'; await pc.setRemoteDescription({ type: 'answer', sdp: m.sdp }); }
    else if (m.type === 'error') { diag.step = 'signal-error'; }
  };

  diag.step = 'ws-connecting';
  await wsOpen;
  diag.step = 'creating-offer';
  await pc.setLocalDescription(await pc.createOffer());
  await new Promise((res) => {
    if (pc.iceGatheringState === 'complete') return res();
    const finish = () => { clearTimeout(t); res(); };
    const t = setTimeout(() => { diag.gather = 'timeout'; finish(); }, 3000);
    pc.addEventListener('icegatheringstatechange', () => { diag.gather = pc.iceGatheringState; if (pc.iceGatheringState === 'complete') finish(); });
  });
  diag.step = 'offer-sent';
  ws.send(JSON.stringify({ type: 'offer', sdp: pc.localDescription.sdp }));

  diag.step = 'awaiting-datachannel';
  const inbox = [];
  let waiter = null;
  dc.onmessage = (ev) => { const u = new Uint8Array(ev.data); if (waiter) { const w = waiter; waiter = null; w(u); } else inbox.push(u); };
  // recv rejects if the session ends while we are waiting, so the read loop unwinds.
  const recv = () => new Promise((resolve, reject) => {
    if (inbox.length) return resolve(inbox.shift());
    waiter = resolve;
    ended.then(() => { if (waiter === resolve) { waiter = null; reject(new Error('session ended')); } });
  });

  await Promise.race([new Promise((res) => (dc.onopen = () => { diag.dc = 'open'; res(); })), ended.then(() => { throw new Error('closed before datachannel'); })]);
  diag.step = 'handshaking';

  const hs = new HandshakeKK({ initiator: true, s: owner, rs: hexToBytes(machine.host_pub) });
  dc.send(hs.writeMessage(new Uint8Array(0)));
  hs.readMessage(await recv());

  // Channel live: publish send through the shared ref and tell the caller.
  current.send = (framed) => dc.send(hs.encrypt(framed));
  try { ws.close(); } catch {} // signalling is done; the data plane is the DC
  diag.step = 'connected';
  onConnected && onConnected();

  // Initial size, then stream until the session ends.
  current.send(encodeResize(term.cols, term.rows));
  try {
    for (;;) {
      const ct = await recv();
      const { type, payload } = decodeFrame(hs.decrypt(ct));
      if (type === FRAME_DATA) term.write(payload);
      else if (type === FRAME_WINDOWS) { try { onWindows && onWindows(JSON.parse(td.decode(payload))); } catch {} }
    }
  } catch { /* recv() rejected: the session ENDED (a normal drop). Swallow so connectOnce
               resolves — runSession then backs off and reconnects. Pre-connect failures
               reject earlier (before onConnected), which is the setup-failure path. */ }
  finally {
    current.send = null;
    try { pc.close(); } catch {}
    try { ws.close(); } catch {}
    window.__attached = false;
  }
  // normal return => connectOnce RESOLVES, signalling "an established session ended".
}
```

- [ ] **Step 2: Build the durable terminal in a small helper and rewrite `attach()` as a wrapper**

Still in `web/src/app.js`, add a `makeTerminal(termEl)` helper that owns the xterm + fit +
resize listeners + the `current` ref + `term.onData/onResize` (bound once, routed through
`current.send`), and rewrite `attach()` to use it for the single-session (no-reconnect) path so
existing callers keep working until Task 4:

```js
// makeTerminal builds the durable terminal: the xterm, its fit/resize wiring, and a
// `current` ref whose .send is swapped per (re)connect. Keystrokes are bound ONCE and
// route through current.send, so they survive reconnects without rebinding.
function makeTerminal(termEl) {
  const term = new Terminal({ fontSize: 13, cursorBlink: true, theme: { background: '#0b0e14' } });
  const fitAddon = new FitAddon();
  term.loadAddon(fitAddon);
  term.open(termEl);
  const refit = () => { try { fitAddon.fit(); } catch {} };
  refit();
  setTimeout(refit, 80);
  const current = { send: null };
  term.onData((d) => current.send && current.send(encodeData(te.encode(d))));
  term.onResize(({ cols, rows }) => current.send && current.send(encodeResize(cols, rows)));
  let rT;
  const onViewport = () => { clearTimeout(rT); rT = setTimeout(() => { refit(); current.send && current.send(encodeResize(term.cols, term.rows)); }, 120); };
  window.addEventListener('resize', onViewport);
  window.visualViewport && window.visualViewport.addEventListener('resize', onViewport);
  window.addEventListener('orientationchange', onViewport);
  const dispose = () => {
    clearTimeout(rT);
    window.removeEventListener('resize', onViewport);
    window.visualViewport && window.visualViewport.removeEventListener('resize', onViewport);
    window.removeEventListener('orientationchange', onViewport);
    term.dispose();
  };
  // test hooks (unchanged surface)
  window.__term = term;
  window.__send = (s) => current.send && current.send(encodeData(te.encode(s)));
  window.__termText = () => { const b = term.buffer.active; let out = ''; for (let i = 0; i < b.length; i++) out += b.getLine(i).translateToString(true) + '\n'; return out; };
  return { term, current, refit, dispose };
}

// attach keeps the single-session contract used by callers/tests in Task 3. Task 4
// replaces viewTerminal's use of it with the reconnecting runSession loop.
export async function attach(machine, termEl, onWindows) {
  const { term, current, dispose } = makeTerminal(termEl);
  term.write('[mir] connecting to ' + (machine.name || machine.machine_id) + '…\r\n');
  await connectOnce(machine, term, current, () => { window.__attached = true; term.focus(); }, onWindows);
  return { term, sendText: (s) => current.send && current.send(encodeData(te.encode(s))), sendCtl: (obj) => current.send && current.send(encodeControl(te.encode(JSON.stringify(obj)))), close: () => { try { current.send = null; } catch {} dispose(); } };
}
```

Note: the old single-blob `attach` is fully replaced by `makeTerminal` + `connectOnce` + this
wrapper. Keep the existing imports (`awaitSocketOpen`, `encodeControl`, etc.) — all are still used.

- [ ] **Step 3: Verify it still builds and the no-reconnect attach still works (live smoke, abbreviated)**

Run the live-smoke backend (see Task 6 setup) and confirm a single attach still reaches a shell:
the browser `__diag.step` should progress to `'connected'`, `window.__attached === true`, and
`window.__termText()` shows a prompt after `window.__send('echo hi\n')`. (No reconnect yet.)

- [ ] **Step 4: Commit**

```bash
cd /Users/fredde/repositories/miranda && git add web/src/app.js && git commit -m "refactor(web): split attach into makeTerminal + connectOnce (resolves on drop)"
```

---

## Task 4: `viewTerminal` reconnect loop + status pill

**Files:**
- Modify: `web/src/app.js` (`viewTerminal` ~280–426)

- [ ] **Step 1: Add a status pill to the topbar and run `runSession`**

In `viewTerminal`, build the durable terminal via `makeTerminal`, add a status pill to the
topbar, and drive reconnection with `runSession`. Replace the current `attach(machine, termBox, …)
.then(…)` call (~423) with the wiring below. Add the imports at the top of `app.js`:

```js
import { runSession } from './net/reconnect.js';
import { backoff } from './net/backoff.js';
```

Inside `viewTerminal(root, machine)`, after `mount(root, view)` and before the tmux control
helpers' first use, replace the terminal/attach setup with:

```js
  const { term, current, dispose } = makeTerminal(termBox);
  term.write('[mir] connecting to ' + (machine.name || machine.machine_id) + '…\r\n');

  // status pill in the topbar (tap to retry when failed)
  const pill = el('button', { className: 'pill status', onclick: () => { if (pill.dataset.failed) session.retryNow(); } }, '…');
  const setPill = (label, cls) => { pill.className = 'pill status ' + cls; pill.textContent = label; pill.dataset.failed = cls === 'failed' ? '1' : ''; };
  // insert the pill into the existing topbar (between title and switch button)
  view.querySelector('.topbar').insertBefore(pill, sw);

  // expose the current send to the tmux control helpers below (they call handle.sendCtl)
  handle = {
    sendCtl: (obj) => { current.send && current.send(encodeControl(te.encode(JSON.stringify(obj)))); focus(); },
    close: () => { session.stop(); try { current.send = null; } catch {} dispose(); },
  };

  const session = runSession({
    connectOnce: (onConnected) => connectOnce(machine, term, current, onConnected, (s) => { snap = s; renderStrip(); }),
    onState: (state, attempt) => {
      if (state === 'connected') { setPill('● live', 'ok'); window.__attached = true; term.focus(); }
      else if (state === 'connecting') setPill('⟳ connecting', 'wait');
      else if (state === 'reconnecting') { setPill('⟳ reconnecting' + (attempt ? ' (' + attempt + ')' : ''), 'wait'); if (attempt === 0) term.write('\r\n[mir] connection lost — reconnecting…\r\n'); }
      else if (state === 'failed') { setPill('⊘ tap to retry', 'failed'); term.write('\r\n[mir] couldn\'t reconnect — tap ⊘ to retry\r\n'); }
    },
    isVisible: () => document.visibilityState === 'visible',
    waitVisible: () => new Promise((res) => { const h = () => { if (document.visibilityState === 'visible') { document.removeEventListener('visibilitychange', h); res(); } }; document.addEventListener('visibilitychange', h); }),
    sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
    backoffFor: (attempt) => backoff(attempt),
  });
```

Then change the existing `viewTerminal` locals: replace `let handle = null;` usage so `handle`
and `session` are declared before use (declare `let handle, session, snap = null;` at the top of
`viewTerminal`), and make the `close()`/back/switch buttons call `handle.close()` (which now
stops the session). Delete the old trailing `attach(machine, termBox, …).then(...).catch(...)`
block — `runSession` owns the lifecycle now. On connect error before any success the pill shows
`connecting (n)` then `failed`; there is no separate `.catch` toast.

- [ ] **Step 2: Add minimal pill styles**

Append to the SPA stylesheet (find the existing `.pill` rules in `web/index.html` or the served
CSS; add near them):

```css
.pill.status { font-variant-numeric: tabular-nums; }
.pill.status.ok { opacity: .8; }
.pill.status.wait { opacity: .8; animation: mir-pulse 1.2s ease-in-out infinite; }
.pill.status.failed { color: #ff6b6b; border-color: #ff6b6b; }
@keyframes mir-pulse { 0%,100% { opacity: .4; } 50% { opacity: .9; } }
```

(If styles live in `web/index.html`, add the block inside its `<style>`; match the existing
indentation and the `__CSP_NONCE__` style element.)

- [ ] **Step 3: Verify reconnect end-to-end (live smoke — Task 6 covers the full run)**

Quick check now: attach in the browser → pill `● live`; `kill` the `mir up` agent → pill flips to
`⟳ reconnecting` and a `[mir] connection lost…` line appears; restart `mir up` → pill returns to
`● live` and the **same** `sh` prompt is usable (`window.__send('echo back\n')` → output renders).

- [ ] **Step 4: Commit**

```bash
cd /Users/fredde/repositories/miranda && git add web/src/app.js web/index.html && git commit -m "feat(web): auto-reconnect with a topbar status pill"
```

---

## Task 5: Consistent connect/pairing error + retry UI

**Files:**
- Modify: `web/src/app.js` (`viewPair` ~221–278)

- [ ] **Step 1: Make the pairing failure actionable (it already has a retry; tighten the copy)**

`viewPair`'s catch already shows `pairing failed: <msg>` + a "Try again" button — keep it, but
replace the bare message with a clearer one and surface the relay it tried. Change the catch block
inside `pairCode`:

```js
    } catch (e) {
      status.innerHTML = '';
      const msg = (e && e.message) || String(e);
      status.append(
        el('div', { className: 'muted' }, 'Pairing failed: ' + msg + '. Check the code is fresh (codes expire after 5 min) and that the machine is still showing it.'),
        el('button', { className: 'btn', onclick: () => viewPair(root) }, 'Try again'));
    }
```

- [ ] **Step 2: Verify**

Run: `cd /Users/fredde/repositories/miranda/web && npm test 2>&1 | tail -4`
Expected: PASS (no test touches this copy; confirms nothing else broke). Then in the browser,
paste a malformed/expired code → the clearer message + "Try again" appears.

- [ ] **Step 3: Commit**

```bash
cd /Users/fredde/repositories/miranda && git add web/src/app.js && git commit -m "feat(web): clearer pairing-failure message with retry"
```

---

## Task 6: Full live-smoke (reconnect) + suite + finalize

**Files:** none (verification)

- [ ] **Step 1: Stand up the local stack**

```bash
cd /Users/fredde/repositories/miranda && make build
rm -rf /tmp/mir-smoke
bin/mir-signal --addr :8765 --webroot /Users/fredde/repositories/miranda/web   # background; pick a free port if needed
```
Pair + serve (in another shell): `bin/mir pair --signal http://localhost:8765 --dir /tmp/mir-smoke --name smokebox` (grab the code), then drive the SPA to pair, then `bin/mir up --dir /tmp/mir-smoke --signal http://localhost:8765 --shell sh`.

- [ ] **Step 2: Drive the browser through the reconnect scenario**

Load `http://localhost:8765/`, dev sign-in, attach. Confirm in order:
1. pill `● live`; `window.__send('echo one\n')` → `one` renders.
2. `kill` the `mir up` process → pill `⟳ reconnecting`, `[mir] connection lost…` line.
3. restart `mir up` (same `--dir`) → pill `● live`; `window.__send('echo two\n')` → `two` renders in the **same** session.
4. `kill` BOTH `mir up` and the relay → pill cycles `⟳ reconnecting (n)` → `⊘ tap to retry` after ~6 attempts.
5. restart relay + `mir up`, tap the pill → reconnects, pill `● live`.

- [ ] **Step 3: Full suite + cleanup**

Run: `cd /Users/fredde/repositories/miranda/web && npm test 2>&1 | tail -6`
Expected: PASS (existing + backoff + reconnect suites).
Then kill the smoke relay/agent and `rm -rf /tmp/mir-smoke`.

- [ ] **Step 4: Finalize**

All tasks are committed individually. Proceed to finishing-a-development-branch (PR, like A1/#26).

---

## Self-Review

- [ ] **Spec coverage:** auto-reconnect + reuse xterm → Tasks 3–4 (`makeTerminal` durable term +
  `connectOnce` resolves-on-drop + `runSession`). Backoff → Task 1. State machine + pause-when-
  hidden + give-up + manual retry → Task 2 + the Task 4 wiring. Status pill + in-terminal lines →
  Task 4. Pairing/connect error UX → Task 5. Unit tests (backoff, reconnect) → Tasks 1–2; live
  smoke → Task 6. Non-goals (keystroke queueing, WS-reconnect-for-its-own-sake) are not built.
- [ ] **Placeholder scan:** none — pure-logic tasks show full test+impl; DOM tasks show the full
  restructured functions and are gated by the live smoke (the spec's stated verification for the
  WebRTC/DOM glue).
- [ ] **Type/name consistency:** `connectOnce(machine, term, current, onConnected, onWindows)` and
  `runSession({connectOnce, onState, isVisible, waitVisible, sleep, backoffFor, maxSetupAttempts})`
  and `backoff(attempt, opts)` are used identically across tasks. `current.send` is the single
  swap point everywhere. The `runSession` `connectOnce` adapter passes only `onConnected` (matching
  the loop's call `connectOnce(() => …)`) and closes over `machine/term/current/onWindows`.
- [ ] **Working-after-each-task:** Task 3 keeps a single-attach `attach()` wrapper so the app runs
  before reconnect lands in Task 4.
