// Package slip10 implements SLIP-0010 HD key derivation for the ed25519 curve.
// ed25519 supports hardened derivation only. Tiny and dependency-free for
// byte-identical parity with web/src/wallet/slip10.js.
//
// master  = HMAC-SHA512("ed25519 seed", seed)            -> key=left32, chain=right32
// child_i = HMAC-SHA512(chain, 0x00 || key || ser32(i))   (i always hardened)
package slip10

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

const hardenedOffset = 0x80000000

// Node is a derived key: a 32-byte key (the ed25519 seed at this node) and its
// 32-byte chain code.
type Node struct {
	Key   []byte
	Chain []byte
}

func master(seed []byte) Node {
	h := hmac.New(sha512.New, []byte("ed25519 seed"))
	h.Write(seed)
	sum := h.Sum(nil)
	return Node{Key: sum[:32], Chain: sum[32:]}
}

// child derives the hardened child at the given full index (already including
// the hardened offset).
func (n Node) child(index uint32) Node {
	var data [37]byte
	data[0] = 0x00
	copy(data[1:33], n.Key)
	binary.BigEndian.PutUint32(data[33:], index)
	h := hmac.New(sha512.New, n.Chain)
	h.Write(data[:])
	sum := h.Sum(nil)
	return Node{Key: sum[:32], Chain: sum[32:]}
}

// DerivePath derives a node along a path such as "m/44'/501'/0'/0'". Every
// segment must be hardened (ed25519 only supports hardened derivation).
func DerivePath(seed []byte, path string) (Node, error) {
	n := master(seed)
	path = strings.TrimSpace(path)
	if path == "m" || path == "" {
		return n, nil
	}
	if !strings.HasPrefix(path, "m/") {
		return Node{}, fmt.Errorf("slip10: path must start with m/, got %q", path)
	}
	for _, seg := range strings.Split(path[2:], "/") {
		if !strings.HasSuffix(seg, "'") {
			return Node{}, fmt.Errorf("slip10: ed25519 requires hardened indices, got %q", seg)
		}
		num, err := strconv.ParseUint(strings.TrimSuffix(seg, "'"), 10, 32)
		if err != nil || num >= hardenedOffset {
			return Node{}, fmt.Errorf("slip10: bad index %q", seg)
		}
		n = n.child(uint32(num) + hardenedOffset)
	}
	return n, nil
}
