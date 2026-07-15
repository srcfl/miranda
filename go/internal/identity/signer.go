package identity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"io"

	"golang.org/x/crypto/hkdf"

	"github.com/srcful/terminal-relay/go/internal/base58"
	"github.com/srcful/terminal-relay/go/internal/bip39"
)

var (
	signerSalt = []byte("miranda/identity/signer/v1")
	signerInfo = []byte("ed25519")
)

// Signer is Miranda's neutral Ed25519 owner identity. It is deliberately not a
// cryptocurrency wallet. Mnemonic is only a recovery rendering of the 32-byte
// passkey PRF output; the signing seed is domain-separated with HKDF.
type Signer struct {
	Mnemonic string
	Priv     ed25519.PrivateKey
	Pub      ed25519.PublicKey
	Address  string // base58(Pub), used as the opaque owner id
}

// DeriveSigner deterministically derives the Miranda signing identity from a
// passkey PRF output. Mirrors web/src/identity/signer.js exactly.
func DeriveSigner(root []byte) (*Signer, error) {
	mnemonic, err := bip39.EntropyToMnemonic(root)
	if err != nil {
		return nil, err
	}
	r := hkdf.New(sha256.New, root, signerSalt, signerInfo)
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(r, seed); err != nil {
		return nil, err
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	return &Signer{Mnemonic: mnemonic, Priv: priv, Pub: pub, Address: base58.Encode(pub)}, nil
}

// SignerFromMnemonic restores the root entropy and re-derives the same Miranda
// identity. The phrase is not compatible with or intended for financial wallets.
func SignerFromMnemonic(mnemonic string) (*Signer, error) {
	root, err := bip39.MnemonicToEntropy(mnemonic)
	if err != nil {
		return nil, err
	}
	return DeriveSigner(root)
}
