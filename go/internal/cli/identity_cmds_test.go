package cli

import (
	"bytes"
	"strings"
	"testing"
)

const knownMnemonic = "abandon math mimic master filter design carbon crystal rookie group knife wrap absurd much snack melt grid rough chapter fever rubber humble room trophy"

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	var out, errb bytes.Buffer
	code := Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func runInput(t *testing.T, input string, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	var out, errb bytes.Buffer
	code := runWithInput(args, strings.NewReader(input), &out, &errb)
	return code, out.String(), errb.String()
}

func TestIdentityShowCreatesRootedIdentity(t *testing.T) {
	dir := t.TempDir()
	code, out, errb := run(t, "identity", "show", "--dir", dir)
	if code != 0 || len(strings.TrimSpace(out)) < 32 {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, errb)
	}
}

func TestIdentityImportThenShow(t *testing.T) {
	dir := t.TempDir()
	code, out, errb := runInput(t, knownMnemonic, "identity", "import-recovery", "--dir", dir, "--stdin", "--yes")
	if code != 0 || !strings.Contains(out, "owner_id:") {
		t.Fatalf("import exit=%d out=%q stderr=%q", code, out, errb)
	}
	want := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out), "identity restored\n  owner_id:"))
	code, out, errb = run(t, "identity", "show", "--dir", dir)
	if code != 0 || strings.TrimSpace(out) != want {
		t.Fatalf("show=%q want=%q stderr=%q", out, want, errb)
	}
}

func TestIdentityRecoveryExportGate(t *testing.T) {
	dir := t.TempDir()
	if code, _, _ := runInput(t, knownMnemonic, "identity", "import-recovery", "--dir", dir, "--stdin", "--yes"); code != 0 {
		t.Fatal("import failed")
	}
	if code, _, errb := run(t, "identity", "export-recovery", "--dir", dir); code == 0 || !strings.Contains(errb, "--yes") {
		t.Fatalf("expected guarded export, exit=%d stderr=%q", code, errb)
	}
	code, out, _ := run(t, "identity", "export-recovery", "--dir", dir, "--yes")
	if code != 0 || strings.TrimSpace(out) != knownMnemonic {
		t.Fatalf("export=%q", out)
	}
}

func TestIdentityImportRequiresExplicitStdinAndConfirmation(t *testing.T) {
	dir := t.TempDir()
	if code, _, errb := runInput(t, knownMnemonic, "identity", "import-recovery", "--dir", dir, "--stdin"); code == 0 || !strings.Contains(errb, "--yes") {
		t.Fatalf("expected confirmation failure, exit=%d stderr=%q", code, errb)
	}
	if code, _, errb := runWithCapturedInput(t, knownMnemonic, "identity", "import-recovery", "--dir", dir, "--yes"); code == 0 || !strings.Contains(errb, "explicit --stdin") {
		t.Fatalf("expected non-TTY input refusal, exit=%d stderr=%q", code, errb)
	}
}

func runWithCapturedInput(t *testing.T, input string, args ...string) (int, string, string) {
	return runInput(t, input, args...)
}

func TestLegacyWalletAliasHasNoFinancialAccounts(t *testing.T) {
	if code, _, errb := run(t, "wallet", "accounts"); code == 0 || !strings.Contains(errb, "removed") {
		t.Fatalf("exit=%d stderr=%q", code, errb)
	}
}
