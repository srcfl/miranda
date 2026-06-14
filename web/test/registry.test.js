// web/test/registry.test.js — asserts the Go-written registry-vector.json vector
// and round-trip/tamper behaviour, byte-identical to go/internal/identity/registry.go.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { hexToBytes, bytesToHex } from '@noble/hashes/utils';
import { registryKey, sealRecord, openRecord } from '../src/identity/registry.js';

const here = dirname(fileURLToPath(import.meta.url));
const td = (f) => JSON.parse(readFileSync(join(here, '..', '..', 'testdata', f), 'utf8'));
const v = td('registry-vector.json');
const enc = new TextEncoder();

test('registryKey reproduces the Go vector key (deterministic HKDF)', () => {
  const key = registryKey(hexToBytes(v.secret));
  assert.equal(bytesToHex(key), v.key);
});

test('sealRecord is byte-identical to the Go vector blob', () => {
  const key = hexToBytes(v.key);
  const blob = sealRecord(key, hexToBytes(v.nonce), enc.encode(v.record), v.machine_id);
  assert.equal(bytesToHex(blob), v.blob);
});

test('openRecord round-trips the committed blob', () => {
  const key = hexToBytes(v.key);
  const opened = openRecord(key, hexToBytes(v.blob), v.machine_id);
  assert.equal(new TextDecoder().decode(opened), v.record);
});

test('seal then open round-trips a fresh record', () => {
  const key = registryKey(hexToBytes(v.secret));
  const nonce = new Uint8Array(12).fill(9);
  const pt = enc.encode('{"v":1,"name":"zap-garage"}');
  const blob = sealRecord(key, nonce, pt, 'deadbeefcafe0001');
  const back = openRecord(key, blob, 'deadbeefcafe0001');
  assert.deepEqual(back, pt);
});

test('openRecord throws on a wrong machine_id (AAD)', () => {
  const key = hexToBytes(v.key);
  assert.throws(() => openRecord(key, hexToBytes(v.blob), 'b1b2c3d4e5f60718'));
});

test('openRecord throws on a tampered ciphertext byte', () => {
  const key = hexToBytes(v.key);
  const blob = hexToBytes(v.blob);
  blob[12] ^= 0x01; // flip first ciphertext byte (past the 12-byte nonce)
  assert.throws(() => openRecord(key, blob, v.machine_id));
});
