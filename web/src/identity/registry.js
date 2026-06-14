// web/src/identity/registry.js
// Mirrors go/internal/identity/registry.go: a wallet-derived symmetric key and a
// ChaCha20-Poly1305 (IETF, 12-byte nonce) record seal/open. Byte-identical to Go,
// gated by testdata/registry-vector.json. Only wallet-holders can derive K_reg;
// the relay only ever holds the opaque nonce||ciphertext||tag blob.
import { chacha20poly1305 } from '@noble/ciphers/chacha';
import { hkdf } from '@noble/hashes/hkdf';
import { sha256 } from '@noble/hashes/sha2';

const enc = new TextEncoder();
const SALT = enc.encode('miranda/registry/v1');
const INFO = enc.encode('aead-key');

// registryKey derives the 32-byte registry AEAD key from the wallet's 32-byte prf
// secret. K_reg = HKDF-SHA256(secret, salt='miranda/registry/v1', info='aead-key').
export function registryKey(secret) {
  return hkdf(sha256, secret, SALT, INFO, 32);
}

// sealRecord encrypts plaintext under key with machineID as AEAD associated data,
// returning nonce||ciphertext||tag. nonce must be 12 bytes.
export function sealRecord(key, nonce, plaintext, machineID) {
  const ct = chacha20poly1305(key, nonce, enc.encode(machineID)).encrypt(plaintext);
  const out = new Uint8Array(nonce.length + ct.length);
  out.set(nonce);
  out.set(ct, nonce.length);
  return out;
}

// openRecord reverses sealRecord. Throws on any failure — a forged/garbage blob,
// or a wrong machineID (AAD), fails here (never returns partial plaintext).
export function openRecord(key, blob, machineID) {
  return chacha20poly1305(key, blob.slice(0, 12), enc.encode(machineID)).decrypt(blob.slice(12));
}
