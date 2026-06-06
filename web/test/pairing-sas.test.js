// web/test/pairing-sas.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { safetyNumber } from '../src/pairing/sas.js';

test('safety number is 4 groups of 4 hex, deterministic', () => {
  const a = safetyNumber(new Uint8Array([1, 2, 3, 4, 5]));
  assert.match(a, /^[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}$/);
  assert.equal(a, safetyNumber(new Uint8Array([1, 2, 3, 4, 5])));
});
