package bip39

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestWordlistIntegrity(t *testing.T) {
	if len(wordlist) != 2048 {
		t.Fatalf("wordlist has %d words, want 2048", len(wordlist))
	}
	if wordlist[0] != "abandon" || wordlist[2047] != "zoo" {
		t.Fatalf("wordlist bounds: [0]=%q [2047]=%q", wordlist[0], wordlist[2047])
	}
}

func TestEntropyToMnemonicZero(t *testing.T) {
	// Famous BIP39 anchors.
	m16, _ := EntropyToMnemonic(make([]byte, 16))
	want16 := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	if m16 != want16 {
		t.Errorf("16B zero: %q", m16)
	}
	m32, _ := EntropyToMnemonic(make([]byte, 32))
	want32 := strings.Repeat("abandon ", 23) + "art"
	if m32 != want32 {
		t.Errorf("32B zero: %q", m32)
	}
}

func TestPrfToMnemonicAndSeed(t *testing.T) {
	// External anchor (bip-utils) for the B1 fixed prf.
	prf, _ := hex.DecodeString("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	wantMnemonic := "abandon math mimic master filter design carbon crystal rookie group knife wrap absurd much snack melt grid rough chapter fever rubber humble room trophy"
	wantSeed := "559da5e7655dd1fbe657c100870512afb2b654b0acfd32f2c549344407e555bc16c2e71219eefc24acc7ed2cfaeac8a1808d543a5de4890bb2d95a7bb58af5b7"

	m, err := EntropyToMnemonic(prf)
	if err != nil {
		t.Fatal(err)
	}
	if m != wantMnemonic {
		t.Fatalf("mnemonic = %q", m)
	}
	if got := hex.EncodeToString(MnemonicToSeed(m, "")); got != wantSeed {
		t.Fatalf("seed = %s", got)
	}
}

func TestEntropyBounds(t *testing.T) {
	for _, n := range []int{0, 12, 15, 17, 33} {
		if _, err := EntropyToMnemonic(make([]byte, n)); err == nil {
			t.Errorf("EntropyToMnemonic(%dB) should error", n)
		}
	}
}

func TestMnemonicToEntropyRoundTrip(t *testing.T) {
	prf, _ := hex.DecodeString("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	m, _ := EntropyToMnemonic(prf)
	got, err := MnemonicToEntropy(m)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(got) != hex.EncodeToString(prf) {
		t.Fatalf("round-trip = %x", got)
	}
	// zero-entropy anchors round-trip too.
	for _, n := range []int{16, 32} {
		mm, _ := EntropyToMnemonic(make([]byte, n))
		e, err := MnemonicToEntropy(mm)
		if err != nil || len(e) != n {
			t.Fatalf("%dB zero round-trip: %v len=%d", n, err, len(e))
		}
	}
}

func TestMnemonicToEntropyRejects(t *testing.T) {
	// unknown word
	if _, err := MnemonicToEntropy("abandon abandon notaword"); err == nil {
		t.Error("unknown word should error")
	}
	// bad checksum: 24 valid words but last word swapped to break the checksum.
	good, _ := EntropyToMnemonic(make([]byte, 32)) // ...abandon art
	broken := strings.TrimSuffix(good, "art") + "zoo"
	if _, err := MnemonicToEntropy(broken); err == nil {
		t.Error("checksum mismatch should error")
	}
	// bad length
	if _, err := MnemonicToEntropy("abandon abandon abandon"); err == nil {
		t.Error("3-word mnemonic should error")
	}
}
