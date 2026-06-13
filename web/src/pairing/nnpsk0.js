// web/src/pairing/nnpsk0.js
// Noise_NNpsk0_25519_ChaChaPoly_SHA256, hand-rolled from the Noise spec on
// @noble primitives. Must interoperate byte-for-byte with the Go flynn/noise
// reference (go/internal/pairing/pairing.go).
//
// Pattern NNpsk0:
//   -> psk, e
//   <- e, ee
// PresharedKeyPlacement 0 prepends the PSK token to the first message. In PSK
// mode the `e` token calls MixKey IN ADDITION to MixHash (Noise spec rule).
import { x25519 } from '@noble/curves/ed25519';
import { chacha20poly1305 } from '@noble/ciphers/chacha';
import { sha256 } from '@noble/hashes/sha2';
import { hmac } from '@noble/hashes/hmac';
import { pskFromToken } from './code.js';
import { signAuth, verifyAuth } from '../identity/auth.js';

const PROTOCOL_NAME = 'Noise_NNpsk0_25519_ChaChaPoly_SHA256';
const PROLOGUE = new TextEncoder().encode('terminal-relay/pair/v1');

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

// Noise HKDF with `num` outputs; chaining-key as salt, 0x01/0x02/0x03 counter.
function hkdf(chainingKey, ikm, num) {
  const tempKey = hmac(sha256, chainingKey, ikm);
  const o1 = hmac(sha256, tempKey, Uint8Array.of(0x01));
  if (num === 1) return [o1];
  const o2 = hmac(sha256, tempKey, concat(o1, Uint8Array.of(0x02)));
  if (num === 2) return [o1, o2];
  const o3 = hmac(sha256, tempKey, concat(o2, Uint8Array.of(0x03)));
  return [o1, o2, o3];
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
  hasKey() {
    return this.k !== null;
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
    let h;
    if (name.length <= 32) {
      h = new Uint8Array(32);
      h.set(name);
    } else {
      h = sha256(name); // name longer than hash size => hash it
    }
    this.h = h;
    this.ck = h.slice();
    this.cs = new CipherState();
  }
  mixKey(ikm) {
    const [ck, tempK] = hkdf(this.ck, ikm, 2);
    this.ck = ck;
    this.cs.initializeKey(tempK);
  }
  mixHash(data) {
    this.h = sha256(concat(this.h, data));
  }
  mixKeyAndHash(ikm) {
    const [ck, temp, tempK] = hkdf(this.ck, ikm, 3);
    this.ck = ck;
    this.mixHash(temp);
    this.cs.initializeKey(tempK);
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
    const [t1, t2] = hkdf(this.ck, new Uint8Array(0), 2);
    const c1 = new CipherState();
    c1.initializeKey(t1);
    const c2 = new CipherState();
    c2.initializeKey(t2);
    return [c1, c2];
  }
}

export class HandshakeNNpsk0 {
  // psk = 32-byte preshared key (Uint8Array).
  // ephemeralPriv optional, for deterministic test vectors.
  constructor({ initiator, psk, ephemeralPriv = null }) {
    this.initiator = initiator;
    this.psk = psk;
    this.ephemeralPriv = ephemeralPriv;
    this.e = null;
    this.re = null;
    this.msgIndex = 0;
    this.done = false;
    this.sendCs = null;
    this.recvCs = null;

    this.ss = new SymmetricState();
    this.ss.mixHash(PROLOGUE);
    // NN has no pre-messages.
  }

  _genEphemeral() {
    const priv = this.ephemeralPriv || x25519.utils.randomPrivateKey();
    this.e = { priv, pub: x25519.getPublicKey(priv) };
  }

  _dh(priv, pub) {
    return x25519.getSharedSecret(priv, pub);
  }

  // ee = DH(init_e, resp_e) from either perspective.
  _dhToken(t) {
    switch (t) {
      case 'ee':
        return this._dh(this.e.priv, this.re);
      default:
        throw new Error('unknown token ' + t);
    }
  }

  // NNpsk0 message patterns (psk placement 0):
  //   msg0: psk, e
  //   msg1: e, ee
  _tokens() {
    return this.msgIndex === 0 ? ['psk', 'e'] : ['e', 'ee'];
  }

  writeMessage(payload = new Uint8Array(0)) {
    let out = new Uint8Array(0);
    for (const t of this._tokens()) {
      if (t === 'psk') {
        this.ss.mixKeyAndHash(this.psk);
      } else if (t === 'e') {
        this._genEphemeral();
        out = concat(out, this.e.pub);
        this.ss.mixHash(this.e.pub);
        this.ss.mixKey(this.e.pub); // PSK-mode: e also MixKey
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
      if (t === 'psk') {
        this.ss.mixKeyAndHash(this.psk);
      } else if (t === 'e') {
        this.re = message.slice(off, off + 32);
        off += 32;
        this.ss.mixHash(this.re);
        this.ss.mixKey(this.re); // PSK-mode: e also MixKey
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
      const [c1, c2] = this.ss.split();
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

  // Channel binding = the symmetric-state hash h after the handshake.
  binding() {
    return this.ss.h;
  }
}

// runInitiator (client): sends PairClaim{wallet} in msg1, reads agent info from
// msg2, then proves wallet control with msg3 = signAuth(channelBinding).
// Returns { info, binding }. `ephemeralPriv` is optional (deterministic tests).
export async function runInitiator(mc, token, wallet, ephemeralPriv = null) {
  const hs = new HandshakeNNpsk0({ initiator: true, psk: pskFromToken(token), ephemeralPriv });
  const msg1 = hs.writeMessage(new TextEncoder().encode(JSON.stringify({ wallet: wallet.address })));
  await mc.send(msg1);
  const msg2 = await mc.recv();
  const payload = hs.readMessage(msg2);
  const info = JSON.parse(new TextDecoder().decode(payload));
  const binding = hs.binding();
  await mc.send(signAuth(wallet, binding)); // msg3, raw 64-byte sig
  return { info, binding };
}

// runResponder (agent): reads PairClaim{wallet} from msg1, sends info in msg2,
// then verifies msg3 (wallet auth over the channel binding). Returns
// { wallet, binding }. `ephemeralPriv` is optional (deterministic tests).
export async function runResponder(mc, token, info, ephemeralPriv = null) {
  const hs = new HandshakeNNpsk0({ initiator: false, psk: pskFromToken(token), ephemeralPriv });
  const msg1 = await mc.recv();
  const claim = JSON.parse(new TextDecoder().decode(hs.readMessage(msg1)));
  const infoJSON = new TextEncoder().encode(JSON.stringify(info));
  const msg2 = hs.writeMessage(infoJSON);
  await mc.send(msg2);
  const binding = hs.binding();
  const sig = await mc.recv(); // msg3
  if (!verifyAuth(claim.wallet, binding, sig)) throw new Error('pairing: wallet auth failed');
  return { wallet: claim.wallet, binding };
}
