// go/internal/pairing/code.go
package pairing

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
)

// NewToken returns a fresh 16-byte (128-bit) single-use pairing token.
func NewToken() []byte {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b
}

type codePayload struct {
	S string `json:"s"` // signal URL
	T string `json:"t"` // token hex
}

// EncodeCode produces a self-contained, copy-pasteable pairing code.
func EncodeCode(signalURL string, token []byte) string {
	data, _ := json.Marshal(codePayload{S: signalURL, T: hex.EncodeToString(token)})
	return base64.RawURLEncoding.EncodeToString(data)
}

// DecodeCode parses a pairing code into its signal URL and token.
func DecodeCode(code string) (signalURL string, token []byte, err error) {
	data, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil {
		return "", nil, fmt.Errorf("bad pairing code: %w", err)
	}
	var p codePayload
	if err := json.Unmarshal(data, &p); err != nil {
		return "", nil, fmt.Errorf("bad pairing code: %w", err)
	}
	token, err = hex.DecodeString(p.T)
	if err != nil || len(token) != 16 {
		return "", nil, fmt.Errorf("bad pairing code token")
	}
	// Validate the relay URL before it ever drives DialPair/websocket.Dial or is
	// persisted. The relay is untrusted by design (Noise authenticates the peer,
	// not the transport), but a scanned/pasted code is attacker-controlled, so
	// reject anything that is not a real http(s) origin: no javascript:/data:/
	// file: schemes, no plain-http to a public host (downgrade), no garbage that
	// would corrupt the ws:// derivation. Mirrors web/src/pairing/code.js
	// (validSignalURL) — fail closed identically so a CLI never silently dials a
	// hostile transport. http is allowed only for localhost dev.
	if !validSignalURL(p.S) {
		return "", nil, fmt.Errorf("bad pairing code signal URL")
	}
	return p.S, token, nil
}

// validSignalURL reports whether s is a relay URL we are willing to dial. It
// mirrors validSignalURL in web/src/pairing/code.js: require an http(s) URL;
// https is always allowed; plain http only for localhost / 127.0.0.1; everything
// else (javascript:/data:/file:, other schemes, malformed, schemeless) is
// rejected. url.Parse lowercases the scheme, matching the JS URL.protocol check.
func validSignalURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		// JS `new URL` throws on an authority-less URL like "https:" / "https://";
		// fail closed to match (and a hostless URL can't drive a real ws:// dial).
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		return host == "localhost" || host == "127.0.0.1"
	default:
		return false
	}
}

// pskFromToken derives the 32-byte Noise PSK (domain-separated from roomID).
func pskFromToken(token []byte) []byte {
	h := sha256.Sum256(append([]byte("terminal-relay/pair/psk"), token...))
	return h[:]
}

// RoomID derives the public rendezvous id (domain-separated from the psk).
func RoomID(token []byte) string {
	h := sha256.Sum256(append([]byte("terminal-relay/pair/room"), token...))
	return hex.EncodeToString(h[:16])
}
