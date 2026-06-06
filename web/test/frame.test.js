// web/test/frame.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  FRAME_DATA,
  FRAME_RESIZE,
  FRAME_WINDOWS,
  FRAME_CONTROL,
  encodeData,
  encodeResize,
  encodeWindows,
  encodeControl,
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

test('windows frame round trip (byte-mirrors Go)', () => {
  const j = new TextEncoder().encode('{"v":1,"active":"@0","win":[{"id":"@0","i":0,"n":"main"}]}');
  const enc = encodeWindows(j);
  assert.equal(enc[0], FRAME_WINDOWS);
  assert.equal(enc[0], 0x04);
  const { type, payload } = decodeFrame(enc);
  assert.equal(type, FRAME_WINDOWS);
  assert.deepEqual(payload, j);
});

test('control frame round trip (byte-mirrors Go)', () => {
  const j = new TextEncoder().encode('{"a":"select-window","t":"@3"}');
  const enc = encodeControl(j);
  assert.equal(enc[0], FRAME_CONTROL);
  assert.equal(enc[0], 0x05);
  const { type, payload } = decodeFrame(enc);
  assert.equal(type, FRAME_CONTROL);
  assert.deepEqual(payload, j);
});

test('decoding empty frame throws', () => {
  assert.throws(() => decodeFrame(new Uint8Array(0)));
});
