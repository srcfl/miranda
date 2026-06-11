// go/internal/identity/wallet.go
package identity

import (
	"crypto/ed25519"

	"github.com/srcful/terminal-relay/go/internal/base58"
	"github.com/srcful/terminal-relay/go/internal/bip39"
	"github.com/srcful/terminal-relay/go/internal/slip10"
)

// WalletPath is the Phantom-importable Solana account-0 derivation path.
// Sub-accounts use m/44'/501'/i'/0'.
const WalletPath = "m/44'/501'/0'/0'"

// Wallet is the Solana-compatible Ed25519 identity derived from the passkey prf.
// It is independent of the X25519 transport key (DeriveOwnerKey); the two share
// only the prf root. Address (base58 of the public key) is the wallet owner_id.
type Wallet struct {
	Mnemonic string             // 24-word BIP39 rendering of the prf
	Seed     []byte             // 64-byte BIP39 seed
	Priv     ed25519.PrivateKey // 64-byte (seed||pub); Priv.Seed() is the 32-byte node key
	Pub      ed25519.PublicKey  // 32-byte Ed25519 public key
	Address  string             // base58(Pub) — the Solana address / wallet owner_id
}

// DeriveWallet renders the 32-byte prf as a BIP39 mnemonic and derives the
// account-0 Solana wallet. Restoring from the mnemonic reconstructs prf and
// re-derives the same wallet without the passkey. Mirrors
// web/src/identity/wallet.js exactly.
func DeriveWallet(prf []byte) (*Wallet, error) {
	mnemonic, err := bip39.EntropyToMnemonic(prf)
	if err != nil {
		return nil, err
	}
	return WalletFromMnemonic(mnemonic)
}

// WalletFromMnemonic derives the account-0 wallet from a BIP39 mnemonic (the
// import path). Empty BIP39 passphrase, matching DeriveWallet.
func WalletFromMnemonic(mnemonic string) (*Wallet, error) {
	seed := bip39.MnemonicToSeed(mnemonic, "")
	node, err := slip10.DerivePath(seed, WalletPath)
	if err != nil {
		return nil, err
	}
	priv := ed25519.NewKeyFromSeed(node.Key)
	pub := priv.Public().(ed25519.PublicKey)
	return &Wallet{
		Mnemonic: mnemonic,
		Seed:     seed,
		Priv:     priv,
		Pub:      pub,
		Address:  base58.Encode(pub),
	}, nil
}
