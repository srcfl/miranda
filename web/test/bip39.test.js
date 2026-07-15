// web/test/bip39.test.js — mirrors go/internal/bip39/bip39_test.go.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils';
import { entropyToMnemonic, mnemonicToEntropy } from '../src/wallet/bip39.js';
import { wordlist } from '../src/wallet/wordlist.js';

test('wordlist integrity', () => {
  assert.equal(wordlist.length, 2048);
  assert.equal(wordlist[0], 'abandon');
  assert.equal(wordlist[2047], 'zoo');
});

test('zero-entropy anchors', () => {
  assert.equal(
    entropyToMnemonic(new Uint8Array(16)),
    'abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about',
  );
  assert.equal(entropyToMnemonic(new Uint8Array(32)), 'abandon '.repeat(23) + 'art');
});

test('prf -> recovery mnemonic external anchor', () => {
  const prf = hexToBytes('00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff');
  const wantMnemonic =
    'abandon math mimic master filter design carbon crystal rookie group knife wrap absurd much snack melt grid rough chapter fever rubber humble room trophy';
  const m = entropyToMnemonic(prf);
  assert.equal(m, wantMnemonic);
});

test('entropy bounds', () => {
  for (const n of [0, 12, 15, 17, 33]) {
    assert.throws(() => entropyToMnemonic(new Uint8Array(n)));
  }
});

test('mnemonicToEntropy round-trips and validates checksum', () => {
  const prf = hexToBytes('00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff');
  const m = entropyToMnemonic(prf);
  assert.equal(bytesToHex(mnemonicToEntropy(m)), bytesToHex(prf));
  for (const n of [16, 32]) {
    const mm = entropyToMnemonic(new Uint8Array(n));
    assert.equal(mnemonicToEntropy(mm).length, n);
  }
  // unknown word, bad checksum, bad length all throw.
  assert.throws(() => mnemonicToEntropy('abandon abandon notaword'));
  const good = entropyToMnemonic(new Uint8Array(32)); // ...abandon art
  assert.throws(() => mnemonicToEntropy(good.replace(/art$/, 'zoo')));
  assert.throws(() => mnemonicToEntropy('abandon abandon abandon'));
});
