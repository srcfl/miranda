import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { hexToBytes } from '@noble/hashes/utils';
import { deriveSigner } from '../src/identity/signer.js';
import {
  filterRevoked, loadRevocations, recordRevocation, revocationChallenge,
  signRevocation, verifyRevocation,
} from '../src/revocations.js';

const signer = deriveSigner(hexToBytes('00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff'));
const here = dirname(fileURLToPath(import.meta.url));
const vector = JSON.parse(readFileSync(join(here, '..', '..', 'testdata', 'revocation.json'), 'utf8'));

beforeEach(() => {
  const values = new Map();
  globalThis.localStorage = {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, String(value)),
  };
});

test('revocation challenge is byte-identical to Go', () => {
  assert.equal(new TextDecoder().decode(revocationChallenge('machine-1', 1700000000)),
    'miranda/revocation/v1\nmachine-1\n1700000000');
});

test('signed revocation round-trips and tampering fails', () => {
  const record = signRevocation(signer, 'machine-1', 1700000000);
  assert.equal(verifyRevocation(record), true);
  assert.equal(verifyRevocation({ ...record, machine_id: 'machine-2' }), false);
  assert.equal(verifyRevocation({ ...record, ts: record.ts + 1 }), false);
});

test('signature is byte-identical to the committed Go vector', () => {
  const vectorSigner = deriveSigner(hexToBytes(vector.root));
  const record = signRevocation(vectorSigner, vector.machine_id, vector.ts);
  assert.deepEqual(record, {
    v: vector.v,
    owner_id: vector.owner_id,
    machine_id: vector.machine_id,
    ts: vector.ts,
    signature: vector.signature,
  });
});

test('verified local tombstones filter machines', () => {
  const record = signRevocation(signer, 'machine-1', 1700000000);
  recordRevocation(record);
  recordRevocation(record);
  const records = loadRevocations(signer.address);
  assert.equal(records.length, 1);
  assert.deepEqual(filterRevoked([{ machine_id: 'machine-1' }, { machine_id: 'machine-2' }], records), [{ machine_id: 'machine-2' }]);
});

test('corrupt local tombstone store fails closed', () => {
  const record = signRevocation(signer, 'machine-1', 1700000000);
  recordRevocation(record);
  localStorage.setItem('mir_revocations_v1', JSON.stringify([{ ...record, machine_id: 'machine-2' }]));
  assert.throws(() => loadRevocations(signer.address), /signature verification/);
});
