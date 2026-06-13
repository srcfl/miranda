// web/src/identity/auth.js — mirrors go/internal/identity/auth.go. Wallet control
// proof over a fresh challenge (e.g. a pairing channel binding).
import { ed25519 } from '@noble/curves/ed25519';
import { decode as b58decode } from '../wallet/base58.js';

const AUTH_DOMAIN = 'miranda/auth/v1';
const enc = new TextEncoder();

function authMessage(challenge) {
  const d = enc.encode(AUTH_DOMAIN);
  const m = new Uint8Array(d.length + challenge.length);
  m.set(d);
  m.set(challenge, d.length);
  return m;
}

// signAuth returns the raw 64-byte Ed25519 signature over AUTH_DOMAIN||challenge.
export function signAuth(wallet, challenge) {
  return ed25519.sign(authMessage(challenge), wallet.priv);
}

// verifyAuth checks a signAuth signature against a base58 wallet address.
export function verifyAuth(walletAddress, challenge, sig) {
  let pub;
  try { pub = b58decode(walletAddress); } catch { return false; }
  if (pub.length !== 32 || sig.length !== 64) return false;
  return ed25519.verify(sig, authMessage(challenge), pub);
}
