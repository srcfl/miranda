// web/test/pool.test.js — R2 warm multi-machine policy: the LRU pool bound,
// the hidden-tab park policy, and the state→UI mapping (which pins R1's pill
// strings so moving the mapping into pool.js cannot change what the pill says).
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { makePool, makeParkPolicy, stateView, HIDDEN_PARK_MS } from '../src/net/pool.js';

// --- makePool: bounded LRU, active never evicted -------------------------

test('pool holds up to max machines with no eviction', () => {
  const pool = makePool({ max: 3 });
  assert.deepEqual(pool.activate('a').evict, []);
  assert.deepEqual(pool.activate('b').evict, []);
  assert.deepEqual(pool.activate('c').evict, []);
  assert.equal(pool.size(), 3);
  assert.equal(pool.activeId(), 'c');
});

test('overflow evicts the least-recently-used background machine', () => {
  let t = 0;
  const pool = makePool({ max: 3, now: () => ++t });
  pool.activate('a'); pool.activate('b'); pool.activate('c');
  assert.deepEqual(pool.activate('d').evict, ['a']); // a is LRU
  assert.deepEqual(pool.ids().sort(), ['b', 'c', 'd']);
});

test('re-activating refreshes recency, changing who is evicted', () => {
  let t = 0;
  const pool = makePool({ max: 3, now: () => ++t });
  pool.activate('a'); pool.activate('b'); pool.activate('c');
  pool.activate('a'); // a is now the most recent (and active)
  assert.deepEqual(pool.activate('d').evict, ['b']); // b became LRU
});

test('the active machine is never evicted', () => {
  let t = 0;
  const pool = makePool({ max: 1, now: () => ++t });
  pool.activate('a');
  const { evict } = pool.activate('b');
  assert.deepEqual(evict, ['a']);
  assert.equal(pool.activeId(), 'b');
  assert.ok(pool.has('b'));
});

test('drop removes a machine and clears active when it was active', () => {
  const pool = makePool({ max: 3 });
  pool.activate('a'); pool.activate('b');
  pool.drop('b');
  assert.equal(pool.activeId(), null);
  assert.ok(!pool.has('b'));
  assert.ok(pool.has('a'));
});

// --- makeParkPolicy: injectable timers, no DOM ---------------------------

function fakeTimers() {
  let seq = 0;
  const pending = new Map();
  return {
    setTimer: (fn, ms) => { const id = ++seq; pending.set(id, { fn, ms }); return id; },
    clearTimer: (id) => pending.delete(id),
    fire: () => { for (const [id, t] of [...pending]) { pending.delete(id); t.fn(); } },
    count: () => pending.size,
    delayOf: (i = 0) => [...pending.values()][i] && [...pending.values()][i].ms,
  };
}

test('park fires only after the hidden delay, once', () => {
  const t = fakeTimers();
  let parks = 0, resumes = 0;
  const p = makeParkPolicy({ onPark: () => parks++, onResume: () => resumes++, setTimer: t.setTimer, clearTimer: t.clearTimer });
  p.visibility(true);
  assert.equal(t.delayOf(), HIDDEN_PARK_MS); // the delay is the policy's, not ad hoc
  assert.equal(parks, 0);          // not yet — the delay has not passed
  p.visibility(true);              // repeated hidden events do not stack timers
  assert.equal(t.count(), 1);
  t.fire();
  assert.equal(parks, 1);
  assert.ok(p.isParked());
  assert.equal(resumes, 0);
});

test('a quick app-switch (visible before the delay) parks nothing', () => {
  const t = fakeTimers();
  let parks = 0, resumes = 0;
  const p = makeParkPolicy({ onPark: () => parks++, onResume: () => resumes++, setTimer: t.setTimer, clearTimer: t.clearTimer });
  p.visibility(true);
  p.visibility(false); // back before the delay
  t.fire();            // nothing should be pending
  assert.equal(parks, 0);
  assert.equal(resumes, 0); // never parked -> nothing to resume
});

test('returning to the tab resumes exactly once', () => {
  const t = fakeTimers();
  let resumes = 0;
  const p = makeParkPolicy({ onResume: () => resumes++, setTimer: t.setTimer, clearTimer: t.clearTimer });
  p.visibility(true); t.fire(); // parked
  p.visibility(false);
  p.visibility(false);
  assert.equal(resumes, 1);
  assert.ok(!p.isParked());
});

test('dispose cancels a pending park timer', () => {
  const t = fakeTimers();
  let parks = 0;
  const p = makeParkPolicy({ onPark: () => parks++, setTimer: t.setTimer, clearTimer: t.clearTimer });
  p.visibility(true);
  p.dispose();
  t.fire();
  assert.equal(parks, 0);
});

// --- stateView: R1's pill strings, pinned --------------------------------

test('stateView keeps R1 pill strings verbatim', () => {
  assert.deepEqual(stateView({ conn: 'connected' }), { pill: '● live', cls: 'ok', chip: 'live' });
  assert.deepEqual(stateView({ conn: 'connected', degraded: true }), { pill: '⟳ resuming', cls: 'wait', chip: 'wait' });
  assert.deepEqual(stateView({ conn: 'connecting' }), { pill: '⟳ connecting', cls: 'wait', chip: 'wait' });
  assert.deepEqual(stateView({ conn: 'reconnecting', attempt: 0 }), { pill: '⟳ reconnecting', cls: 'wait', chip: 'wait' });
  assert.deepEqual(stateView({ conn: 'reconnecting', attempt: 2 }), { pill: '⟳ reconnecting (2)', cls: 'wait', chip: 'wait' });
  assert.deepEqual(stateView({ conn: 'failed' }), { pill: '⊘ tap to retry', cls: 'failed', chip: 'failed' });
});

test('parked wins over everything else', () => {
  assert.deepEqual(stateView({ conn: 'connected', parked: true }), { pill: '⏸ parked', cls: 'wait', chip: 'parked' });
});

// --- app.js shape: switching must not tear down --------------------------
// In the spirit of empty-state.test.js: pin the load-bearing wiring so a later
// edit cannot quietly turn "switch" back into "close and reopen".

const appSrc = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '..', 'src/app.js'), 'utf8');

test('switchTo never closes a session', () => {
  const body = appSrc.slice(appSrc.indexOf('function switchTo'), appSrc.indexOf('switchTo(machineToOpen)'));
  assert.ok(body.length > 0, 'switchTo must exist');
  assert.doesNotMatch(body, /closeSession|disposeTerm|loop\.stop/);
});

test('Back leaves the pool warm (no close on the way to the machine list)', () => {
  const m = appSrc.match(/const back = el\('button', \{ className: 'tb-btn', onclick: ([^}]*)\}/);
  assert.ok(m, 'back button must exist');
  assert.doesNotMatch(m[1], /close/);
});

test('frames route to their own session state, not the mounted view', () => {
  const body = appSrc.slice(appSrc.indexOf('function startLoop'), appSrc.indexOf('function openSession'));
  assert.match(body, /sess\.snap = s/);
  assert.match(body, /connectOnce\(sess\.machine, sess\.term, sess\.current/);
});
