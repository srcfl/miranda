package client

import (
	"bytes"
	"strings"
	"testing"
)

// TestDarwinPutInputCarriesSecretInBand is the regression guard for the macOS
// keychain bug where `security add-generic-password -w` (flag last, no value)
// read the secret from a tty prompt instead of piped stdin and silently stored
// an EMPTY secret with exit 0 — breaking every `mir list`/`up`/`attach` with
// "owner secret is empty". The fix drives `security -i`, carrying the whole
// command (secret included) on stdin. This must stay true.
func TestDarwinPutInputCarriesSecretInBand(t *testing.T) {
	ref := "owner-0123456789abcdef0123456789abcdef"
	secret := []byte("a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90") // 64 hex

	in, err := darwinPutInput(ref, keychainService, secret)
	if err != nil {
		t.Fatalf("darwinPutInput returned error for valid input: %v", err)
	}
	got := string(in)
	// The secret must appear verbatim after `-w` (the store bug produced input
	// that stored nothing) and the command must end with a newline so `security
	// -i` executes it.
	if !strings.Contains(got, "-w "+string(secret)+"\n") {
		t.Fatalf("secret not carried in-band after -w; input=%q", got)
	}
	if !strings.HasPrefix(got, "add-generic-password -a "+ref+" -s "+keychainService+" -U -w ") {
		t.Fatalf("unexpected command shape: %q", got)
	}
}

func TestDarwinPutInputRejectsUnsafeSecret(t *testing.T) {
	ref := "owner-0123456789abcdef0123456789abcdef"
	// A secret with whitespace/quotes/backslash could break the command line and
	// must be refused (fail-closed) rather than silently mangled.
	for _, bad := range [][]byte{
		[]byte("has space"),
		[]byte("has\nnewline"),
		[]byte("has\"quote"),
		[]byte("has\\backslash"),
		[]byte{}, // empty
	} {
		if _, err := darwinPutInput(ref, keychainService, bad); err == nil {
			t.Fatalf("expected rejection for unsafe secret %q", bad)
		}
	}
}

func TestDarwinPutInputRejectsBadRef(t *testing.T) {
	secret := []byte("deadbeef")
	for _, bad := range []string{"", "has space", "has/slash", "a;rm -rf", strings.Repeat("x", 200)} {
		if _, err := darwinPutInput(bad, keychainService, secret); err == nil {
			t.Fatalf("expected rejection for bad ref %q", bad)
		}
	}
}

// TestDarwinPutInputSecretNotInArgv documents the security property: the secret
// travels only via the returned stdin bytes, so it never reaches the process
// argv (ps/shell-history exposure). This is a shape assertion, not a live exec.
func TestDarwinPutInputSecretNotInArgv(t *testing.T) {
	ref := "owner-0123456789abcdef0123456789abcdef"
	secret := []byte("cafebabecafebabe")
	in, err := darwinPutInput(ref, keychainService, secret)
	if err != nil {
		t.Fatal(err)
	}
	// The command is `security -i`; argv holds no secret. Confirm the secret is
	// present in the stdin payload only.
	if !bytes.Contains(in, secret) {
		t.Fatal("secret missing from stdin payload")
	}
}
