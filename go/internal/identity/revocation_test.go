package identity

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRevocationRoundTripAndTamper(t *testing.T) {
	signer, err := DeriveSigner(bytes.Repeat([]byte{0x61}, 32))
	if err != nil {
		t.Fatal(err)
	}
	r, err := signer.SignRevocation("machine-1", time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRevocation(r); err != nil {
		t.Fatalf("valid revocation: %v", err)
	}
	tampered := *r
	tampered.MachineID = "machine-2"
	if err := VerifyRevocation(&tampered); err == nil {
		t.Fatal("tampered machine id verified")
	}
}

func TestRevocationInteropVectorStable(t *testing.T) {
	type vector struct {
		Root      string `json:"root"`
		V         int    `json:"v"`
		OwnerID   string `json:"owner_id"`
		MachineID string `json:"machine_id"`
		TS        int64  `json:"ts"`
		Signature string `json:"signature"`
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "revocation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want vector
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	root, err := hex.DecodeString(want.Root)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := DeriveSigner(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := signer.SignRevocation(want.MachineID, time.Unix(want.TS, 0))
	if err != nil {
		t.Fatal(err)
	}
	got := vector{Root: want.Root, V: record.V, OwnerID: record.OwnerID, MachineID: record.MachineID, TS: record.TS, Signature: record.Signature}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("revocation vector drifted\n got %+v\nwant %+v", got, want)
	}
}
