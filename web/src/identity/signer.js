// Neutral Miranda Ed25519 owner identity. The passkey PRF output is rendered as
// BIP39 only for human recovery; the signing seed is domain-separated with HKDF
// and is not a cryptocurrency derivation path.
import { ed25519 } from '@noble/curves/ed25519';
import { hkdf } from '@noble/hashes/hkdf';
import { sha256 } from '@noble/hashes/sha2';
import { entropyToMnemonic, mnemonicToEntropy } from '../wallet/bip39.js';
import { encode as base58encode } from '../wallet/base58.js';

const enc = new TextEncoder();
const SALT = enc.encode('miranda/identity/signer/v1');
const INFO = enc.encode('ed25519');

export function deriveSigner(root) {
  const mnemonic = entropyToMnemonic(root);
  const priv = hkdf(sha256, root, SALT, INFO, 32);
  const pub = ed25519.getPublicKey(priv);
  return { mnemonic, priv, pub, address: base58encode(pub) };
}

export function signerFromMnemonic(mnemonic) {
  return deriveSigner(mnemonicToEntropy(mnemonic));
}
