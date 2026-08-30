// Cross-language guest-grant vector: JS canonical bytes and verification must
// match go/internal/identity/grant.go exactly (v1 signs only in the CLI, so JS
// pins canonical + verify, not signing).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { canonical, verifyGrant, validAt, recordJSON } from '../src/identity/grant.js';

const here = dirname(fileURLToPath(import.meta.url));
const v = JSON.parse(readFileSync(join(here, '..', '..', 'testdata', 'grant.json'), 'utf8'));

const grant = {
  v: 1, owner: v.owner, machine: v.machine, guest: v.guest,
  scope: v.scope, mode: v.mode, nb: v.nb, na: v.na, gid: v.gid,
};
const signed = { ...grant, sig: v.sig };

test('canonical message matches the Go vector', () => {
  assert.equal(canonical(grant), v.canonical);
});

test('verify accepts the committed record and its JSON form', () => {
  assert.equal(verifyGrant(signed), true);
  assert.equal(recordJSON(signed), v.record);
  assert.equal(verifyGrant(JSON.parse(v.record)), true);
});

test('verify rejects tampered fields', () => {
  const cases = [
    { machine: 'b1b2c3d4e5f60718' },
    { guest: v.owner },
    { scope: 'other' },
    { mode: 'rw' },
    { nb: v.nb + 1 },
    { na: v.na - 1 },
    { gid: 'ffffffffffffffff' },
    { sig: '1111111111' },
  ];
  for (const patch of cases) {
    assert.equal(verifyGrant({ ...signed, ...patch }), false, JSON.stringify(patch));
  }
});

test('canonical rejects invalid grants', () => {
  const bad = [
    { ...grant, v: 2 },
    { ...grant, owner: 'a"b' },
    { ...grant, machine: 'm"1' },
    { ...grant, guest: 'zzz' },
    { ...grant, scope: 'ma"in' },
    { ...grant, scope: 'a'.repeat(65) },
    { ...grant, mode: 'admin' },
    { ...grant, nb: 0 },
    { ...grant, na: grant.nb },
    { ...grant, na: grant.nb + 24 * 3600 + 300 + 1 },
    { ...grant, gid: '0011' },
    { ...grant, gid: '00112233AABBCCDD' },
  ];
  for (const g of bad) assert.throws(() => canonical(g), undefined, JSON.stringify(g));
});

test('validAt covers exactly the window', () => {
  assert.equal(validAt(grant, grant.nb - 1), false);
  assert.equal(validAt(grant, grant.nb), true);
  assert.equal(validAt(grant, grant.na), true);
  assert.equal(validAt(grant, grant.na + 1), false);
});
