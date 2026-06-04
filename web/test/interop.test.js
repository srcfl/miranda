// web/test/interop.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { x25519 } from '@noble/curves/ed25519';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils';
import { HandshakeKK } from '../src/noise/noise-kk.js';

const here = dirname(fileURLToPath(import.meta.url));
const v = JSON.parse(
  readFileSync(join(here, '..', '..', 'testdata', 'kk-interop.json'), 'utf8'),
);

function pair(privHex) {
  const priv = hexToBytes(privHex);
  return { priv, pub: x25519.getPublicKey(priv) };
}

test('JS reproduces the Go KK wire bytes exactly', () => {
  const i = pair(v.init_static_priv);
  const r = pair(v.resp_static_priv);

  const initiator = new HandshakeKK({
    initiator: true,
    s: i,
    rs: r.pub,
    ephemeralPriv: hexToBytes(v.init_eph_priv),
  });
  const responder = new HandshakeKK({
    initiator: false,
    s: r,
    rs: i.pub,
    ephemeralPriv: hexToBytes(v.resp_eph_priv),
  });

  const msg0 = initiator.writeMessage(hexToBytes(v.payload0));
  assert.equal(bytesToHex(msg0), v.msg0, 'msg0 must match Go');

  const got0 = responder.readMessage(msg0);
  assert.equal(bytesToHex(got0), v.payload0, 'responder decrypts payload0');

  const msg1 = responder.writeMessage(new Uint8Array(0));
  assert.equal(bytesToHex(msg1), v.msg1, 'msg1 must match Go');
  initiator.readMessage(msg1);

  const ct = initiator.encrypt(hexToBytes(v.transport_plaintext));
  assert.equal(bytesToHex(ct), v.transport_ct, 'transport ciphertext must match Go');

  // And the JS responder decrypts the Go-produced transport ciphertext.
  const pt = responder.decrypt(hexToBytes(v.transport_ct));
  assert.equal(bytesToHex(pt), v.transport_plaintext, 'JS decrypts Go ciphertext');

  // Hardening: second record on the same send key (nonce 1) must match Go —
  // catches a counter-endianness divergence that nonce 0 alone would hide.
  const ct2 = initiator.encrypt(hexToBytes(v.transport2_plaintext));
  assert.equal(bytesToHex(ct2), v.transport2_ct, 'second-record (nonce 1) ciphertext must match Go');
  const pt2 = responder.decrypt(hexToBytes(v.transport2_ct));
  assert.equal(bytesToHex(pt2), v.transport2_plaintext, 'JS decrypts Go second record');

  // Hardening: reverse direction (responder->initiator, nonce 0) must match Go —
  // catches a split() send/recv cipherstate swap that the forward direction misses.
  const ctRev = responder.encrypt(hexToBytes(v.transport_rev_plaintext));
  assert.equal(bytesToHex(ctRev), v.transport_rev_ct, 'reverse-direction ciphertext must match Go');
  const ptRev = initiator.decrypt(hexToBytes(v.transport_rev_ct));
  assert.equal(bytesToHex(ptRev), v.transport_rev_plaintext, 'JS initiator decrypts Go reverse ciphertext');
});
