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

// base64url-encode an arbitrary payload object the way encodeCode does,
// so we can craft malformed pairing codes that exercise decodeCode's validation.
function encodePayload(obj) {
  return btoa(JSON.stringify(obj)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

test('decodeCode rejects a malformed token like Go (fail closed)', () => {
  const bad = [
    'z'.repeat(32),                  // 32 non-hex chars (Go: bad token; JS must not zero-fill)
    'ab'.repeat(8),                  // 8-byte token (wrong length)
    '',                              // empty token
    'abc',                           // odd-length hex
    'ab'.repeat(32),                 // 32-byte token (wrong length)
  ];
  for (const t of bad) {
    assert.throws(
      () => decodeCode(encodePayload({ s: 'https://relay.example', t })),
      /bad pairing code token/,
      `expected reject for token ${JSON.stringify(t)}`,
    );
  }
});

test('decodeCode rejects a missing or non-string token field', () => {
  assert.throws(() => decodeCode(encodePayload({ s: 'https://relay.example' })), /bad pairing code token/);
  assert.throws(() => decodeCode(encodePayload({ s: 'https://relay.example', t: 123 })), /bad pairing code token/);
  assert.throws(() => decodeCode(encodePayload({ s: 'https://relay.example', t: null })), /bad pairing code token/);
});

test('decodeCode rejects an unsafe or malformed signal URL', () => {
  const t = '00'.repeat(16); // a valid 16-byte token so we reach the URL check
  const bad = [
    'javascript:alert(1)',                 // script scheme
    'data:text/html,evil',                 // data scheme
    'file:///etc/passwd',                  // file scheme
    'http://evil.example',                 // plain-http to a public host (mixed content)
    'ftp://relay.example',                 // wrong scheme
    'not a url',                           // unparseable
    'wss://relay.example',                 // ws scheme is derived from http(s), not accepted raw
  ];
  for (const s of bad) {
    assert.throws(
      () => decodeCode(encodePayload({ s, t })),
      /bad pairing code signal URL/,
      `expected reject for signal URL ${JSON.stringify(s)}`,
    );
  }
});

test('decodeCode accepts https and http://localhost relay URLs', () => {
  const t = '00'.repeat(16);
  for (const s of ['https://relay.sourceful-labs.net', 'https://relay.example:8443', 'http://localhost:8080', 'http://127.0.0.1:9000']) {
    assert.equal(decodeCode(encodePayload({ s, t })).signalURL, s);
  }
});

test('decodeCode rejects a missing signal URL', () => {
  const t = '00'.repeat(16);
  assert.throws(() => decodeCode(encodePayload({ t })), /bad pairing code signal URL/);
});
