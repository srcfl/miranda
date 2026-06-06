// web/src/identity.js — owner identity from a passkey (WebAuthn prf), with a
// degraded dev fallback. The prf output (deterministic per credential+salt, and
// the same on every device the synced passkey reaches) is fed UNCHANGED into the
// existing deriveOwnerKey() so the owner_id follows you across devices and is
// gated by Face ID / Touch ID. The relay never sees any of this.
import { deriveOwnerKey } from './identity/owner.js';
import { x25519 } from '@noble/curves/ed25519';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils';

const enc = new TextEncoder();
// rp.id = the registrable parent domain, so one passkey works across
// term./app./relay.sourceful-labs.net without re-enrolling. localhost for dev.
const RP_ID = location.hostname === 'localhost' ? 'localhost' : 'sourceful-labs.net';
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
  const ext = cred.getClientExtensionResults();
  if (!ext.prf || ext.prf.enabled !== true) throw new Error('NO_PRF');
  localStorage.setItem('tr_cred_id', b64url(cred.rawId));
  return signInPasskey();
}

// signInPasskey runs a get() ceremony (Face ID / Touch ID) and derives the key.
export async function signInPasskey() {
  const credId = localStorage.getItem('tr_cred_id');
  const publicKey = {
    challenge: crypto.getRandomValues(new Uint8Array(32)),
    rpId: RP_ID,
    userVerification: 'required',
  };
  if (credId) {
    publicKey.allowCredentials = [{ type: 'public-key', id: unb64url(credId) }];
    publicKey.extensions = { prf: { evalByCredential: { [credId]: { first: SALT } } } };
  } else {
    publicKey.extensions = { prf: { eval: { first: SALT } } }; // synced 2nd device, no local id yet
  }
  const assertion = await navigator.credentials.get({ publicKey });
  const prf = assertion.getClientExtensionResults().prf?.results?.first;
  if (!prf) throw new Error('NO_PRF');
  if (!credId) localStorage.setItem('tr_cred_id', b64url(assertion.rawId));
  return deriveOwnerKey(new Uint8Array(prf));
}

// devOwnerKey is the DEGRADED fallback: a plaintext x25519 key in localStorage
// (not biometric-gated, not synced). For localhost dev or when prf is absent.
export function devOwnerKey() {
  let h = localStorage.getItem('tr_owner');
  if (!h) {
    h = bytesToHex(x25519.utils.randomPrivateKey());
    localStorage.setItem('tr_owner', h);
  }
  const priv = hexToBytes(h);
  return { priv, pub: x25519.getPublicKey(priv) };
}
