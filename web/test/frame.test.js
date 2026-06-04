// web/test/frame.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  FRAME_DATA,
  FRAME_RESIZE,
  encodeData,
  encodeResize,
  decodeFrame,
  decodeResize,
} from '../src/noise/frame.js';

test('data frame round trip', () => {
  const enc = encodeData(new TextEncoder().encode('ls -la\n'));
  const { type, payload } = decodeFrame(enc);
  assert.equal(type, FRAME_DATA);
  assert.equal(new TextDecoder().decode(payload), 'ls -la\n');
});

test('resize frame round trip', () => {
  const enc = encodeResize(120, 40);
  const { type, payload } = decodeFrame(enc);
  assert.equal(type, FRAME_RESIZE);
  const { cols, rows } = decodeResize(payload);
  assert.equal(cols, 120);
  assert.equal(rows, 40);
});

test('decoding empty frame throws', () => {
  assert.throws(() => decodeFrame(new Uint8Array(0)));
});
