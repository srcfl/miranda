// web/test/base58.test.js — mirrors go/internal/base58/base58_test.go.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils';
import { encode, decode } from '../src/wallet/base58.js';

const vectors = [
  ['', ''],
  ['61', '2g'],
  ['626262', 'a3gV'],
  ['73696d706c792061206c6f6e6720737472696e67', '2cFupjhnEsSn59qHXstmK2ffpLv2'],
  ['00000000000000000000', '1111111111'],
  // 32-byte Solana address anchor (B1 wallet pubkey -> address).
  ['a3d4ab895f8bc2990f27e64b4ee2abcb9396dc132ead962a1ba6664fd938ec41', 'C2XYPfExbj6azVqYLWeUphzsdKK2dQ53dm83Brd3THmS'],
];

test('base58 encode matches vectors', () => {
  for (const [h, enc] of vectors) assert.equal(encode(hexToBytes(h)), enc);
});

test('base58 decode matches vectors', () => {
  for (const [h, enc] of vectors) assert.equal(bytesToHex(decode(enc)), h);
});

test('base58 round-trips', () => {
  for (const h of ['00', 'ff', 'deadbeef', '00ff00ff00', '0102030405060708090a']) {
    assert.equal(bytesToHex(decode(encode(hexToBytes(h)))), h);
  }
});

test('base58 decode rejects invalid characters', () => {
  for (const bad of ['0', 'O', 'I', 'l', 'abc!', '  ']) {
    assert.throws(() => decode(bad));
  }
});
