package cli

import (
	"bytes"
	"strings"
	"testing"
)

const (
	knownMnemonic = "abandon math mimic master filter design carbon crystal rookie group knife wrap absurd much snack melt grid rough chapter fever rubber humble room trophy"
	knownAddress  = "C2XYPfExbj6azVqYLWeUphzsdKK2dQ53dm83Brd3THmS"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	var out, errb bytes.Buffer
	code := Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestWalletAddressCreatesPrfRooted(t *testing.T) {
	dir := t.TempDir()
	code, out, errb := run(t, "wallet", "address", "--dir", dir)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errb)
	}
	if len(strings.TrimSpace(out)) < 32 {
		t.Fatalf("address output = %q", out)
	}
}

func TestWalletImportThenAddressAndAccounts(t *testing.T) {
	dir := t.TempDir()
	// import the known phrase -> deterministic external-anchor address.
	code, out, errb := run(t, "wallet", "import-phrase", "--dir", dir, "--phrase", knownMnemonic, "--yes")
	if code != 0 {
		t.Fatalf("import exit = %d, stderr = %q", code, errb)
	}
	if !strings.Contains(out, knownAddress) {
		t.Fatalf("import output = %q, want %s", out, knownAddress)
	}
	// address reflects the imported identity.
	code, out, _ = run(t, "wallet", "address", "--dir", dir)
	if code != 0 || strings.TrimSpace(out) != knownAddress {
		t.Fatalf("address = %q, want %s", out, knownAddress)
	}
	// accounts: first line is account 0 = the same address.
	code, out, errb = run(t, "wallet", "accounts", "--dir", dir, "--count", "3")
	if code != 0 {
		t.Fatalf("accounts exit = %d, stderr = %q", code, errb)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("accounts lines = %d: %q", len(lines), out)
	}
	if !strings.Contains(lines[0], "m/44'/501'/0'/0'") || !strings.Contains(lines[0], knownAddress) {
		t.Fatalf("account 0 = %q", lines[0])
	}
}

func TestWalletExportPhraseGate(t *testing.T) {
	dir := t.TempDir()
	if code, _, _ := run(t, "wallet", "import-phrase", "--dir", dir, "--phrase", knownMnemonic, "--yes"); code != 0 {
		t.Fatal("import failed")
	}
	// refused without --yes.
	code, out, errb := run(t, "wallet", "export-phrase", "--dir", dir)
	if code == 0 {
		t.Fatalf("export-phrase without --yes should fail; out=%q", out)
	}
	if !strings.Contains(errb, "--yes") {
		t.Fatalf("expected --yes hint, got %q", errb)
	}
	// reveals with --yes.
	code, out, _ = run(t, "wallet", "export-phrase", "--dir", dir, "--yes")
	if code != 0 || strings.TrimSpace(out) != knownMnemonic {
		t.Fatalf("export-phrase --yes = %q", out)
	}
}

func TestWalletImportPhraseGuards(t *testing.T) {
	dir := t.TempDir()
	// refused without --yes (identity not replaced).
	if code, _, errb := run(t, "wallet", "import-phrase", "--dir", dir, "--phrase", knownMnemonic); code == 0 {
		t.Fatal("import without --yes should fail")
	} else if !strings.Contains(errb, "--yes") {
		t.Fatalf("expected --yes hint, got %q", errb)
	}
	// invalid phrase errors.
	if code, _, _ := run(t, "wallet", "import-phrase", "--dir", dir, "--phrase", "not a real bip39 phrase at all", "--yes"); code == 0 {
		t.Fatal("invalid phrase should fail")
	}
}

func TestWalletUnknownSubcommand(t *testing.T) {
	if code, _, errb := run(t, "wallet", "frobnicate"); code != 1 || !strings.Contains(errb, "unknown wallet subcommand") {
		t.Fatalf("exit=%d stderr=%q", code, errb)
	}
}
