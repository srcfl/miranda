// web/test/pairing-code.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils';
import { roomID, pskFromToken, encodeCode, decodeCode } from '../src/pairing/code.js';

const here = dirname(fileURLToPath(import.meta.url));
const v = JSON.parse(readFileSync(join(here, '..', '..', 'testdata', 'pair-interop.json'), 'utf8'));

test('roomID and psk match the Go vector', () => {
  const tok = hexToBytes(v.token);
  assert.equal(roomID(tok), v.room_id);
  assert.equal(bytesToHex(pskFromToken(tok)), v.psk);
});

test('pairing code round-trips', () => {
  const code = encodeCode('https://relay.sourceful-labs.net', hexToBytes(v.token));
  const { signalURL, token } = decodeCode(code);
  assert.equal(signalURL, 'https://relay.sourceful-labs.net');
  assert.equal(bytesToHex(token), v.token);
});
