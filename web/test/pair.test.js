import test from 'node:test';
import assert from 'node:assert';
import { mock } from 'node:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { hexToBytes } from '@noble/hashes/utils';
import { encodeCode } from '../src/pairing/code.js';
import { runResponder } from '../src/pairing/nnpsk0.js';
import { safetyNumber } from '../src/pairing/sas.js';

// pairWithCode is the browser pairing entry point. These tests pin its FAILURE
// handling — the bug being fixed: with no recv timeout and close/error wired only
// during the initial open, a stalled or dropped /pair relay left the UI hanging on
// "pairing…" forever. We drive it with a fake WebSocket and node's mock timers.
//
// We import pair.js lazily (after installing the fake WebSocket on globalThis) so the
// module under test constructs our fake. ownerPub is unused on the failure paths
// (runInitiator's first await is a recv()), so a 32-byte zero key is fine.

const ownerPub = new Uint8Array(32);
// A well-formed code: 16-byte (32-hex) token + an https relay URL passes decodeCode.
const CODE = encodeCode('https://relay.example.test', new Uint8Array(16).fill(7));

// A minimal WebSocket double. The constructor records the instance so the test can
// drive its event handlers (onopen/onmessage/onclose/onerror) by hand.
function installFakeWS() {
  const created = [];
  class FakeWS {
    constructor(url) {
      this.url = url;
      this.readyState = 0; // CONNECTING
      this.onopen = this.onmessage = this.onclose = this.onerror = null;
      this.sent = [];
      this.closed = false;
      created.push(this);
    }
    send(b) { this.sent.push(b); }
    close() { this.closed = true; this.readyState = 3; }
    // helpers for the test
    fireOpen() { this.readyState = 1; this.onopen && this.onopen(); }
    fireMessage(data) { this.onmessage && this.onmessage({ data }); }
    fireClose() { this.onclose && this.onclose(); }
    fireError() { this.onerror && this.onerror(); }
  }
  const prev = globalThis.WebSocket;
  globalThis.WebSocket = FakeWS;
  return { created, restore: () => { globalThis.WebSocket = prev; } };
}

// import after the harness so each test gets the real (singleton) module but our fake
// constructor — pair.js reads globalThis.WebSocket at call time, so one import is fine.
const { pairWithCode } = await import('../src/pair.js');

test('rejects (does not hang) when the relay opens but never sends — timeout fires', async () => {
  const ws = installFakeWS();
  mock.timers.enable({ apis: ['setTimeout'] });
  try {
    const p = pairWithCode(CODE, ownerPub);
    const settled = p.then(() => 'resolved', (e) => e); // capture without throwing yet
    await Promise.resolve();
    ws.created[0].fireOpen(); // socket opens, runInitiator sends msg1 and awaits recv()
    await Promise.resolve();
    await Promise.resolve();
    mock.timers.tick(30000); // advance to the 30s ceiling
    const result = await settled;
    assert.ok(result instanceof Error, 'pairing rejects rather than hanging');
    assert.match(result.message, /timed out/);
    assert.ok(ws.created[0].closed, 'socket is closed on the way out');
  } finally {
    mock.timers.reset();
    ws.restore();
  }
});

test('rejects when the relay closes mid-handshake (post-open close wired)', async () => {
  const ws = installFakeWS();
  try {
    const p = pairWithCode(CODE, ownerPub);
    const settled = p.then(() => 'resolved', (e) => e);
    await Promise.resolve();
    ws.created[0].fireOpen();
    await Promise.resolve();
    await Promise.resolve();
    ws.created[0].fireClose(); // relay drops while we await msg2
    const result = await settled;
    assert.ok(result instanceof Error, 'a mid-handshake close rejects the pending recv');
    assert.match(result.message, /closed the connection/);
  } finally {
    ws.restore();
  }
});

test('rejects when the relay errors mid-handshake (post-open error wired)', async () => {
  const ws = installFakeWS();
  try {
    const p = pairWithCode(CODE, ownerPub);
    const settled = p.then(() => 'resolved', (e) => e);
    await Promise.resolve();
    ws.created[0].fireOpen();
    await Promise.resolve();
    await Promise.resolve();
    ws.created[0].fireError();
    const result = await settled;
    assert.ok(result instanceof Error, 'a mid-handshake error rejects the pending recv');
    assert.match(result.message, /relay error/);
  } finally {
    ws.restore();
  }
});

test('rejects when the relay is unreachable (pre-open error)', async () => {
  const ws = installFakeWS();
  try {
    const p = pairWithCode(CODE, ownerPub);
    const settled = p.then(() => 'resolved', (e) => e);
    await Promise.resolve();
    ws.created[0].fireError(); // never opened
    const result = await settled;
    assert.ok(result instanceof Error);
    assert.match(result.message, /could not reach the pairing relay/);
  } finally {
    ws.restore();
  }
});

test('rejects when the relay closes before opening (pre-open close)', async () => {
  const ws = installFakeWS();
  try {
    const p = pairWithCode(CODE, ownerPub);
    const settled = p.then(() => 'resolved', (e) => e);
    await Promise.resolve();
    ws.created[0].fireClose(); // closed before open
    const result = await settled;
    assert.ok(result instanceof Error);
    assert.match(result.message, /closed the connection/);
  } finally {
    ws.restore();
  }
});

// Happy path through pairWithCode, proving the post-open re-wiring of onmessage /
// onclose / onerror did NOT break a successful handshake: a real NNpsk0 responder
// answers over the fake socket and pairWithCode returns the machine + safety number
// from the Go interop vector.
const here = dirname(fileURLToPath(import.meta.url));
const vec = JSON.parse(readFileSync(join(here, '..', '..', 'testdata', 'pair-interop.json'), 'utf8'));

test('completes the handshake and returns the machine + safety number', async () => {
  const ws = installFakeWS();
  try {
    const token = hexToBytes(vec.token);
    const owner = hexToBytes(vec.owner_pub);
    const goodCode = encodeCode('https://relay.example.test', token);
    const info = JSON.parse(vec.info_json);

    // A MsgConn for the responder bridged to the fake client socket: the responder
    // recv()s the client's msg1 (from sock.sent) and send()s msg2 back via fireMessage.
    const sock = ws.created; // filled once pairWithCode constructs the socket
    let sentIdx = 0;
    const responderMC = {
      recv: async () => {
        for (;;) {
          if (sock[0] && sock[0].sent.length > sentIdx) return sock[0].sent[sentIdx++];
          await new Promise((r) => setTimeout(r, 1));
        }
      },
      send: async (m) => { sock[0].fireMessage(m); },
    };
    // pairWithCode's initiator uses a RANDOM ephemeral (not injectable), so the SAS
    // won't match the all-fixed-ephemeral Go vector. Instead assert the two sides
    // agree on the binding and that the SAS is well-formed.
    const responderP = runResponder(responderMC, token, info, hexToBytes('2122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f40'));

    const p = pairWithCode(goodCode, owner);
    await Promise.resolve();
    sock[0].fireOpen();
    const result = await p;
    const resp = await responderP;

    assert.equal(result.machine.machine_id, 'm42');
    assert.equal(result.machine.name, 'box');
    assert.equal(result.machine.host_pub, info.host_pub);
    assert.equal(result.machine.signal, 'https://relay.example.test');
    assert.match(result.safetyNumber, /^[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}$/, 'well-formed SAS');
    assert.equal(result.safetyNumber, safetyNumber(resp.binding), 'both peers derive the SAME safety number');
    assert.ok(sock[0].closed, 'socket closed after a successful pairing');
  } finally {
    ws.restore();
  }
});
