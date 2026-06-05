// go/internal/pairing/code.go
package pairing

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	return p.S, token, nil
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
