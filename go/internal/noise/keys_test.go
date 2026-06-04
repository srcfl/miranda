// go/internal/noise/keys_test.go
package noise

import (
	"bytes"
	"testing"
)

func TestPublicFromPrivateIsDeterministic(t *testing.T) {
	priv := make([]byte, 32)
	for i := range priv {
		priv[i] = byte(i)
	}
	pub1, err := PublicFromPrivate(priv)
	if err != nil {
		t.Fatal(err)
	}
	pub2, err := PublicFromPrivate(priv)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pub1, pub2) {
		t.Fatal("public key not deterministic for same private")
	}
	if len(pub1) != 32 {
		t.Fatalf("expected 32-byte public, got %d", len(pub1))
	}
}

func TestGenerateStaticProducesUsableKeys(t *testing.T) {
	priv, pub, err := GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	derived, err := PublicFromPrivate(priv)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pub, derived) {
		t.Fatal("GenerateStatic public does not match PublicFromPrivate")
	}
}
