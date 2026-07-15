package identity

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const signerRootHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

func identityVectorPath() string {
	return filepath.Join("..", "..", "..", "testdata", "identity-derivation.json")
}

type identityVector struct {
	Root         string `json:"root"`
	Mnemonic     string `json:"mnemonic"`
	SignerPriv   string `json:"signer_priv"`
	SignerPub    string `json:"signer_pub"`
	OwnerID      string `json:"owner_id"`
	TransportPub string `json:"transport_pub"`
}

func TestIdentityDerivationVector(t *testing.T) {
	root, _ := hex.DecodeString(signerRootHex)
	signer, err := DeriveSigner(root)
	if err != nil {
		t.Fatal(err)
	}
	_, transportPub, err := DeriveOwnerKey(root)
	if err != nil {
		t.Fatal(err)
	}
	got := identityVector{
		Root:         signerRootHex,
		Mnemonic:     signer.Mnemonic,
		SignerPriv:   hex.EncodeToString(signer.Priv.Seed()),
		SignerPub:    hex.EncodeToString(signer.Pub),
		OwnerID:      signer.Address,
		TransportPub: hex.EncodeToString(transportPub),
	}
	path := identityVectorPath()
	if os.Getenv("UPDATE_VECTORS") == "1" {
		data, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vector (run UPDATE_VECTORS=1 first): %v", err)
	}
	var want identityVector
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("identity derivation drifted\n got %+v\nwant %+v", got, want)
	}
	restored, err := SignerFromMnemonic(signer.Mnemonic)
	if err != nil || restored.Address != signer.Address {
		t.Fatalf("recovery mismatch: %v", err)
	}
}
