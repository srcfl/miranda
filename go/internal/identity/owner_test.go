// go/internal/identity/owner_test.go
package identity

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestDeriveOwnerKeyIsDeterministic(t *testing.T) {
	prf, _ := hex.DecodeString("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	priv1, pub1, err := DeriveOwnerKey(prf)
	if err != nil {
		t.Fatal(err)
	}
	priv2, pub2, err := DeriveOwnerKey(prf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(priv1, priv2) || !bytes.Equal(pub1, pub2) {
		t.Fatal("derivation not deterministic")
	}
	if len(priv1) != 32 || len(pub1) != 32 {
		t.Fatalf("expected 32-byte keys, got priv=%d pub=%d", len(priv1), len(pub1))
	}
}

func TestDeriveOwnerKeyVariesWithInput(t *testing.T) {
	a, _ := hex.DecodeString("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	b, _ := hex.DecodeString("ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100")
	_, pubA, _ := DeriveOwnerKey(a)
	_, pubB, _ := DeriveOwnerKey(b)
	if bytes.Equal(pubA, pubB) {
		t.Fatal("different prf inputs produced the same owner key")
	}
}
