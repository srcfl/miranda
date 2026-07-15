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

// FromBinding renders a Noise channel binding as a 96-bit safety number in six
// compact groups. Pairing is rare; the extra comparison cost buys a much wider
// defense-in-depth margin if a pairing token is exposed.
func FromBinding(binding []byte) string {
	h := sha256.Sum256(append([]byte("miranda/sas/v2"), binding...))
	return fmt.Sprintf("%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x",
		h[0], h[1], h[2], h[3], h[4], h[5], h[6], h[7], h[8], h[9], h[10], h[11])
}
