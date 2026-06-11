// Package bip39 implements the BIP39 entropy<->mnemonic and mnemonic->seed
// steps used to render the passkey prf as a 24-word phrase and a wallet seed.
// Tiny and dependency-free for byte-identical parity with web/src/wallet/bip39.js.
package bip39

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"strings"
)

// EntropyToMnemonic renders entropy (16..32 bytes, multiple of 4) as a BIP39
// mnemonic from the English wordlist. 32 bytes -> 24 words (Miranda's prf case).
func EntropyToMnemonic(entropy []byte) (string, error) {
	n := len(entropy)
	if n < 16 || n > 32 || n%4 != 0 {
		return "", fmt.Errorf("bip39: entropy must be 16..32 bytes and a multiple of 4, got %d", n)
	}
	cs := n * 8 / 32 // checksum bits = entropy bits / 32
	hash := sha256.Sum256(entropy)

	// bit reads bit i of (entropy || checksum), MSB-first.
	bit := func(i int) int {
		if i < n*8 {
			return int((entropy[i/8] >> (7 - uint(i%8))) & 1)
		}
		j := i - n*8
		return int((hash[j/8] >> (7 - uint(j%8))) & 1)
	}

	words := make([]string, (n*8+cs)/11)
	for w := range words {
		idx := 0
		for b := 0; b < 11; b++ {
			idx = idx<<1 | bit(w*11+b)
		}
		words[w] = wordlist[idx]
	}
	return strings.Join(words, " "), nil
}

// MnemonicToSeed derives the 64-byte BIP39 seed via PBKDF2-HMAC-SHA512 with 2048
// iterations and salt "mnemonic"+passphrase. Inputs must already be NFKD-
// normalized; the English wordlist and Miranda's empty passphrase are ASCII, so
// this matches the JS side (which applies NFKD) byte-for-byte.
func MnemonicToSeed(mnemonic, passphrase string) []byte {
	seed, err := pbkdf2.Key(sha512.New, mnemonic, []byte("mnemonic"+passphrase), 2048, 64)
	if err != nil {
		// pbkdf2.Key only errors on absurd key lengths; 64 is always valid.
		panic(err)
	}
	return seed
}
