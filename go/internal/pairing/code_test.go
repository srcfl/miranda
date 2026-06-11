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

// TestValidSignalURL mirrors web/src/pairing/code.js validSignalURL: https is
// always allowed, http only for localhost/127.0.0.1, everything else rejected.
func TestValidSignalURL(t *testing.T) {
	accept := []string{
		"https://relay.example",
		"https://relay.example:8443/path",
		"HTTPS://Relay.Example", // scheme is case-insensitive
		"http://localhost",
		"http://localhost:8443",
		"http://127.0.0.1",
		"http://127.0.0.1:8443",
	}
	for _, s := range accept {
		if !validSignalURL(s) {
			t.Errorf("validSignalURL(%q) = false, want true", s)
		}
	}
	reject := []string{
		"javascript:alert(1)",       // script scheme
		"data:text/html,<b>x</b>",   // data scheme
		"file:///etc/passwd",        // file scheme
		"ftp://host/x",              // wrong scheme
		"http://relay.example",      // plain http to a public host (downgrade)
		"http://localhost.evil.com", // not actually localhost
		"not a url at all",          // garbage
		"",                          // empty
		"//relay.example",           // schemeless
		"https:",                    // no authority
		"https://",                  // no host
	}
	for _, s := range reject {
		if validSignalURL(s) {
			t.Errorf("validSignalURL(%q) = true, want false", s)
		}
	}
}

// TestDecodeRejectsBadSignalURL drives the rejection through DecodeCode end-to-end
// (a valid token + a hostile/invalid signal URL must fail closed, like JS).
func TestDecodeRejectsBadSignalURL(t *testing.T) {
	tok := bytes.Repeat([]byte{0x42}, 16)
	bad := []string{
		"javascript:alert(1)",
		"data:text/html,x",
		"file:///etc/passwd",
		"http://relay.example",
		"ftp://host",
		"not a url",
		"",
	}
	for _, s := range bad {
		code := EncodeCode(s, tok)
		if _, _, err := DecodeCode(code); err == nil {
			t.Errorf("DecodeCode with signal %q = nil error, want rejection", s)
		}
	}
}

// TestDecodeAcceptsGoodSignalURL confirms the allowed URLs round-trip through
// DecodeCode unchanged (https anywhere, http only on localhost).
func TestDecodeAcceptsGoodSignalURL(t *testing.T) {
	tok := bytes.Repeat([]byte{0x42}, 16)
	good := []string{
		"https://relay.example",
		"http://localhost:8443",
		"http://127.0.0.1:8443",
	}
	for _, s := range good {
		code := EncodeCode(s, tok)
		gotURL, gotTok, err := DecodeCode(code)
		if err != nil {
			t.Errorf("DecodeCode with signal %q: unexpected error %v", s, err)
			continue
		}
		if gotURL != s {
			t.Errorf("DecodeCode signal = %q, want %q", gotURL, s)
		}
		if !bytes.Equal(gotTok, tok) {
			t.Errorf("DecodeCode token mismatch for %q", s)
		}
	}
}
