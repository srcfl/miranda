// web/test/wallet.test.js — asserts the Go-written wallet-derivation.json vector,
// mirroring how owner.test.js gates owner-derivation.json.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils';
import { deriveWallet, walletFromMnemonic } from '../src/identity/wallet.js';
import { deriveOwnerKey } from '../src/identity/owner.js';

const here = dirname(fileURLToPath(import.meta.url));
const vector = JSON.parse(
  readFileSync(join(here, '..', '..', 'testdata', 'wallet-derivation.json'), 'utf8'),
);

test('wallet derivation matches the Go vector', () => {
  const prf = hexToBytes(vector.prf_output);
  const w = deriveWallet(prf);
  assert.equal(w.mnemonic, vector.mnemonic);
  assert.equal(bytesToHex(w.seed), vector.seed);
  assert.equal(bytesToHex(w.priv), vector.wallet_priv);
  assert.equal(bytesToHex(w.pub), vector.wallet_pub);
  assert.equal(w.address, vector.address);
});

test('import from mnemonic reproduces the same wallet', () => {
  const w = walletFromMnemonic(vector.mnemonic);
  assert.equal(w.address, vector.address);
});

test('X25519 transport key is unchanged (independent of the wallet)', () => {
  const { pub } = deriveOwnerKey(hexToBytes(vector.prf_output));
  assert.equal(bytesToHex(pub), vector.owner_pub);
});
