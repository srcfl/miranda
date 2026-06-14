package identity

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Fixed registry-vector inputs. secret is an arbitrary fixed 32-byte value (the
// wallet prf root is 32 bytes; the vector pins the crypto, not the derivation).
const (
	regSecretHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	regNonceHex  = "0102030405060708090a0b0c" // fixed 12-byte nonce
	regMachineID = "a1b2c3d4e5f60718"
	regRecord    = `{"v":1,"name":"zap-kitchen","host_pub":"269863f7f8d945c83cb429b6f16ab5655229a70b08272318267f41b1e8a28613","signal_url":"wss://signal.miranda.example/agent","ts":1749600000}`
)

func TestRegistryKeyDeterministic(t *testing.T) {
	secret, _ := hex.DecodeString(regSecretHex)
	k1, err := RegistryKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := RegistryKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k1, k2) {
		t.Fatal("same secret produced different keys")
	}
	if len(k1) != 32 {
		t.Fatalf("key length = %d, want 32", len(k1))
	}
	other, _ := hex.DecodeString("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	k3, err := RegistryKey(other)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(k1, k3) {
		t.Fatal("different secrets produced the same key")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	secret, _ := hex.DecodeString(regSecretHex)
	key, _ := RegistryKey(secret)
	nonce, _ := hex.DecodeString(regNonceHex)
	plaintext := []byte(regRecord)

	blob, err := SealRecord(key, nonce, plaintext, regMachineID)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) <= len(nonce) {
		t.Fatal("blob not longer than nonce")
	}
	if !bytes.Equal(blob[:len(nonce)], nonce) {
		t.Fatal("blob does not start with the nonce")
	}

	got, err := OpenRecord(key, blob, regMachineID)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch:\n got %q\nwant %q", got, plaintext)
	}
}

func TestOpenRejectsWrongAAD(t *testing.T) {
	secret, _ := hex.DecodeString(regSecretHex)
	key, _ := RegistryKey(secret)
	nonce, _ := hex.DecodeString(regNonceHex)
	blob, err := SealRecord(key, nonce, []byte(regRecord), regMachineID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRecord(key, blob, "b1b2c3d4e5f60718"); err == nil {
		t.Fatal("open with wrong machineID (AAD) should fail")
	}
}

func TestOpenRejectsTamper(t *testing.T) {
	secret, _ := hex.DecodeString(regSecretHex)
	key, _ := RegistryKey(secret)
	nonce, _ := hex.DecodeString(regNonceHex)
	blob, err := SealRecord(key, nonce, []byte(regRecord), regMachineID)
	if err != nil {
		t.Fatal(err)
	}
	// Flip one ciphertext byte (just past the 12-byte nonce prefix).
	tampered := append([]byte{}, blob...)
	tampered[len(nonce)] ^= 0x01
	if _, err := OpenRecord(key, tampered, regMachineID); err == nil {
		t.Fatal("open of a tampered blob should fail")
	}
	// A blob shorter than the nonce is rejected, not panicked.
	if _, err := OpenRecord(key, nonce[:5], regMachineID); err == nil {
		t.Fatal("open of a short blob should fail")
	}
}

func registryVectorPath() string {
	return filepath.Join("..", "..", "..", "testdata", "registry-vector.json")
}

type registryVector struct {
	Secret    string `json:"secret"`     // hex, 32-byte ikm
	Key       string `json:"key"`        // hex, derived K_reg
	Nonce     string `json:"nonce"`      // hex, 12-byte
	Record    string `json:"record"`     // JSON plaintext string
	MachineID string `json:"machine_id"` // AEAD associated data
	Blob      string `json:"blob"`       // hex, nonce||ciphertext||tag
}

func TestRegistryVector(t *testing.T) {
	secret, _ := hex.DecodeString(regSecretHex)
	nonce, _ := hex.DecodeString(regNonceHex)
	key, err := RegistryKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := SealRecord(key, nonce, []byte(regRecord), regMachineID)
	if err != nil {
		t.Fatal(err)
	}
	got := registryVector{
		Secret:    regSecretHex,
		Key:       hex.EncodeToString(key),
		Nonce:     regNonceHex,
		Record:    regRecord,
		MachineID: regMachineID,
		Blob:      hex.EncodeToString(blob),
	}

	path := registryVectorPath()
	if os.Getenv("UPDATE_VECTORS") == "1" {
		data, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("registry-vector.json written")
		return
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vector (run UPDATE_VECTORS=1 first): %v", err)
	}
	var want registryVector
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("registry crypto drifted from committed vector\n got %+v\nwant %+v", got, want)
	}
	// Belt-and-suspenders: the committed blob opens back to the record under the
	// committed key + machine_id.
	wantBlob, _ := hex.DecodeString(want.Blob)
	wantKey, _ := hex.DecodeString(want.Key)
	opened, err := OpenRecord(wantKey, wantBlob, want.MachineID)
	if err != nil {
		t.Fatalf("committed blob failed to open: %v", err)
	}
	if string(opened) != want.Record {
		t.Fatalf("committed blob opened to %q, want %q", opened, want.Record)
	}
}
