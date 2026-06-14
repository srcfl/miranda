// web/src/identity.js — owner identity from a passkey (WebAuthn prf), with a
// degraded dev fallback. The prf output (deterministic per credential+salt, and
// the same on every device the synced passkey reaches) is fed UNCHANGED into
// BOTH deriveOwnerKey() (X25519 transport key) and deriveWallet() (the Ed25519
// Solana wallet that anchors ownership). They share only the prf root, so the
// owner_id (wallet address) and transport key follow you across devices and are
// gated by Face ID / Touch ID. The relay never sees any of this.
import { deriveOwnerKey } from './identity/owner.js';
import { deriveWallet } from './identity/wallet.js';
import { resolveRPID } from './rp.js';
import { randomBytes } from '@noble/hashes/utils';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils';

// identityFromPRF derives the full identity ({ owner, wallet, secret }) rooted in
// one 32-byte secret — the passkey prf output, or the dev secret. Mirrors how the
// Go owner.json roots both keys in a single seed. `secret` is kept IN MEMORY only
// (never persisted) so the session can derive the registry key (B2); it is the same
// secret deriveWallet/deriveOwnerKey consume.
function identityFromPRF(secret) {
  return { owner: deriveOwnerKey(secret), wallet: deriveWallet(secret), secret };
}

const enc = new TextEncoder();
// rp.id is scoped to the exact production app host. Localhost remains separate
// for dev. Do not use the parent domain as the RP trust root.
const RP_ID = resolveRPID(location.hostname);
const SALT = enc.encode('terminal-relay/owner/v1'); // prf eval salt — FIXED forever
const USER_ID = enc.encode('terminal-relay/owner'); // fixed, no PII; re-enroll overwrites

export const passkeySupported = !!(window.PublicKeyCredential && navigator.credentials && navigator.credentials.create);
export const hasEnrolledPasskey = () => !!localStorage.getItem('tr_cred_id');
export const isLocalhost = () => location.hostname === 'localhost';

function b64url(buf) {
  let s = '';
  for (const x of new Uint8Array(buf)) s += String.fromCharCode(x);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}
function unb64url(s) {
  const bin = atob(s.replace(/-/g, '+').replace(/_/g, '/'));
  const u = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) u[i] = bin.charCodeAt(i);
  return u;
}

// registerPasskey enrolls a synced, discoverable passkey with prf, then derives
// the identity via a get() ceremony (the universal derivation path).
export async function registerPasskey() {
  const cred = await navigator.credentials.create({ publicKey: {
    rp: { id: RP_ID, name: 'Terminal Relay' },
    user: { id: USER_ID, name: 'terminal-relay', displayName: 'Terminal Relay owner' },
    challenge: crypto.getRandomValues(new Uint8Array(32)),
    pubKeyCredParams: [{ type: 'public-key', alg: -7 }, { type: 'public-key', alg: -257 }],
    authenticatorSelection: { residentKey: 'required', requireResidentKey: true, userVerification: 'required' },
    extensions: { prf: { eval: { first: SALT } } },
  } });
  // Do NOT gate on create-time prf.enabled: third-party providers (notably the
  // Bitwarden extension) omit or misreport it at create() even when they evaluate
  // prf on get(). The get() ceremony below is the authoritative gate — identity
  // derivation needs actual prf output, and signInPasskey() demands exactly that.
  localStorage.setItem('tr_cred_id', b64url(cred.rawId));
  try {
    return await signInPasskey();
  } catch (e) {
    localStorage.removeItem('tr_cred_id');
    if (e && e.message === 'NO_PRF') {
      throw new Error('your passkey provider does not support the prf extension — retry with iCloud Keychain or Google Password Manager (you can delete the unused passkey from your provider)');
    }
    throw e;
  }
}

// signInPasskey runs a DISCOVERABLE get() ceremony (Face ID / Touch ID): with no
// allowCredentials, the platform surfaces the iCloud-synced passkey directly
// (rather than the cross-device "scan QR / hardware key" fallback that a stale
// local credential id would trigger), then derives the key from its prf output.
export async function signInPasskey() {
  const assertion = await navigator.credentials.get({ publicKey: {
    challenge: crypto.getRandomValues(new Uint8Array(32)),
    rpId: RP_ID,
    userVerification: 'required',
    extensions: { prf: { eval: { first: SALT } } },
  } });
  const prf = assertion.getClientExtensionResults().prf?.results?.first;
  if (!prf) throw new Error('NO_PRF');
  localStorage.setItem('tr_cred_id', b64url(assertion.rawId));
  return identityFromPRF(new Uint8Array(prf));
}

// devOwnerKey is the DEGRADED localhost-only fallback: a plaintext 32-byte secret
// in localStorage (not biometric-gated, not synced) that roots BOTH the X25519
// transport key and the Ed25519 wallet — exactly as the prf output does on the
// real path, mirroring Go's owner.json single-seed model. It is hard-guarded to
// localhost so a real owner identity can NEVER be minted/persisted in the clear
// on a production origin (where any same-origin script could read it) — not even
// when the browser lacks WebAuthn. On a public host the passkey path is the only
// way in. Returns { owner, wallet }.
export function devOwnerKey() {
  if (!isLocalhost()) throw new Error('dev key is localhost-only; use a passkey on a public origin');
  let h = localStorage.getItem('tr_owner');
  if (!h) {
    // 32-byte secret seed: the BIP39/wallet derivation needs 16..32 bytes.
    h = bytesToHex(randomBytes(32));
    localStorage.setItem('tr_owner', h);
  }
  // Migration: a pre-B1.5 tr_owner held a raw 32-byte x25519 priv. Reuse it as the
  // secret seed so an existing dev install keeps a stable (if re-rooted) identity.
  return identityFromPRF(hexToBytes(h));
}
