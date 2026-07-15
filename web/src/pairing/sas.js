// web/src/pairing/sas.js
// Safety number ("SAS") from a Noise channel binding. Mirrors the Go reference
// (go/internal/sas/sas.go): first 12 bytes of the v2 domain hash, rendered as
// six 4-hex-digit groups (96 bits).
import { sha256 } from '@noble/hashes/sha2';
import { bytesToHex } from '@noble/hashes/utils';

const enc = new TextEncoder();
export function safetyNumber(binding) {
  const h = sha256(concat(enc.encode('miranda/sas/v2'), binding)).slice(0, 12);
  const hex = bytesToHex(h);
  return `${hex.slice(0, 4)}-${hex.slice(4, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20, 24)}`;
}
function concat(a, b) {
  const o = new Uint8Array(a.length + b.length);
  o.set(a);
  o.set(b, a.length);
  return o;
}
