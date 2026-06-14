// web/test/registry-web.test.js — the browser-side registry discovery (B2):
// decode the relay's encrypted entries, drop forgeries, merge, and flag new devices.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { hexToBytes } from '@noble/hashes/utils';
import { registryKey, sealRecord } from '../src/identity/registry.js';
import { decodeRegistry, mergeMachines, freshDevices } from '../src/registry.js';

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
  assert.deepEqual(out[0], { machine_id: 'm1', name: 'laptop', host_pub: 'aa'.repeat(32), signal: 'https://relay.example' });
});

test('decodeRegistry falls back to the fetch origin when a record has no signal_url', () => {
  const e = entry(SECRET, 'm3', { v: 1, name: 'box', host_pub: 'cc'.repeat(32) });
  const out = decodeRegistry([e], SECRET, 'https://origin.example');
  assert.equal(out[0].signal, 'https://origin.example');
});

test('mergeMachines: local wins, discovered-only appended', () => {
  const local = [{ machine_id: 'm1', name: 'local-laptop' }];
  const disc = [{ machine_id: 'm1', name: 'reg-laptop' }, { machine_id: 'm2', name: 'desktop' }];
  const merged = mergeMachines(local, disc);
  assert.equal(merged.length, 2);
  assert.equal(merged[0].name, 'local-laptop', 'local entry wins');
  assert.equal(merged[1].machine_id, 'm2');
});

test('freshDevices flags only unseen machine_ids', () => {
  const disc = [{ machine_id: 'm1', name: 'a' }, { machine_id: 'm2', name: 'b' }];
  assert.deepEqual(freshDevices(['m1'], disc).map((m) => m.machine_id), ['m2']);
  assert.deepEqual(freshDevices(['m1', 'm2'], disc), []);
});
