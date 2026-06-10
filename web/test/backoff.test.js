import test from 'node:test';
import assert from 'node:assert';
import { backoff } from '../src/net/backoff.js';

test('full jitter stays within [0, ceil] and ceil grows then caps', () => {
  // random=1 returns the ceiling: base*factor**attempt, capped at cap.
  const max = (n) => backoff(n, { base: 500, factor: 2, cap: 10000, random: () => 1 });
  assert.equal(max(0), 500);
  assert.equal(max(1), 1000);
  assert.equal(max(2), 2000);
  assert.equal(max(5), 10000); // 500*32=16000 -> capped
  assert.equal(max(9), 10000); // stays capped (no overflow)
});

test('random=0 yields 0 (full jitter floor)', () => {
  assert.equal(backoff(3, { random: () => 0 }), 0);
});

test('result is always an integer within bounds for arbitrary random', () => {
  for (const r of [0.1, 0.37, 0.5, 0.99]) {
    const v = backoff(2, { base: 500, factor: 2, cap: 10000, random: () => r });
    assert.ok(Number.isInteger(v));
    assert.ok(v >= 0 && v <= 2000, `${v} out of [0,2000]`);
  }
});
