// Package sas derives a short, human-comparable "safety number" from a Noise
// channel binding (the handshake transcript hash). Both ends of an
// un-MITM'd handshake compute the same binding, hence the same safety number;
// a man-in-the-middle produces two different bindings, so the numbers differ.
// Showing it on both ends and comparing it by eye gives a VISIBLE confirmation
// that no MITM is present — defense-in-depth on top of the cryptographic
// guarantee (and the thing that catches a MITM even if a pairing token leaked).
package sas

import (
	"crypto/sha256"
	"fmt"
)

// FromBinding renders a Noise channel binding as a 64-bit safety number, in four
// 4-hex-digit groups (e.g. "a3f1-9c2b-77de-4051"). 64 bits resists a real-time
// birthday-collision MITM grinder within an interactive pairing window.
func FromBinding(binding []byte) string {
	h := sha256.Sum256(append([]byte("terminal-relay/sas/v1"), binding...))
	return fmt.Sprintf("%02x%02x-%02x%02x-%02x%02x-%02x%02x",
		h[0], h[1], h[2], h[3], h[4], h[5], h[6], h[7])
}
