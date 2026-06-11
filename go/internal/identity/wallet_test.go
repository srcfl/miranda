package identity

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Same prf as owner-derivation.json; external anchors cross-checked vs bip-utils.
const walletPrfHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

func TestDeriveWalletAnchor(t *testing.T) {
	prf, _ := hex.DecodeString(walletPrfHex)
	w, err := DeriveWallet(prf)
	if err != nil {
		t.Fatal(err)
	}
	if w.Address != "C2XYPfExbj6azVqYLWeUphzsdKK2dQ53dm83Brd3THmS" {
		t.Errorf("address = %s", w.Address)
	}
	if got := hex.EncodeToString(w.Pub); got != "a3d4ab895f8bc2990f27e64b4ee2abcb9396dc132ead962a1ba6664fd938ec41" {
		t.Errorf("pub = %s", got)
	}
	wantMnemonic := "abandon math mimic master filter design carbon crystal rookie group knife wrap absurd much snack melt grid rough chapter fever rubber humble room trophy"
	if w.Mnemonic != wantMnemonic {
		t.Errorf("mnemonic = %q", w.Mnemonic)
	}
	// Import path reproduces the same wallet.
	w2, err := WalletFromMnemonic(w.Mnemonic)
	if err != nil {
		t.Fatal(err)
	}
	if w2.Address != w.Address {
		t.Errorf("import mismatch: %s != %s", w2.Address, w.Address)
	}
}

// vectorPath: internal/identity -> repo-root testdata.
func walletVectorPath() string {
	return filepath.Join("..", "..", "..", "testdata", "wallet-derivation.json")
}

type walletVector struct {
	PrfOutput  string `json:"prf_output"`
	Mnemonic   string `json:"mnemonic"`
	Seed       string `json:"seed"`
	WalletPriv string `json:"wallet_priv"` // 32-byte node key (ed25519 seed)
	WalletPub  string `json:"wallet_pub"`
	Address    string `json:"address"`
	OwnerPub   string `json:"owner_pub"` // X25519 transport pub — proves it is unchanged
}

func TestWalletDerivationVector(t *testing.T) {
	prf, _ := hex.DecodeString(walletPrfHex)
	w, err := DeriveWallet(prf)
	if err != nil {
		t.Fatal(err)
	}
	_, ownerPub, err := DeriveOwnerKey(prf)
	if err != nil {
		t.Fatal(err)
	}
	got := walletVector{
		PrfOutput:  walletPrfHex,
		Mnemonic:   w.Mnemonic,
		Seed:       hex.EncodeToString(w.Seed),
		WalletPriv: hex.EncodeToString(w.Priv.Seed()),
		WalletPub:  hex.EncodeToString(w.Pub),
		Address:    w.Address,
		OwnerPub:   hex.EncodeToString(ownerPub),
	}

	path := walletVectorPath()
	if os.Getenv("UPDATE_VECTORS") == "1" {
		data, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("wallet-derivation.json written")
		return
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vector (run UPDATE_VECTORS=1 first): %v", err)
	}
	var want walletVector
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("wallet derivation drifted from committed vector\n got %+v\nwant %+v", got, want)
	}
	// Guardrail: the X25519 transport key must not have moved.
	if want.OwnerPub != "269863f7f8d945c83cb429b6f16ab5655229a70b08272318267f41b1e8a28613" {
		t.Fatalf("owner X25519 pub changed: %s", want.OwnerPub)
	}
}
