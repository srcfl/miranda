// web/test/registry-web.test.js — the browser-side registry discovery (B2):
// decode the relay's encrypted entries, drop forgeries, merge, and flag new devices.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { hexToBytes } from '@noble/hashes/utils';
import { registryKey, sealRecord } from '../src/identity/registry.js';
import { decodeRegistry, mergeMachines, freshDevices, sealMachineRecord } from '../src/registry.js';

const enc = new TextEncoder();
const b64 = (u8) => Buffer.from(u8).toString('base64');

// entry seals a record under `secret`'s key, AAD = machine_id, as the relay would
// serve it: { machine_id, blob(base64) }.
function entry(secret, machineID, rec) {
  const key = registryKey(secret);
  const nonce = new Uint8Array(12).fill(7);
  const blob = sealRecord(key, nonce, enc.encode(JSON.stringify(rec)), machineID);
  return { machine_id: machineID, blob: b64(blob) };
}

const SECRET = hexToBytes('00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff');

test('decodeRegistry opens your records and drops forgeries', () => {
  const good = entry(SECRET, 'm1', { v: 1, name: 'laptop', host_pub: 'aa'.repeat(32), signal_url: 'https://relay.example' });
  const forged = entry(hexToBytes('ff'.repeat(32)), 'm2', { v: 1, name: 'evil', host_pub: 'bb'.repeat(32) }); // wrong key
  const out = decodeRegistry([good, forged], SECRET, 'https://fallback.example');
  assert.equal(out.length, 1, 'the forgery must be dropped');
  assert.deepEqual(out[0], { machine_id: 'm1', name: 'laptop', host_pub: 'aa'.repeat(32), signal: 'https://relay.example', name_ts: 0 });
});

test('decodeRegistry falls back to the fetch origin when a record has no signal_url', () => {
  const e = entry(SECRET, 'm3', { v: 1, name: 'box', host_pub: 'cc'.repeat(32) });
  const out = decodeRegistry([e], SECRET, 'https://origin.example');
  assert.equal(out[0].signal, 'https://origin.example');
});

test('mergeMachines: verified registry host_pub/signal beat local cache', () => {
  const local = [{ machine_id: 'm1', name: 'local-laptop', host_pub: 'aa'.repeat(32), signal: 'https://old.example' }];
  const disc = [
    { machine_id: 'm1', name: 'reg-laptop', host_pub: 'bb'.repeat(32), signal: 'https://relay.example', name_ts: 100 },
    { machine_id: 'm2', name: 'desktop', host_pub: 'cc'.repeat(32), signal: 'https://relay.example' },
  ];
  const merged = mergeMachines(local, disc);
  assert.equal(merged.length, 2);
  const m1 = merged.find((m) => m.machine_id === 'm1');
  assert.equal(m1.host_pub, 'bb'.repeat(32), 'registry host_pub is canonical');
  assert.equal(m1.signal, 'https://relay.example', 'registry signal is canonical');
  assert.equal(m1.name, 'reg-laptop', 'a newer registry name (a rename made elsewhere) wins');
  assert.equal(merged.find((m) => m.machine_id === 'm2').name, 'desktop');
});

// N1 rename: the display name is last-writer-wins on name_ts (mirrors Go
// MergeMachines — go/internal/client/registry.go).
test('mergeMachines: name is last-writer-wins on name_ts', () => {
  const id = 'm1';
  const cases = [
    // Legacy local (no name_ts): registry canonical -> rename propagates here.
    { local: { machine_id: id, name: 'old' }, disc: { machine_id: id, name: 'renamed', name_ts: 100 }, want: 'renamed' },
    // A local rename not yet delivered to the machine keeps winning.
    { local: { machine_id: id, name: 'mine', name_ts: 200 }, disc: { machine_id: id, name: 'stale', name_ts: 100 }, want: 'mine' },
    // Once the machine republishes (same ts), the renaming device converges.
    { local: { machine_id: id, name: 'renamed', name_ts: 100 }, disc: { machine_id: id, name: 'renamed', name_ts: 100 }, want: 'renamed' },
    // An empty registry name never clobbers.
    { local: { machine_id: id, name: 'kept', name_ts: 5 }, disc: { machine_id: id, name: '', name_ts: 999 }, want: 'kept' },
  ];
  for (const c of cases) {
    const merged = mergeMachines([c.local], [c.disc]);
    assert.equal(merged.length, 1);
    assert.equal(merged[0].name, c.want, JSON.stringify(c));
  }
});

// The rename path seals a fresh record the same way pairing does; decodeRegistry
// must open it and carry its ts through as name_ts.
test('sealMachineRecord round-trips through decodeRegistry with its ts', () => {
  const { blob, ts } = sealMachineRecord(SECRET, {
    machine_id: 'm9', name: 'renamed-box', host_pub: 'dd'.repeat(32), signal_url: 'https://relay.example',
  });
  const out = decodeRegistry([{ machine_id: 'm9', blob }], SECRET, 'https://fallback.example');
  assert.equal(out.length, 1);
  assert.equal(out[0].name, 'renamed-box');
  assert.equal(out[0].name_ts, ts);
  assert.ok(ts > 0);
});

test('freshDevices flags only unseen machine_ids', () => {
  const disc = [{ machine_id: 'm1', name: 'a' }, { machine_id: 'm2', name: 'b' }];
  assert.deepEqual(freshDevices(['m1'], disc).map((m) => m.machine_id), ['m2']);
  assert.deepEqual(freshDevices(['m1', 'm2'], disc), []);
});
