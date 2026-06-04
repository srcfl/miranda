// web/test/owner.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils';
import { deriveOwnerKey } from '../src/identity/owner.js';

const here = dirname(fileURLToPath(import.meta.url));
const vector = JSON.parse(
  readFileSync(join(here, '..', '..', 'testdata', 'owner-derivation.json'), 'utf8'),
);

test('owner-key derivation matches the Go vector', () => {
  const { priv, pub } = deriveOwnerKey(hexToBytes(vector.prf_output));
  assert.equal(bytesToHex(priv), vector.owner_priv);
  assert.equal(bytesToHex(pub), vector.owner_pub);
});
