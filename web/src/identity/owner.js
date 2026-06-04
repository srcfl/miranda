// web/src/identity/owner.js
// Mirrors go/internal/identity/owner.go: HKDF-SHA256 over the prf output ->
// X25519 owner keypair. Same prf output -> same owner_id on every device.
import { hkdf } from '@noble/hashes/hkdf';
import { sha256 } from '@noble/hashes/sha2';
import { x25519 } from '@noble/curves/ed25519';

const SALT = new TextEncoder().encode('terminal-relay/owner/v1');
const INFO = new TextEncoder().encode('x25519');

export function deriveOwnerKey(prfOutput) {
  const priv = hkdf(sha256, prfOutput, SALT, INFO, 32);
  const pub = x25519.getPublicKey(priv);
  return { priv, pub };
}
