// web/src/identity/grant.js
// Mirrors go/internal/identity/grant.go: an owner-signed, expiring, guest-bound
// authorization for one machine's terminal. v1 mints only in the CLI, so this
// module verifies and renders — it does not sign.
import { ed25519 } from '@noble/curves/ed25519';
import { decode as b58decode } from '../wallet/base58.js';

const DOMAIN = 'miranda/grant/v1';
const MACHINE_RE = /^[0-9A-Za-z._-]{1,128}$/;
const SCOPE_RE = /^[0-9A-Za-z._-]{1,64}$/;
const GID_RE = /^[0-9a-f]{16}$/;
// Window cap = 24 h TTL + 5 min skew, in seconds; must match Go's
// GrantMaxTTL + GrantSkew.
const MAX_WINDOW_S = 24 * 3600 + 300;

function b58key(s) {
  let pk;
  try {
    pk = b58decode(s);
  } catch {
    return null;
  }
  return pk.length === 32 ? pk : null;
}

function validate(g) {
  if (g.v !== 1) throw new Error(`grant: unsupported version ${g.v}`);
  if (typeof g.owner !== 'string' || !b58key(g.owner)) throw new Error('grant: owner is not a 32-byte base58 key');
  if (typeof g.machine !== 'string' || !MACHINE_RE.test(g.machine)) throw new Error('grant: invalid machine id');
  if (typeof g.guest !== 'string' || !b58key(g.guest)) throw new Error('grant: guest is not a 32-byte base58 key');
  if (typeof g.scope !== 'string' || !SCOPE_RE.test(g.scope)) throw new Error('grant: invalid scope');
  if (g.mode !== 'ro' && g.mode !== 'rw') throw new Error('grant: mode must be ro or rw');
  if (!Number.isInteger(g.nb) || g.nb <= 0) throw new Error('grant: nb must be a positive integer');
  if (!Number.isInteger(g.na) || g.na <= g.nb) throw new Error('grant: na must be after nb');
  if (g.na - g.nb > MAX_WINDOW_S) throw new Error('grant: window exceeds the 24h cap');
  if (typeof g.gid !== 'string' || !GID_RE.test(g.gid)) throw new Error('grant: gid must be 16 lowercase hex chars');
}

// canonical builds the byte-identical signing message: fixed field order, no
// whitespace. Validated fields need no JSON escaping, so this matches Go's
// hand-built canonical string byte-for-byte.
export function canonical(g) {
  validate(g);
  return `{"v":${g.v},"owner":"${g.owner}","machine":"${g.machine}","guest":"${g.guest}","scope":"${g.scope}","mode":"${g.mode}","nb":${g.nb},"na":${g.na},"gid":"${g.gid}"}`;
}

const enc = new TextEncoder();

// verifyGrant checks the signature against the owner pubkey embedded in the
// record. Record-level only — the caller still checks the owner is the one it
// trusts and that the window covers now. Returns true iff valid; never throws.
export function verifyGrant(sg) {
  let canon;
  try {
    canon = canonical(sg);
  } catch {
    return false;
  }
  const pub = b58key(sg.owner);
  let sig;
  try {
    sig = b58decode(sg.sig);
  } catch {
    return false;
  }
  if (!pub || sig.length !== 64) return false;
  return ed25519.verify(sig, enc.encode(DOMAIN + canon), pub);
}

// validAt reports whether the grant's window covers nowSec (unix seconds).
export function validAt(g, nowSec) {
  return Number.isInteger(g.nb) && Number.isInteger(g.na) && nowSec >= g.nb && nowSec <= g.na;
}

// recordJSON renders the wire record deterministically (canonical + ,"sig":"…").
export function recordJSON(sg) {
  const canon = canonical(sg);
  return canon.slice(0, -1) + `,"sig":"${sg.sig}"}`;
}
