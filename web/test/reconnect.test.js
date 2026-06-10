import test from 'node:test';
import assert from 'node:assert';
import { runSession } from '../src/net/reconnect.js';

const flush = async (n = 20) => { for (let i = 0; i < n; i++) await Promise.resolve(); };
const deferred = () => { let resolve, reject; const promise = new Promise((res, rej) => { resolve = res; reject = rej; }); return { promise, resolve, reject }; };

function harness({ visible = true } = {}) {
  const states = [];
  let visibleNow = visible;
  let visGate = null;
  return {
    states,
    setVisible(v) { visibleNow = v; if (v && visGate) { visGate.resolve(); visGate = null; } },
    opts: {
      onState: (s, a) => states.push([s, a]),
      isVisible: () => visibleNow,
      waitVisible: () => { visGate = deferred(); return visGate.promise; },
      sleep: () => Promise.resolve(),
      backoffFor: (a) => a,
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
  sessions[0].resolve(); // first session drops
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
