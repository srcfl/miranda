package slip10

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

// SLIP-0010 official Test Vector 1 for ed25519 (seed 000102...0f).
// pub is the 33-byte 0x00||pubkey form from the spec.
var tv1 = []struct{ path, chain, priv, pub string }{
	{"m", "90046a93de5380a72b5e45010748567d5ea02bbf6522f979e05c0d8d8ca9fffb", "2b4be7f19ee27bbf30c667b642d5f4aa69fd169872f8fc3059c08ebae2eb19e7", "00a4b2856bfec510abab89753fac1ac0e1112364e7d250545963f135f2a33188ed"},
	{"m/0'", "8b59aa11380b624e81507a27fedda59fea6d0b779a778918a2fd3590e16e9c69", "68e0fe46dfb67e368c75379acec591dad19df3cde26e63b93a8e704f1dade7a3", "008c8a13df77a28f3445213a0f432fde644acaa215fc72dcdf300d5efaa85d350c"},
	{"m/0'/1'", "a320425f77d1b5c2505a6b1b27382b37368ee640e3557c315416801243552f14", "b1d0bad404bf35da785a64ca1ac54b2617211d2777696fbffaf208f746ae84f2", "001932a5270f335bed617d5b935c80aedb1a35bd9fc1e31acafd5372c30f5c1187"},
	{"m/0'/1'/2'", "2e69929e00b5ab250f49c3fb1c12f252de4fed2c1db88387094a0f8c4c9ccd6c", "92a5b23c0b8a99e37d07df3fb9966917f5d06e02ddbd909c7e184371463e9fc9", "00ae98736566d30ed0e9d2f4486a64bc95740d89c7db33f52121f8ea8f76ff0fc1"},
	{"m/0'/1'/2'/2'", "8f6d87f93d750e0efccda017d662a1b31a266e4a6f5993b15f5c1f07f74dd5cc", "30d1dc7e5fc04c31219ab25a27ae00b50f6fd66622f6e9c913253d6511d1e662", "008abae2d66361c879b900d204ad2cc4984fa2aa344dd7ddc46007329ac76c429c"},
	{"m/0'/1'/2'/2'/1000000000'", "68789923a0cac2cd5a29172a475fe9e0fb14cd6adb5ad98a3fa70333e7afa230", "8f94d394a8e8fd6b1bc2f3f49f5c47e385281d5c17e65324b0f62483e37e8793", "003c24da049451555d51a7014a37337aa4e12d41e485abccfa46b47dfb2af54b7a"},
}

func TestVector1(t *testing.T) {
	seed, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	for _, c := range tv1 {
		n, err := DerivePath(seed, c.path)
		if err != nil {
			t.Fatalf("DerivePath(%q): %v", c.path, err)
		}
		if got := hex.EncodeToString(n.Chain); got != c.chain {
			t.Errorf("%s chain = %s, want %s", c.path, got, c.chain)
		}
		if got := hex.EncodeToString(n.Key); got != c.priv {
			t.Errorf("%s key = %s, want %s", c.path, got, c.priv)
		}
		// SLIP-0010 ed25519 public key = 0x00 || ed25519.pub(seed=key).
		pub := ed25519.NewKeyFromSeed(n.Key).Public().(ed25519.PublicKey)
		if got := "00" + hex.EncodeToString(pub); got != c.pub {
			t.Errorf("%s pub = %s, want %s", c.path, got, c.pub)
		}
	}
}

func TestRejectsNonHardened(t *testing.T) {
	seed, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	for _, bad := range []string{"m/44/501'/0'/0'", "m/0", "x/0'"} {
		if _, err := DerivePath(seed, bad); err == nil {
			t.Errorf("DerivePath(%q) should error", bad)
		}
	}
}
