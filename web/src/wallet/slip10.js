// web/src/wallet/slip10.js
// Mirrors go/internal/slip10/slip10.go: SLIP-0010 ed25519 HD derivation
// (hardened-only). Byte-identical to the Go side.
//
// master  = HMAC-SHA512("ed25519 seed", seed)          -> key=left32, chain=right32
// child_i = HMAC-SHA512(chain, 0x00 || key || ser32(i))   (i always hardened)
import { hmac } from '@noble/hashes/hmac';
import { sha512 } from '@noble/hashes/sha2';

const HARDENED = 0x80000000;
const SEED_KEY = new TextEncoder().encode('ed25519 seed');

function master(seed) {
  const I = hmac(sha512, SEED_KEY, seed);
  return { key: I.slice(0, 32), chain: I.slice(32) };
}

function child(node, index) {
  const data = new Uint8Array(37);
  data[0] = 0x00;
  data.set(node.key, 1);
  data[33] = (index >>> 24) & 0xff;
  data[34] = (index >>> 16) & 0xff;
  data[35] = (index >>> 8) & 0xff;
  data[36] = index & 0xff;
  const I = hmac(sha512, node.chain, data);
  return { key: I.slice(0, 32), chain: I.slice(32) };
}

// derivePath derives a node for a path like "m/44'/501'/0'/0'". Every segment
// must be hardened (ed25519 only supports hardened derivation). Returns
// { key, chain } as 32-byte Uint8Arrays.
export function derivePath(seed, path) {
  let node = master(seed);
  path = path.trim();
  if (path === 'm' || path === '') return node;
  if (!path.startsWith('m/')) throw new Error(`slip10: path must start with m/, got ${JSON.stringify(path)}`);
  for (const seg of path.slice(2).split('/')) {
    if (!seg.endsWith("'")) throw new Error(`slip10: ed25519 requires hardened indices, got ${JSON.stringify(seg)}`);
    const num = Number(seg.slice(0, -1));
    if (!Number.isInteger(num) || num < 0 || num >= HARDENED) throw new Error(`slip10: bad index ${JSON.stringify(seg)}`);
    node = child(node, (num + HARDENED) >>> 0);
  }
  return node;
}
