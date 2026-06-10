import test from 'node:test';
import assert from 'node:assert';
import { awaitSocketOpen } from '../src/net/ws-open.js';

// A minimal WebSocket-ish fake: a readyState plus addEventListener, and a fire()
// helper to dispatch a one-shot event to whatever listeners are attached AT THAT
// MOMENT (matching the browser's one-shot semantics that the bug tripped over).
function fakeSocket(readyState) {
  const listeners = {};
  return {
    readyState,
    addEventListener(type, fn) { (listeners[type] ||= []).push(fn); },
    fire(type) { for (const fn of listeners[type] || []) fn(); },
  };
}

// The regression: the old code did `new Promise(r => ws.onopen = ...)` with no
// readyState guard, so a socket that was ALREADY open never resolved -> attach()
// hung forever. awaitSocketOpen must resolve immediately in that case.
test('resolves immediately when the socket is already OPEN', async () => {
  const ws = fakeSocket(1); // OPEN
  await awaitSocketOpen(ws); // hangs (test times out) under the old behavior
});

test('resolves when "open" fires after subscription (CONNECTING -> OPEN)', async () => {
  const ws = fakeSocket(0); // CONNECTING
  const p = awaitSocketOpen(ws);
  ws.fire('open');
  await p;
});

test('rejects on "error"', async () => {
  const ws = fakeSocket(0);
  const p = awaitSocketOpen(ws);
  ws.fire('error');
  await assert.rejects(p, /signal socket error/);
});

// An 'open' that fires before anyone could attach a listener is exactly the race;
// the readyState guard (not the listener) is what saves it.
test('does not depend on catching the one-shot open event', async () => {
  const ws = fakeSocket(0);
  ws.readyState = 1; // opened during the gap, before awaitSocketOpen is called
  await awaitSocketOpen(ws); // resolves via the guard, with no 'open' event fired
});
