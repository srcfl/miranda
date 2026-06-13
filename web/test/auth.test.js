// web/test/auth.test.js — round-trip + tamper tests for the wallet auth proof
// (web/src/identity/auth.js), mirroring go/internal/identity/auth_test.go. The
// cross-impl byte check against the pairing vector (msg3) lives in the interop
// test, where the channel binding the signature commits to is available.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { hexToBytes } from '@noble/hashes/utils';
import { deriveWallet } from '../src/identity/wallet.js';
import { signAuth, verifyAuth } from '../src/identity/auth.js';

const wallet = deriveWallet(hexToBytes('00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff'));

test('signAuth round-trips through verifyAuth', () => {
  const challenge = crypto.getRandomValues(new Uint8Array(32));
  const sig = signAuth(wallet, challenge);
  assert.equal(sig.length, 64);
  assert.equal(verifyAuth(wallet.address, challenge, sig), true);
});

test('a tampered challenge does not verify', () => {
  const challenge = crypto.getRandomValues(new Uint8Array(32));
  const sig = signAuth(wallet, challenge);
  const bad = challenge.slice();
  bad[0] ^= 0xff;
  assert.equal(verifyAuth(wallet.address, bad, sig), false);
});

test('a tampered signature does not verify', () => {
  const challenge = crypto.getRandomValues(new Uint8Array(32));
  const sig = signAuth(wallet, challenge);
  const bad = sig.slice();
  bad[0] ^= 0xff;
  assert.equal(verifyAuth(wallet.address, challenge, bad), false);
});

test('the wrong wallet does not verify', () => {
  const challenge = crypto.getRandomValues(new Uint8Array(32));
  const sig = signAuth(wallet, challenge);
  const other = deriveWallet(hexToBytes('ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'));
  assert.equal(verifyAuth(other.address, challenge, sig), false);
});

test('a bad wallet address or wrong-length sig never throws, returns false', () => {
  const challenge = crypto.getRandomValues(new Uint8Array(32));
  assert.equal(verifyAuth('not-base58-0OIl', challenge, new Uint8Array(64)), false);
  assert.equal(verifyAuth(wallet.address, challenge, new Uint8Array(10)), false);
});
