// web/src/identity/binding.js
// Mirrors go/internal/identity/binding.go: a wallet-signed authorization of a
// device's X25519 transport key. Byte-identical canonical message and signature.
import { ed25519 } from '@noble/curves/ed25519';
import { encode as b58encode, decode as b58decode } from '../wallet/base58.js';

const DOMAIN = 'miranda/binding/v1';
const DEVICE_RE = /^[0-9A-Za-z._-]+$/;
const HEX64_RE = /^[0-9a-f]{64}$/;
const enc = new TextEncoder();

function validate(b) {
  if (b.v !== 1) throw new Error(`binding: unsupported version ${b.v}`);
  let pk;
  try {
    pk = b58decode(b.wallet);
  } catch {
    throw new Error('binding: wallet is not base58');
  }
  if (pk.length !== 32) throw new Error('binding: wallet is not a 32-byte key');
  if (typeof b.device !== 'string' || !DEVICE_RE.test(b.device)) throw new Error('binding: device has unsafe characters');
  if (typeof b.x25519 !== 'string' || !HEX64_RE.test(b.x25519)) throw new Error('binding: x25519 must be 64 lowercase hex chars');
  if (!Number.isInteger(b.ts) || b.ts <= 0) throw new Error('binding: ts must be a positive integer');
}

// canonical builds the byte-identical signing message: fixed field order, no
// whitespace. Validated fields need no JSON escaping, so this matches Go's
// hand-built canonical string (not JSON.stringify, to avoid escaping divergence).
export function canonical(b) {
  validate(b);
  return `{"v":${b.v},"wallet":"${b.wallet}","device":"${b.device}","x25519":"${b.x25519}","ts":${b.ts}}`;
}

// signBinding signs a binding authorizing device + x25519 under the given wallet
// ({ address, priv }). Returns the wire record { v, wallet, device, x25519, ts, sig }.
export function signBinding(wallet, device, x25519, ts) {
  const b = { v: 1, wallet: wallet.address, device, x25519, ts };
  const canon = canonical(b);
  const sig = ed25519.sign(enc.encode(DOMAIN + canon), wallet.priv);
  return { ...b, sig: b58encode(sig) };
}

// verifyBinding checks the signature against the wallet pubkey embedded in the
// record. Returns true iff valid; never throws.
export function verifyBinding(sb) {
  let canon;
  try {
    canon = canonical(sb);
  } catch {
    return false;
  }
  let pub, sig;
  try {
    pub = b58decode(sb.wallet);
    sig = b58decode(sb.sig);
  } catch {
    return false;
  }
  if (pub.length !== 32 || sig.length !== 64) return false;
  return ed25519.verify(sig, enc.encode(DOMAIN + canon), pub);
}

// recordJSON renders the wire record deterministically (canonical + ,"sig":"…").
export function recordJSON(sb) {
  const canon = canonical(sb);
  return canon.slice(0, -1) + `,"sig":"${sb.sig}"}`;
}
