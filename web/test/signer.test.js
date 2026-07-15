import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils';
import { deriveSigner, signerFromMnemonic } from '../src/identity/signer.js';
import { deriveOwnerKey } from '../src/identity/owner.js';

const here = dirname(fileURLToPath(import.meta.url));
const vector = JSON.parse(readFileSync(join(here, '..', '..', 'testdata', 'identity-derivation.json'), 'utf8'));

test('neutral signer derivation matches the Go vector', () => {
  const signer = deriveSigner(hexToBytes(vector.root));
  assert.equal(signer.mnemonic, vector.mnemonic);
  assert.equal(bytesToHex(signer.priv), vector.signer_priv);
  assert.equal(bytesToHex(signer.pub), vector.signer_pub);
  assert.equal(signer.address, vector.owner_id);
});

test('recovery phrase reproduces the same Miranda identity', () => {
  assert.equal(signerFromMnemonic(vector.mnemonic).address, vector.owner_id);
});

test('X25519 transport derivation remains independently domain-separated', () => {
  assert.equal(bytesToHex(deriveOwnerKey(hexToBytes(vector.root)).pub), vector.transport_pub);
});
