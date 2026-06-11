// web/src/wallet/bip39.js
// Mirrors go/internal/bip39/bip39.go: BIP39 entropy<->mnemonic + mnemonic->seed.
// Byte-identical to the Go side.
import { sha256 } from '@noble/hashes/sha2';
import { sha512 } from '@noble/hashes/sha2';
import { pbkdf2 } from '@noble/hashes/pbkdf2';
import { wordlist } from './wordlist.js';

const enc = new TextEncoder();

// entropyToMnemonic renders entropy (16..32 bytes, multiple of 4) as a BIP39
// English mnemonic. 32 bytes -> 24 words (Miranda's prf case).
export function entropyToMnemonic(entropy) {
  const n = entropy.length;
  if (n < 16 || n > 32 || n % 4 !== 0) {
    throw new Error(`bip39: entropy must be 16..32 bytes and a multiple of 4, got ${n}`);
  }
  const cs = (n * 8) / 32; // checksum bits
  const hash = sha256(entropy);
  const bit = (i) => {
    if (i < n * 8) return (entropy[i >> 3] >> (7 - (i & 7))) & 1;
    const j = i - n * 8;
    return (hash[j >> 3] >> (7 - (j & 7))) & 1;
  };
  const nwords = (n * 8 + cs) / 11;
  const words = [];
  for (let w = 0; w < nwords; w++) {
    let idx = 0;
    for (let b = 0; b < 11; b++) idx = (idx << 1) | bit(w * 11 + b);
    words.push(wordlist[idx]);
  }
  return words.join(' ');
}

// mnemonicToSeed derives the 64-byte BIP39 seed (PBKDF2-HMAC-SHA512, 2048,
// salt "mnemonic"+passphrase). NFKD-normalizes per spec; ASCII inputs match Go.
export function mnemonicToSeed(mnemonic, passphrase = '') {
  const m = enc.encode(mnemonic.normalize('NFKD'));
  const salt = enc.encode(('mnemonic' + passphrase).normalize('NFKD'));
  return pbkdf2(sha512, m, salt, { c: 2048, dkLen: 64 });
}
