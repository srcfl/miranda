package cli

import (
	"flag"
	"fmt"
	"strings"

	"github.com/srcful/terminal-relay/go/internal/bip39"
	"github.com/srcful/terminal-relay/go/internal/client"
	"github.com/srcful/terminal-relay/go/internal/identity"
)

// cmdWallet dispatches `mir wallet <sub>`: the Solana wallet derived from the
// identity's prf root. Reveal/restore subcommands gate on --yes.
func (a *app) cmdWallet(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mir wallet <address|accounts|export-phrase|import-phrase>")
	}
	switch args[0] {
	case "address":
		return a.cmdWalletAddress(args[1:])
	case "accounts":
		return a.cmdWalletAccounts(args[1:])
	case "export-phrase":
		return a.cmdWalletExportPhrase(args[1:])
	case "import-phrase":
		return a.cmdWalletImportPhrase(args[1:])
	default:
		return fmt.Errorf("unknown wallet subcommand %q (want address|accounts|export-phrase|import-phrase)", args[0])
	}
}

func (a *app) cmdWalletAddress(args []string) error {
	fs := flag.NewFlagSet("wallet address", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	_ = fs.Parse(args)
	id, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		return err
	}
	w, err := id.Wallet()
	if err != nil {
		return err
	}
	fmt.Fprintln(a.out, w.Address)
	return nil
}

func (a *app) cmdWalletAccounts(args []string) error {
	fs := flag.NewFlagSet("wallet accounts", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	count := fs.Int("count", 5, "number of HD sub-accounts to list")
	_ = fs.Parse(args)
	id, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		return err
	}
	if _, err := id.Wallet(); err != nil { // legacy identities have no wallet
		return err
	}
	if *count < 1 {
		return fmt.Errorf("--count must be >= 1")
	}
	secret := id.Secret()
	for i := 0; i < *count; i++ {
		w, err := identity.DeriveWalletAccount(secret, uint32(i))
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "m/44'/501'/%d'/0'  %s\n", i, w.Address)
	}
	return nil
}

func (a *app) cmdWalletExportPhrase(args []string) error {
	fs := flag.NewFlagSet("wallet export-phrase", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	yes := fs.Bool("yes", false, "confirm you want to reveal the secret recovery phrase")
	_ = fs.Parse(args)
	id, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		return err
	}
	w, err := id.Wallet()
	if err != nil {
		return err
	}
	if !*yes {
		fmt.Fprintln(a.errOut, "This prints your 24-word secret recovery phrase. Anyone who sees it controls your identity and wallet.")
		fmt.Fprintln(a.errOut, "Re-run with --yes in a private terminal to reveal it.")
		return fmt.Errorf("refused: re-run with --yes")
	}
	fmt.Fprintln(a.out, w.Mnemonic)
	return nil
}

func (a *app) cmdWalletImportPhrase(args []string) error {
	fs := flag.NewFlagSet("wallet import-phrase", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	phrase := fs.String("phrase", "", "the BIP39 recovery phrase (quoted)")
	yes := fs.Bool("yes", false, "confirm replacing the current identity")
	_ = fs.Parse(args)
	if strings.TrimSpace(*phrase) == "" {
		return fmt.Errorf("--phrase is required (quote the words)")
	}
	secret, err := bip39.MnemonicToEntropy(*phrase)
	if err != nil {
		return err
	}
	if !*yes {
		fmt.Fprintln(a.errOut, "This REPLACES your current owner identity (owner_id and wallet). Machines pinned to the old owner_id must be re-paired.")
		fmt.Fprintln(a.errOut, "Re-run with --yes to proceed.")
		return fmt.Errorf("refused: re-run with --yes")
	}
	id, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		return err
	}
	if err := id.SetFromSecret(secret); err != nil {
		return err
	}
	if err := client.SaveIdentity(*dir, id); err != nil {
		return err
	}
	w, _ := id.Wallet()
	fmt.Fprintf(a.out, "identity restored from phrase\n  wallet:   %s\n  owner_id: %s\n", w.Address, id.OwnerPubHex)
	return nil
}
