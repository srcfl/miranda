// web/src/identity/wallet.js
// Mirrors go/internal/identity/wallet.go: prf -> BIP39 -> SLIP-0010-ed25519
// m/44'/501'/0'/0' -> Ed25519 -> base58 Solana address. Independent of the
// X25519 transport key (owner.js); the two share only the prf root.
import { ed25519 } from '@noble/curves/ed25519';
import { entropyToMnemonic, mnemonicToSeed } from '../wallet/bip39.js';
import { derivePath } from '../wallet/slip10.js';
import { encode as base58encode } from '../wallet/base58.js';

// WALLET_PATH is the Phantom-importable Solana account-0 path.
export const WALLET_PATH = "m/44'/501'/0'/0'";

// deriveWallet renders the 32-byte prf as a BIP39 mnemonic and derives the
// account-0 Solana wallet. Returns { mnemonic, seed, priv, pub, address } with
// priv = the 32-byte Ed25519 node key (the signing key for @noble/curves).
export function deriveWallet(prf) {
  return walletFromMnemonic(entropyToMnemonic(prf));
}

// walletFromMnemonic derives the account-0 wallet from a mnemonic (import path).
export function walletFromMnemonic(mnemonic) {
  const seed = mnemonicToSeed(mnemonic, '');
  const node = derivePath(seed, WALLET_PATH);
  const pub = ed25519.getPublicKey(node.key);
  return {
    mnemonic,
    seed,
    priv: node.key, // 32-byte ed25519 seed/private key
    pub, // 32-byte ed25519 public key
    address: base58encode(pub),
  };
}
