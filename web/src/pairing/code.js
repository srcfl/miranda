// web/src/pairing/code.js
// Pairing-code / roomID / psk derivation for terminal-relay browser pairing.
// Must match the Go reference (go/internal/pairing/code.go) byte-for-byte.
import { sha256 } from '@noble/hashes/sha2';
import { bytesToHex } from '@noble/hashes/utils';

const enc = new TextEncoder();
function concat(a, b) {
  const o = new Uint8Array(a.length + b.length);
  o.set(a);
  o.set(b, a.length);
  return o;
}

export function pskFromToken(token) {
  return sha256(concat(enc.encode('terminal-relay/pair/psk'), token));
}
export function roomID(token) {
  return bytesToHex(sha256(concat(enc.encode('terminal-relay/pair/room'), token)).slice(0, 16));
}
export function encodeCode(signalURL, token) {
  const json = JSON.stringify({ s: signalURL, t: bytesToHex(token) });
  return btoa(json).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, ''); // base64url, no pad
}
export function decodeCode(code) {
  const b64 = code.replace(/-/g, '+').replace(/_/g, '/');
  const json = atob(b64);
  const p = JSON.parse(json);
  // Validate the token exactly like the Go reference (DecodeCode in code.go):
  // hex.DecodeString + len(token) != 16 -> "bad pairing code token".
  // A token of exactly 16 bytes is 32 hex chars; anything else fails closed
  // so a JS browser never silently derives a wrong/all-zeros psk + roomID.
  if (typeof p.t !== 'string' || !/^[0-9a-fA-F]{32}$/.test(p.t)) {
    throw new Error('bad pairing code token');
  }
  return { signalURL: p.s, token: hexToBytesLocal(p.t) };
}
function hexToBytesLocal(h) {
  const out = new Uint8Array(h.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(h.slice(i * 2, i * 2 + 2), 16);
  return out;
}
