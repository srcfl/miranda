// Package base58 implements Bitcoin/Solana base58 encoding (no checksum).
// Tiny and dependency-free so the Go and JS sides stay byte-identical and the
// implementation is auditable. Mirrors web/src/wallet/base58.js exactly.
package base58

import "fmt"

// alphabet is the Bitcoin/Solana base58 alphabet (no 0, O, I, l).
const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// reverse maps a byte value to its index in alphabet, or -1 if absent.
var reverse = func() [256]int8 {
	var r [256]int8
	for i := range r {
		r[i] = -1
	}
	for i := 0; i < len(alphabet); i++ {
		r[alphabet[i]] = int8(i)
	}
	return r
}()

// Encode returns the base58 encoding of b. Leading zero bytes become leading '1'.
func Encode(b []byte) string {
	zeros := 0
	for zeros < len(b) && b[zeros] == 0 {
		zeros++
	}
	// size = ceil((len-zeros) * log(256)/log(58)) + 1; 138/100 > 1.365.
	size := (len(b)-zeros)*138/100 + 1
	buf := make([]byte, size)
	high := size - 1
	for i := zeros; i < len(b); i++ {
		carry := int(b[i])
		j := size - 1
		for ; j > high || carry != 0; j-- {
			carry += 256 * int(buf[j])
			buf[j] = byte(carry % 58)
			carry /= 58
		}
		high = j
	}
	// Skip leading zero digits produced by the buffer pre-sizing.
	j := 0
	for j < size && buf[j] == 0 {
		j++
	}
	out := make([]byte, zeros+(size-j))
	for i := 0; i < zeros; i++ {
		out[i] = '1'
	}
	for i := zeros; j < size; i++ {
		out[i] = alphabet[buf[j]]
		j++
	}
	return string(out)
}

// Decode parses a base58 string into its byte value. Leading '1' become leading
// zero bytes. It errors on any character outside the alphabet.
func Decode(s string) ([]byte, error) {
	zeros := 0
	for zeros < len(s) && s[zeros] == '1' {
		zeros++
	}
	// size = ceil((len-zeros) * log(58)/log(256)) + 1; 733/1000 > 0.7322.
	size := (len(s)-zeros)*733/1000 + 1
	buf := make([]byte, size)
	high := size - 1
	for i := zeros; i < len(s); i++ {
		c := reverse[s[i]]
		if c < 0 {
			return nil, fmt.Errorf("base58: invalid character %q at %d", s[i], i)
		}
		carry := int(c)
		j := size - 1
		for ; j > high || carry != 0; j-- {
			carry += 58 * int(buf[j])
			buf[j] = byte(carry % 256)
			carry /= 256
		}
		high = j
	}
	j := 0
	for j < size && buf[j] == 0 {
		j++
	}
	out := make([]byte, zeros+(size-j))
	copy(out[zeros:], buf[j:])
	return out, nil
}
