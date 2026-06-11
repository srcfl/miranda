package base58

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// Classic Bitcoin base58 vectors + a 32-byte Solana address anchor (the B1
// wallet pubkey -> address, cross-checked against bip-utils).
var vectors = []struct{ hexIn, enc string }{
	{"", ""},
	{"61", "2g"},
	{"626262", "a3gV"},
	{"73696d706c792061206c6f6e6720737472696e67", "2cFupjhnEsSn59qHXstmK2ffpLv2"},
	{"00000000000000000000", "1111111111"},
	{"a3d4ab895f8bc2990f27e64b4ee2abcb9396dc132ead962a1ba6664fd938ec41", "C2XYPfExbj6azVqYLWeUphzsdKK2dQ53dm83Brd3THmS"},
}

func TestEncode(t *testing.T) {
	for _, v := range vectors {
		in, _ := hex.DecodeString(v.hexIn)
		if got := Encode(in); got != v.enc {
			t.Errorf("Encode(%s) = %q, want %q", v.hexIn, got, v.enc)
		}
	}
}

func TestDecode(t *testing.T) {
	for _, v := range vectors {
		want, _ := hex.DecodeString(v.hexIn)
		got, err := Decode(v.enc)
		if err != nil {
			t.Fatalf("Decode(%q): %v", v.enc, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("Decode(%q) = %x, want %s", v.enc, got, v.hexIn)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	for _, h := range []string{"00", "ff", "deadbeef", "00ff00ff00", "0102030405060708090a"} {
		in, _ := hex.DecodeString(h)
		got, err := Decode(Encode(in))
		if err != nil {
			t.Fatalf("round-trip %s: %v", h, err)
		}
		if !bytes.Equal(got, in) {
			t.Errorf("round-trip %s = %x", h, got)
		}
	}
}

func TestDecodeRejectsInvalid(t *testing.T) {
	for _, bad := range []string{"0", "O", "I", "l", "abc!", "  "} {
		if _, err := Decode(bad); err == nil {
			t.Errorf("Decode(%q) should error", bad)
		}
	}
}
