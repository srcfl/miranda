import test from 'node:test';
import assert from 'node:assert';
import { runSession } from '../src/net/reconnect.js';

const flush = async (n = 40) => { for (let i = 0; i < n; i++) await Promise.resolve(); };
const deferred = () => { let resolve, reject; const promise = new Promise((res, rej) => { resolve = res; reject = rej; }); return { promise, resolve, reject }; };

// A controllable clock: now() returns whatever we set; set()/advance() move it.
// Lets a test stage a session's "uptime" (now-at-drop − now-at-connect) exactly.
function clock(start = 0) {
  let t = start;
  return { now: () => t, set: (v) => { t = v; }, advance: (d) => { t += d; } };
}

function harness(extra = {}) {
  const states = [];
  const clk = clock(0);
  return {
    states,
    clk,
    opts: {
      onState: (s, a) => states.push([s, a]),
      sleep: () => Promise.resolve(),
      backoffFor: (a) => a,
      now: clk.now,
      minHealthyMs: 5000,
      ...extra,
    },
  };
}

test('reconnects after a healthy drop and keeps failures at 0', async () => {
  const h = harness();
  const sessions = [deferred(), deferred()];
  let i = 0;
  // connect at now=0; advance the clock past minHealthyMs before dropping.
  const connectOnce = (onConnected) => { onConnected(); return sessions[i++].promise; };
  const ctl = runSession({ connectOnce, ...h.opts });
  await flush();
  h.clk.set(10000); // uptime 10s >= minHealthyMs => a healthy drop
  sessions[0].resolve(); // first session drops cleanly
  await flush();
  // healthy drop => failures stays 0 => reconnecting reports 0
  assert.deepEqual(h.states, [['connecting', 0], ['connected', 0], ['reconnecting', 0], ['connected', 0]]);
  ctl.stop();
});

test('a flap (uptime < minHealthyMs) grows the failure counter', async () => {
  const h = harness();
  const sessions = [deferred(), deferred(), deferred()];
  let i = 0;
  const connectOnce = (onConnected) => { onConnected(); return sessions[i++].promise; };
  const ctl = runSession({ connectOnce, ...h.opts });
  await flush();
  // drop after only 100ms of uptime => a flap, not healthy.
  h.clk.set(100);
  sessions[0].resolve();
  await flush();
  // The reconnecting state after a flap carries a GROWING failure count (1), not 0
  // — that growing count is what drives backoff and prevents the storm.
  const reconnects = h.states.filter(([s]) => s === 'reconnecting').map(([, a]) => a);
  assert.deepEqual(reconnects, [1], 'flap bumps failures to 1');
  // a second flap grows it further
  h.clk.set(150);
  sessions[1].resolve();
  await flush();
  const reconnects2 = h.states.filter(([s]) => s === 'reconnecting').map(([, a]) => a);
  assert.deepEqual(reconnects2, [1, 2], 'second flap bumps failures to 2');
  ctl.stop();
});

test('connect then flapping drops grow backoff and reach failed', async () => {
  const backoffs = [];
  const h = harness({ backoffFor: (a) => { backoffs.push(a); return a; } });
  // Each session connects (so it WAS connected once) but immediately flaps: uptime
  // stays 0 (clock never advances), so every drop is a flap => failures grow.
  const connectOnce = (onConnected) => { onConnected(); return Promise.resolve(); };
  const ctl = runSession({ connectOnce, maxAttempts: 4, ...h.opts });
  await flush(120);
  const failed = h.states.find(([s]) => s === 'failed');
  assert.ok(failed, 'reaches failed even though it connected at least once');
  assert.equal(failed[1], 4, 'failed reported with the failure count == maxAttempts');
  // Each connect immediately flaps, so the 1st drop is already failure #1: backoff
  // GROWS (1,2,3) across the retries before giving up — NOT stuck at 0 (the storm).
  assert.deepEqual(backoffs, [1, 2, 3], 'backoff grows with consecutive failures');
  ctl.stop();
});

test('grows the failure counter on setup failures, then gives up as failed', async () => {
  const h = harness();
  const connectOnce = () => Promise.reject(new Error('no path')); // never connects
  const ctl = runSession({ connectOnce, maxAttempts: 3, ...h.opts });
  await flush(80);
  const failed = h.states.find(([s]) => s === 'failed');
  assert.ok(failed, 'reaches failed state');
  assert.equal(failed[1], 3, 'after maxAttempts');
  ctl.stop();
});

test('parks in failed and does not storm until retryNow()', async () => {
  const h = harness();
  let calls = 0;
  const connectOnce = () => { calls++; return Promise.reject(new Error('x')); };
  const ctl = runSession({ connectOnce, maxAttempts: 2, ...h.opts });
  await flush(60);
  assert.ok(h.states.some(([s]) => s === 'failed'), 'reached failed');
  const afterFail = calls;
  await flush(60);
  assert.equal(calls, afterFail, 'no further connect attempts while parked in failed (no storm)');
  ctl.stop();
});

test('retryNow() after failed resets state and reconnects', async () => {
  const h = harness();
  let mode = 'reject';
  const connectOnce = (onConnected) => mode === 'reject'
    ? Promise.reject(new Error('x'))
    : (onConnected(), deferred().promise);
  const ctl = runSession({ connectOnce, maxAttempts: 2, ...h.opts });
  await flush(60);
  assert.ok(h.states.some(([s]) => s === 'failed'));
  mode = 'connect';
  ctl.retryNow();
  await flush();
  assert.equal(h.states.at(-1)[0], 'connected');
  ctl.stop();
});

test('a healthy drop resets failures accumulated by earlier flaps', async () => {
  const h = harness();
  const sessions = [deferred(), deferred(), deferred()];
  let i = 0;
  const connectOnce = (onConnected) => { onConnected(); return sessions[i++].promise; };
  const ctl = runSession({ connectOnce, ...h.opts });
  await flush();
  // flap: short uptime
  h.clk.set(100);
  sessions[0].resolve();
  await flush();
  // healthy: long uptime, measured from the reconnect time (100).
  h.clk.set(100 + 9000);
  sessions[1].resolve();
  await flush();
  const reconnects = h.states.filter(([s]) => s === 'reconnecting').map(([, a]) => a);
  // first reconnect after the flap = 1, second after the healthy drop = 0 (reset).
  assert.deepEqual(reconnects, [1, 0], 'healthy drop resets failures back to 0');
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

test('defaults: now and minHealthyMs are optional (real Date.now path)', async () => {
  // Exercise the default-injection path: omit now + minHealthyMs entirely. A session
  // that resolves immediately (uptime ~0) is a flap and must grow failures to failed.
  const states = [];
  const ctl = runSession({
    connectOnce: (onConnected) => { onConnected(); return Promise.resolve(); },
    onState: (s, a) => states.push([s, a]),
    sleep: () => Promise.resolve(),
    backoffFor: (a) => a,
    maxAttempts: 3,
  });
  await flush(120);
  assert.ok(states.some(([s]) => s === 'failed'), 'reaches failed with default clock');
  ctl.stop();
});
