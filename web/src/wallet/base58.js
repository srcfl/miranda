// web/src/wallet/base58.js
// Mirrors go/internal/base58/base58.go: Bitcoin/Solana base58 (no checksum).
// Tiny and dependency-free so Go and JS stay byte-identical.

const ALPHABET = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz';

const REVERSE = (() => {
  const r = new Int16Array(256).fill(-1);
  for (let i = 0; i < ALPHABET.length; i++) r[ALPHABET.charCodeAt(i)] = i;
  return r;
})();

// encode returns the base58 string for a Uint8Array. Leading 0x00 bytes -> '1'.
export function encode(bytes) {
  let zeros = 0;
  while (zeros < bytes.length && bytes[zeros] === 0) zeros++;
  const size = Math.floor(((bytes.length - zeros) * 138) / 100) + 1;
  const buf = new Uint8Array(size);
  let high = size - 1;
  for (let i = zeros; i < bytes.length; i++) {
    let carry = bytes[i];
    let j = size - 1;
    for (; j > high || carry !== 0; j--) {
      carry += 256 * buf[j];
      buf[j] = carry % 58;
      carry = Math.floor(carry / 58);
    }
    high = j;
  }
  let j = 0;
  while (j < size && buf[j] === 0) j++;
  let out = '1'.repeat(zeros);
  for (; j < size; j++) out += ALPHABET[buf[j]];
  return out;
}

// decode parses a base58 string into a Uint8Array. Leading '1' -> 0x00 bytes.
// Throws on any character outside the alphabet.
export function decode(str) {
  let zeros = 0;
  while (zeros < str.length && str[zeros] === '1') zeros++;
  const size = Math.floor(((str.length - zeros) * 733) / 1000) + 1;
  const buf = new Uint8Array(size);
  let high = size - 1;
  for (let i = zeros; i < str.length; i++) {
    const c = REVERSE[str.charCodeAt(i)];
    if (c < 0) throw new Error(`base58: invalid character ${JSON.stringify(str[i])} at ${i}`);
    let carry = c;
    let j = size - 1;
    for (; j > high || carry !== 0; j--) {
      carry += 58 * buf[j];
      buf[j] = carry % 256;
      carry = Math.floor(carry / 256);
    }
    high = j;
  }
  let j = 0;
  while (j < size && buf[j] === 0) j++;
  const out = new Uint8Array(zeros + (size - j));
  out.set(buf.subarray(j), zeros);
  return out;
}
