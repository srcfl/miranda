// web/test/binding.test.js — asserts the Go-written wallet-binding.json vector.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { hexToBytes } from '@noble/hashes/utils';
import { canonical, signBinding, verifyBinding, recordJSON } from '../src/identity/binding.js';
import { walletFromMnemonic } from '../src/identity/wallet.js';

const here = dirname(fileURLToPath(import.meta.url));
const td = (f) => JSON.parse(readFileSync(join(here, '..', '..', 'testdata', f), 'utf8'));
const v = td('wallet-binding.json');
const wv = td('wallet-derivation.json');

// Reconstruct the signing wallet from the committed mnemonic.
const wallet = walletFromMnemonic(wv.mnemonic);

test('canonical message matches the Go vector', () => {
  const c = canonical({ v: 1, wallet: v.wallet, device: v.device, x25519: v.x25519, ts: v.ts });
  assert.equal(c, v.canonical);
});

test('signature is byte-identical to the Go vector (deterministic ed25519)', () => {
  const sb = signBinding(wallet, v.device, v.x25519, v.ts);
  assert.equal(sb.sig, v.sig);
  assert.equal(recordJSON(sb), v.record);
});

test('verify accepts the committed record', () => {
  const sb = { v: 1, wallet: v.wallet, device: v.device, x25519: v.x25519, ts: v.ts, sig: v.sig };
  assert.equal(verifyBinding(sb), true);
});

test('verify rejects tampered fields', () => {
  const base = { v: 1, wallet: v.wallet, device: v.device, x25519: v.x25519, ts: v.ts, sig: v.sig };
  assert.equal(verifyBinding({ ...base, device: 'b1b2c3d4e5f60718' }), false);
  assert.equal(verifyBinding({ ...base, ts: v.ts + 1 }), false);
  assert.equal(verifyBinding({ ...base, sig: '1111111111' }), false);
});

test('signing rejects unsafe device / x25519', () => {
  for (const dev of ['a"b', 'a\\b', 'a b', 'a,b', '']) {
    assert.throws(() => signBinding(wallet, dev, v.x25519, v.ts));
  }
  assert.throws(() => signBinding(wallet, v.device, 'ZZZ', v.ts));
});
