// web/src/pairing/sas.js
// Safety number ("SAS") from a Noise channel binding. Mirrors the Go reference
// (go/internal/sas/sas.go): SHA256("terminal-relay/sas/v1"||binding) first 8
// bytes, rendered as four 4-hex-digit groups.
import { sha256 } from '@noble/hashes/sha2';
import { bytesToHex } from '@noble/hashes/utils';

const enc = new TextEncoder();
export function safetyNumber(binding) {
  const h = sha256(concat(enc.encode('terminal-relay/sas/v1'), binding)).slice(0, 8);
  const hex = bytesToHex(h);
  return `${hex.slice(0, 4)}-${hex.slice(4, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}`;
}
function concat(a, b) {
  const o = new Uint8Array(a.length + b.length);
  o.set(a);
  o.set(b, a.length);
  return o;
}
