// web/test/noise-kk.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { x25519 } from '@noble/curves/ed25519';
import { HandshakeKK } from '../src/noise/noise-kk.js';

function staticPair(seedByte) {
  const priv = new Uint8Array(32).fill(seedByte);
  const pub = x25519.getPublicKey(priv);
  return { priv, pub };
}

test('KK handshake completes and transports both directions', () => {
  const i = staticPair(1);
  const r = staticPair(2);

  const initiator = new HandshakeKK({ initiator: true, s: i, rs: r.pub });
  const responder = new HandshakeKK({ initiator: false, s: r, rs: i.pub });

  const msg0 = initiator.writeMessage(new TextEncoder().encode('hi'));
  const got0 = responder.readMessage(msg0);
  assert.equal(new TextDecoder().decode(got0), 'hi');

  const msg1 = responder.writeMessage(new Uint8Array(0));
  initiator.readMessage(msg1);

  assert.ok(initiator.done && responder.done);

  const ct = initiator.encrypt(new TextEncoder().encode('i->r'));
  assert.equal(new TextDecoder().decode(responder.decrypt(ct)), 'i->r');

  const ct2 = responder.encrypt(new TextEncoder().encode('r->i'));
  assert.equal(new TextDecoder().decode(initiator.decrypt(ct2)), 'r->i');
});
