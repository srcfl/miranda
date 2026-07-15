package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/srcful/terminal-relay/go/internal/bip39"
	"github.com/srcful/terminal-relay/go/internal/client"
)

func (a *app) cmdIdentity(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mir identity <show|export-recovery|import-recovery|rotate>")
	}
	switch args[0] {
	case "show":
		return a.cmdIdentityShow(args[1:])
	case "export-recovery":
		return a.cmdIdentityExport(args[1:])
	case "import-recovery":
		return a.cmdIdentityImport(args[1:])
	case "rotate":
		return a.cmdIdentityRotate(args[1:])
	default:
		return fmt.Errorf("unknown identity subcommand %q", args[0])
	}
}

// cmdLegacyWallet keeps the v0.6 recovery surface long enough to give users a
// clear migration path; financial wallet/account functionality is removed.
func (a *app) cmdLegacyWallet(args []string) error {
	fmt.Fprintln(a.errOut, "note: `mir wallet` was replaced by `mir identity`; Miranda identities are not cryptocurrency wallets")
	if len(args) == 0 {
		return fmt.Errorf("use `mir identity show|export-recovery|import-recovery`")
	}
	switch args[0] {
	case "address":
		return a.cmdIdentityShow(args[1:])
	case "export-phrase":
		return a.cmdIdentityExport(args[1:])
	case "import-phrase":
		return a.cmdIdentityImport(args[1:])
	default:
		return fmt.Errorf("wallet subcommand %q was removed; use `mir identity`", args[0])
	}
}

func (a *app) cmdIdentityShow(args []string) error {
	fs := flag.NewFlagSet("identity show", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	_ = fs.Parse(args)
	id, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.out, id.OwnerID)
	return nil
}

func (a *app) cmdIdentityExport(args []string) error {
	fs := flag.NewFlagSet("identity export-recovery", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	yes := fs.Bool("yes", false, "confirm you want to reveal the secret recovery phrase")
	_ = fs.Parse(args)
	id, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		return err
	}
	signer, err := id.Signer()
	if err != nil {
		return err
	}
	if !*yes {
		fmt.Fprintln(a.errOut, "This prints the 24-word Miranda recovery phrase. Anyone who sees it controls all machines paired to this identity.")
		return fmt.Errorf("refused: re-run with --yes in a private terminal")
	}
	fmt.Fprintln(a.out, signer.Mnemonic)
	return nil
}

func (a *app) cmdIdentityImport(args []string) error {
	fs := flag.NewFlagSet("identity import-recovery", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	fromStdin := fs.Bool("stdin", false, "read recovery phrase from stdin (controlled automation only)")
	yes := fs.Bool("yes", false, "confirm replacing the current identity")
	_ = fs.Parse(args)
	if !*yes {
		return fmt.Errorf("this replaces the current identity and requires re-pairing; re-run with --yes")
	}
	phrase, err := a.readRecoveryPhrase(*fromStdin)
	if err != nil {
		return err
	}
	root, err := bip39.MnemonicToEntropy(phrase)
	if err != nil {
		return err
	}
	defer func() {
		for i := range root {
			root[i] = 0
		}
	}()
	id, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		return err
	}
	if err := id.SetFromSecret(root); err != nil {
		return err
	}
	if err := client.SaveIdentity(*dir, id); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "identity restored\n  owner_id: %s\n", id.OwnerID)
	return nil
}

func (a *app) cmdIdentityRotate(args []string) error {
	fs := flag.NewFlagSet("identity rotate", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	yes := fs.Bool("yes", false, "confirm rotating the identity and re-pairing every machine")
	_ = fs.Parse(args)
	if !*yes {
		return fmt.Errorf("identity rotation requires re-pairing every machine; re-run with --yes")
	}
	id, err := client.Rekey(*dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "identity rotated\n  owner_id: %s\n", id.OwnerID)
	return nil
}

func (a *app) readRecoveryPhrase(fromStdin bool) (string, error) {
	if a.in == nil {
		a.in = os.Stdin
	}
	if fromStdin {
		data, err := io.ReadAll(io.LimitReader(a.in, 4097))
		if err != nil {
			return "", err
		}
		if len(data) > 4096 {
			return "", fmt.Errorf("recovery phrase input is too large")
		}
		if phrase := strings.TrimSpace(string(data)); phrase != "" {
			return phrase, nil
		}
		return "", fmt.Errorf("stdin contained no recovery phrase")
	}
	f, ok := a.in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", fmt.Errorf("recovery phrase must be entered in a TTY, or piped with explicit --stdin; it is never accepted as a command-line argument")
	}
	fmt.Fprint(a.errOut, "Recovery phrase (input hidden): ")
	data, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(a.errOut)
	if err != nil {
		return "", err
	}
	if phrase := strings.TrimSpace(string(data)); phrase != "" {
		return phrase, nil
	}
	return "", fmt.Errorf("empty recovery phrase")
}
