// go/internal/pairing/code_test.go
package pairing

import (
	"bytes"
	"testing"
)

func TestCodeRoundTrip(t *testing.T) {
	tok := bytes.Repeat([]byte{0xAB}, 16)
	code := EncodeCode("http://localhost:8443", tok)
	signal, got, err := DecodeCode(code)
	if err != nil {
		t.Fatal(err)
	}
	if signal != "http://localhost:8443" || !bytes.Equal(got, tok) {
		t.Fatalf("round trip mismatch: %q %x", signal, got)
	}
}

func TestRoomAndPskAreDistinct(t *testing.T) {
	tok := bytes.Repeat([]byte{0x01}, 16)
	room := RoomID(tok)
	psk := pskFromToken(tok)
	if len(psk) != 32 {
		t.Fatalf("psk must be 32 bytes, got %d", len(psk))
	}
	// roomID must not equal/derive trivially from the psk.
	if room == "" || bytes.Contains(psk, []byte(room)) {
		t.Fatal("roomID and psk are not domain-separated")
	}
	// Same token -> stable room + psk.
	if RoomID(tok) != room || !bytes.Equal(pskFromToken(tok), psk) {
		t.Fatal("derivation not deterministic")
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, _, err := DecodeCode("not-a-code"); err == nil {
		t.Fatal("expected decode error")
	}
}
