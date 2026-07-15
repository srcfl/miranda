// web/src/identity/auth.js — mirrors go/internal/identity/auth.go. Owner control
// proof over a fresh challenge (e.g. a pairing channel binding).
import { ed25519 } from '@noble/curves/ed25519';
import { sha256 } from '@noble/hashes/sha2';
import { bytesToHex } from '@noble/hashes/utils';
import { decode as b58decode } from '../wallet/base58.js';

const AUTH_DOMAIN = 'miranda/auth/v1';
const ATTACH_DOMAIN = 'miranda/attach/v1';
const REGISTRATION_DOMAIN = 'miranda/agent-registration/v1';
const enc = new TextEncoder();

function authMessage(challenge) {
  const d = enc.encode(AUTH_DOMAIN);
  const m = new Uint8Array(d.length + challenge.length);
  m.set(d);
  m.set(challenge, d.length);
  return m;
}

// signAuth returns the raw 64-byte Ed25519 signature over AUTH_DOMAIN||challenge.
export function signAuth(signer, challenge) {
  return ed25519.sign(authMessage(challenge), signer.priv);
}

// verifyAuth checks a signAuth signature against a base58 Miranda owner id.
export function verifyAuth(ownerID, challenge, sig) {
  let pub;
  try { pub = b58decode(ownerID); } catch { return false; }
  if (pub.length !== 32 || sig.length !== 64) return false;
  return ed25519.verify(sig, authMessage(challenge), pub);
}

// attachChallenge is byte-identical to Go's identity.AttachChallenge.
export function attachChallenge(session, machineID, sdp) {
  const digest = bytesToHex(sha256(enc.encode(sdp)));
  return enc.encode(`${ATTACH_DOMAIN}\n${session}\n${machineID}\n${digest}`);
}

// registrationChallenge is byte-identical to Go's
// identity.RegistrationChallenge.
export function registrationChallenge(machineID, commitment) {
  return enc.encode(`${REGISTRATION_DOMAIN}\n${machineID}\n${commitment}`);
}
