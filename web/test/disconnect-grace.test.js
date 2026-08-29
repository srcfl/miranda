import test from 'node:test';
import assert from 'node:assert/strict';
import { makeDisconnectGrace, DISCONNECT_GRACE_MS } from '../src/net/disconnect-grace.js';
import { iceSessionDegraded } from '../src/net/ice-state.js';
import { backoff } from '../src/net/backoff.js';

// Injectable timers: capture the callback + delay; fire() runs a pending timer.
function fakeTimers() {
  let pending = null; // { fn, ms, id }
  let nextId = 1;
  return {
    setTimer: (fn, ms) => { const id = nextId++; pending = { fn, ms, id }; return id; },
    clearTimer: (id) => { if (pending && pending.id === id) pending = null; },
    fire: () => { const p = pending; pending = null; if (p) p.fn(); },
    pending: () => pending,
  };
}

function harness(over = {}) {
  const t = fakeTimers();
  const calls = { kill: 0, degraded: 0, recovered: 0 };
  const g = makeDisconnectGrace({
    kill: () => calls.kill++,
    onDegraded: () => calls.degraded++,
    onRecovered: () => calls.recovered++,
    setTimer: t.setTimer,
    clearTimer: t.clearTimer,
    ...over,
  });
  return { t, calls, g };
}

test('disconnected reports degraded once and arms the grace timer', () => {
  const { t, calls, g } = harness();
  g.onState('connected');
  g.onState('disconnected');
  assert.equal(calls.degraded, 1);
  assert.ok(t.pending(), 'grace timer armed');
  assert.equal(t.pending().ms, DISCONNECT_GRACE_MS);
  g.onState('disconnected'); // repeated events must not re-fire or re-arm
  assert.equal(calls.degraded, 1);
});

test('grace expiry kills the session', () => {
  const { t, calls, g } = harness();
  g.onState('disconnected');
  t.fire();
  assert.equal(calls.kill, 1);
});

test('recovery inside the grace window disarms and reports up', () => {
  const { t, calls, g } = harness();
  g.onState('disconnected');
  g.onState('connected');
  assert.equal(t.pending(), null, 'timer disarmed');
  assert.equal(calls.recovered, 1);
  assert.equal(calls.kill, 0);
  t.fire(); // nothing pending: must be a no-op
  assert.equal(calls.kill, 0);
});

test('failed/closed disarm the timer (the dead path owns teardown)', () => {
  for (const state of ['failed', 'closed']) {
    const { t, calls, g } = harness();
    g.onState('disconnected');
    g.onState(state);
    assert.equal(t.pending(), null, state + ' must disarm');
    assert.equal(calls.kill, 0);
  }
});

test('dispose disarms a pending grace timer', () => {
  const { t, calls, g } = harness();
  g.onState('disconnected');
  g.dispose();
  assert.equal(t.pending(), null);
  assert.equal(calls.kill, 0);
});

test('iceSessionDegraded flags only disconnected', () => {
  assert.equal(iceSessionDegraded('disconnected'), true);
  for (const s of ['connected', 'connecting', 'failed', 'closed', 'new']) {
    assert.equal(iceSessionDegraded(s), false);
  }
});

// The R1 resume budget: after a network flip the pieces the CLIENT controls —
// the grace window plus the healthy-drop backoff (failures=0, full jitter, so
// its cap is backoff(0)'s ceiling) — must leave at least a second of the 3 s
// target for the browser's own `disconnected` detection and the fresh connect.
test('grace + healthy-drop backoff leave >=1.3s of the 3s resume budget', () => {
  let worstBackoff0 = 0;
  for (let i = 0; i < 200; i++) worstBackoff0 = Math.max(worstBackoff0, backoff(0, { random: () => 1 }));
  assert.ok(DISCONNECT_GRACE_MS + worstBackoff0 <= 1700,
    `grace ${DISCONNECT_GRACE_MS} + backoff(0) cap ${worstBackoff0} must be <= 1700ms`);
});
