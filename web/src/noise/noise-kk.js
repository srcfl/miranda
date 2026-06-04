// web/src/noise/noise-kk.js
// Noise_KK_25519_ChaChaPoly_SHA256, implemented from the Noise spec on @noble
// primitives. Must interoperate byte-for-byte with the Go flynn/noise wrapper.
import { x25519 } from '@noble/curves/ed25519';
import { chacha20poly1305 } from '@noble/ciphers/chacha';
import { sha256 } from '@noble/hashes/sha2';
import { hmac } from '@noble/hashes/hmac';

const PROTOCOL_NAME = 'Noise_KK_25519_ChaChaPoly_SHA256'; // exactly 32 bytes
const PROLOGUE = new TextEncoder().encode('terminal-relay/v1');

function concat(...arrs) {
  let len = 0;
  for (const a of arrs) len += a.length;
  const out = new Uint8Array(len);
  let off = 0;
  for (const a of arrs) {
    out.set(a, off);
    off += a.length;
  }
  return out;
}

// Noise HKDF: chaining-key as salt, 0x01/0x02 counter chaining.
function hkdf2(chainingKey, ikm) {
  const tempKey = hmac(sha256, chainingKey, ikm);
  const o1 = hmac(sha256, tempKey, Uint8Array.of(0x01));
  const o2 = hmac(sha256, tempKey, concat(o1, Uint8Array.of(0x02)));
  return [o1, o2];
}

// 12-byte nonce: 4 zero bytes ++ 8-byte little-endian counter.
function nonceBytes(n) {
  const out = new Uint8Array(12);
  new DataView(out.buffer).setBigUint64(4, BigInt(n), true);
  return out;
}

class CipherState {
  constructor() {
    this.k = null;
    this.n = 0;
  }
  initializeKey(key) {
    this.k = key;
    this.n = 0;
  }
  encryptWithAd(ad, plaintext) {
    if (!this.k) return plaintext;
    const ct = chacha20poly1305(this.k, nonceBytes(this.n), ad).encrypt(plaintext);
    this.n++;
    return ct;
  }
  decryptWithAd(ad, ciphertext) {
    if (!this.k) return ciphertext;
    const pt = chacha20poly1305(this.k, nonceBytes(this.n), ad).decrypt(ciphertext);
    this.n++;
    return pt;
  }
}

class SymmetricState {
  constructor() {
    const name = new TextEncoder().encode(PROTOCOL_NAME);
    const h = new Uint8Array(32);
    h.set(name); // length is exactly 32, no SHA needed
    this.h = h;
    this.ck = h.slice();
    this.cs = new CipherState();
  }
  mixKey(ikm) {
    const [ck, tempK] = hkdf2(this.ck, ikm);
    this.ck = ck;
    this.cs.initializeKey(tempK);
  }
  mixHash(data) {
    this.h = sha256(concat(this.h, data));
  }
  encryptAndHash(plaintext) {
    const ct = this.cs.encryptWithAd(this.h, plaintext);
    this.mixHash(ct);
    return ct;
  }
  decryptAndHash(ciphertext) {
    const pt = this.cs.decryptWithAd(this.h, ciphertext);
    this.mixHash(ciphertext);
    return pt;
  }
  split() {
    const [t1, t2] = hkdf2(this.ck, new Uint8Array(0));
    const c1 = new CipherState();
    c1.initializeKey(t1);
    const c2 = new CipherState();
    c2.initializeKey(t2);
    return [c1, c2];
  }
}

export class HandshakeKK {
  // s = { priv, pub } local static; rs = peer static pub (Uint8Array).
  // ephemeralPriv optional, for deterministic test vectors.
  constructor({ initiator, s, rs, ephemeralPriv = null }) {
    this.initiator = initiator;
    this.s = s;
    this.rs = rs;
    this.ephemeralPriv = ephemeralPriv;
    this.e = null;
    this.re = null;
    this.msgIndex = 0;
    this.done = false;
    this.sendCs = null;
    this.recvCs = null;

    this.ss = new SymmetricState();
    this.ss.mixHash(PROLOGUE);
    // KK pre-messages: "-> s" (initiator static) then "<- s" (responder static).
    const initStatic = initiator ? s.pub : rs;
    const respStatic = initiator ? rs : s.pub;
    this.ss.mixHash(initStatic);
    this.ss.mixHash(respStatic);
  }

  _genEphemeral() {
    const priv = this.ephemeralPriv || x25519.utils.randomPrivateKey();
    this.e = { priv, pub: x25519.getPublicKey(priv) };
  }

  _dh(priv, pub) {
    return x25519.getSharedSecret(priv, pub);
  }

  // Token DH rules (perspective-resolved):
  //   ee = DH(init_e, resp_e); ss = DH(init_s, resp_s)
  //   es = DH(init_e, resp_s); se = DH(init_s, resp_e)
  _dhToken(t) {
    const I = this.initiator;
    switch (t) {
      case 'ee':
        return this._dh(this.e.priv, this.re);
      case 'ss':
        return this._dh(this.s.priv, this.rs);
      case 'es':
        return I ? this._dh(this.e.priv, this.rs) : this._dh(this.s.priv, this.re);
      case 'se':
        return I ? this._dh(this.s.priv, this.re) : this._dh(this.e.priv, this.rs);
      default:
        throw new Error('unknown token ' + t);
    }
  }

  _tokens() {
    return this.msgIndex === 0 ? ['e', 'es', 'ss'] : ['e', 'ee', 'se'];
  }

  writeMessage(payload = new Uint8Array(0)) {
    let out = new Uint8Array(0);
    for (const t of this._tokens()) {
      if (t === 'e') {
        this._genEphemeral();
        out = concat(out, this.e.pub);
        this.ss.mixHash(this.e.pub);
      } else {
        this.ss.mixKey(this._dhToken(t));
      }
    }
    out = concat(out, this.ss.encryptAndHash(payload));
    this._afterMessage();
    return out;
  }

  readMessage(message) {
    let off = 0;
    for (const t of this._tokens()) {
      if (t === 'e') {
        this.re = message.slice(off, off + 32);
        off += 32;
        this.ss.mixHash(this.re);
      } else {
        this.ss.mixKey(this._dhToken(t));
      }
    }
    const payload = this.ss.decryptAndHash(message.slice(off));
    this._afterMessage();
    return payload;
  }

  _afterMessage() {
    this.msgIndex++;
    if (this.msgIndex === 2) {
      const [c1, c2] = this.ss.split(); // c1 = init->resp, c2 = resp->init
      if (this.initiator) {
        this.sendCs = c1;
        this.recvCs = c2;
      } else {
        this.sendCs = c2;
        this.recvCs = c1;
      }
      this.done = true;
    }
  }

  encrypt(plaintext) {
    return this.sendCs.encryptWithAd(new Uint8Array(0), plaintext);
  }

  decrypt(ciphertext) {
    return this.recvCs.decryptWithAd(new Uint8Array(0), ciphertext);
  }
}
