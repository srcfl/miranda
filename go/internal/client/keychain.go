package client

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const keychainService = "net.miranda.cli.owner"

var secretRefRE = regexp.MustCompile(`^[0-9A-Za-z._-]{1,128}$`)

type secretStore interface {
	Get(ref string) ([]byte, error)
	Put(ref string, secret []byte) error
	Delete(ref string) error
	Name() string
}

func platformSecretStore() (secretStore, error) {
	// Hermetic tests must never touch a developer's real login keychain. The
	// override is deliberately accepted only by a Go test binary.
	if dir := os.Getenv("MIR_TEST_KEYCHAIN_DIR"); dir != "" && strings.HasSuffix(os.Args[0], ".test") {
		return fileSecretStore{dir: dir}, nil
	}
	switch runtime.GOOS {
	case "darwin":
		const path = "/usr/bin/security"
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("macOS Keychain is unavailable: %w", err)
		}
		return commandSecretStore{kind: "macOS Keychain", path: path}, nil
	case "linux":
		// Do not resolve a secret-consuming executable through caller-controlled
		// PATH. Only known system locations are eligible.
		for _, path := range []string{"/usr/bin/secret-tool", "/bin/secret-tool", "/usr/local/bin/secret-tool"} {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return commandSecretStore{kind: "Secret Service", path: path}, nil
			}
		}
		return nil, fmt.Errorf("Secret Service is unavailable: install secret-tool/libsecret-tools (plaintext fallback is disabled)")
	default:
		return nil, fmt.Errorf("secure owner-secret storage is not supported on %s", runtime.GOOS)
	}
}

type commandSecretStore struct {
	kind string
	path string
}

func (s commandSecretStore) Name() string { return s.kind }

func (s commandSecretStore) Get(ref string) ([]byte, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command(s.path, "find-generic-password", "-a", ref, "-s", keychainService, "-w")
	} else {
		cmd = exec.Command(s.path, "lookup", "service", keychainService, "owner", ref)
	}
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		// Never include command output: some keychain tools can echo secret data.
		return nil, fmt.Errorf("%s: owner secret %q is unavailable — if the keychain is locked, unlock it and retry; `mir doctor` checks this", s.kind, ref)
	}
	secret := bytes.TrimSpace(out)
	if len(secret) == 0 {
		return nil, fmt.Errorf("%s: owner secret %q is empty", s.kind, ref)
	}
	return secret, nil
}

func (s commandSecretStore) Put(ref string, secret []byte) error {
	var cmd *exec.Cmd
	var input []byte
	if runtime.GOOS == "darwin" {
		// See darwinPutInput: `add-generic-password ... -w` (flag last, no value)
		// does NOT read the secret from piped stdin — security prompts via
		// readpassphrase and silently stores an EMPTY secret when stdin is a pipe.
		// Drive interactive command mode so the whole command, secret included,
		// travels on stdin — never argv (no ps exposure), never a tty prompt.
		in, err := darwinPutInput(ref, keychainService, secret)
		if err != nil {
			return fmt.Errorf("%s: could not store owner secret (plaintext fallback is disabled)", s.kind)
		}
		cmd = exec.Command(s.path, "-i")
		input = in
	} else {
		cmd = exec.Command(s.path, "store", "--label=Miranda owner identity", "service", keychainService, "owner", ref)
		input = append(append([]byte(nil), secret...), '\n')
	}
	defer zeroBytes(input)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: could not store owner secret (plaintext fallback is disabled)", s.kind)
	}
	return nil
}

func (s commandSecretStore) Delete(ref string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command(s.path, "delete-generic-password", "-a", ref, "-s", keychainService)
	} else {
		cmd = exec.Command(s.path, "clear", "service", keychainService, "owner", ref)
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: could not delete retired owner secret", s.kind)
	}
	return nil
}

// darwinPutInput builds the stdin fed to `security -i` for a keychain write.
// The secret must be safe to place on the interactive command line (which lives
// entirely on stdin, never argv): no whitespace that would end the token early
// and no quote/backslash that `security`'s tokenizer would treat specially. The
// owner secret is hex, so this always holds; the guard is fail-closed defense in
// depth against a caller passing raw bytes. Returned bytes hold the secret — the
// caller must zero them.
func darwinPutInput(ref, service string, secret []byte) ([]byte, error) {
	if !secretRefRE.MatchString(ref) || !secretRefRE.MatchString(service) {
		return nil, fmt.Errorf("invalid keychain reference")
	}
	if len(secret) == 0 || bytes.ContainsAny(secret, " \t\r\n\"'\\") {
		return nil, fmt.Errorf("secret is not command-line safe")
	}
	return []byte(fmt.Sprintf("add-generic-password -a %s -s %s -U -w %s\n", ref, service, secret)), nil
}

// fileSecretStore is test-only; platformSecretStore refuses to select it from a
// production binary even if the environment variable is present.
type fileSecretStore struct{ dir string }

func (s fileSecretStore) Name() string { return "test keychain" }
func (s fileSecretStore) path(ref string) (string, error) {
	if !secretRefRE.MatchString(ref) {
		return "", fmt.Errorf("invalid secret reference")
	}
	return filepath.Join(s.dir, ref), nil
}
func (s fileSecretStore) Get(ref string) ([]byte, error) {
	path, err := s.path(ref)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("test keychain: owner secret unavailable")
	}
	return bytes.TrimSpace(data), nil
}
func (s fileSecretStore) Put(ref string, secret []byte) error {
	path, err := s.path(ref)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	data := append(append([]byte(nil), secret...), '\n')
	defer zeroBytes(data)
	return os.WriteFile(path, data, 0o600)
}

func encodeSecretHex(secret []byte) []byte {
	encoded := make([]byte, hex.EncodedLen(len(secret)))
	hex.Encode(encoded, secret)
	return encoded
}

func decodeSecretHex(encoded []byte) ([]byte, error) {
	encoded = bytes.TrimSpace(encoded)
	if len(encoded) != 64 {
		return nil, fmt.Errorf("owner identity: keychain secret must be 32 bytes")
	}
	secret := make([]byte, 32)
	if _, err := hex.Decode(secret, encoded); err != nil {
		zeroBytes(secret)
		return nil, fmt.Errorf("owner identity: keychain secret must be 32 bytes")
	}
	return secret, nil
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
func (s fileSecretStore) Delete(ref string) error {
	path, err := s.path(ref)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
