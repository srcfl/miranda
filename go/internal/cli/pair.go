package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
	"golang.org/x/term"

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/client"
	"github.com/srcful/terminal-relay/go/internal/defaults"
	"github.com/srcful/terminal-relay/go/internal/pairing"
	"github.com/srcful/terminal-relay/go/internal/sas"
)

// sasGate carries the user's choices for confirming a pairing safety number
// (SAS) before trust is committed. It mirrors the web client, which only
// persists the peer after a human confirms "Safety number matches". The printed
// number must be VERIFIED before AddMachine/PinOwner, otherwise it is advisory
// and a MITM is never caught.
type sasGate struct {
	confirmSAS string    // --confirm-sas <value>: non-interactive, must equal the computed SAS
	skip       bool      // --yes: non-interactive, accept without comparing (scripted, you-trust-the-channel)
	isTTY      bool      // stdin is an interactive terminal -> prompt
	in         io.Reader // where the interactive y/N answer is read from (os.Stdin in prod)
}

// confirmMatches reports whether a user-supplied SAS value equals the computed
// one, ignoring surrounding whitespace and case (the SAS is printed lowercase
// hex with dashes; a human re-typing it may vary case/spacing). Pure + testable.
func confirmMatches(input, sas string) bool {
	return normalizeSAS(input) == normalizeSAS(sas) && normalizeSAS(sas) != ""
}

func normalizeSAS(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// isAffirmative reports whether an interactive [y/N] answer is a yes. Anything
// that is not an explicit yes is a no (fail closed). Pure + testable.
func isAffirmative(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// confirm decides whether pairing trust may be committed for the given computed
// safety number, and reports the reason on refusal. Order of checks:
//   - --confirm-sas given: persist iff it matches the computed SAS (else refuse).
//   - --yes given: persist (operator vouches for the channel out-of-band).
//   - interactive TTY: prompt "[y/N]"; persist iff the answer is affirmative.
//   - otherwise (no TTY, no flag): refuse — fail closed rather than trust blindly.
//
// It writes the prompt to promptOut so the user sees it on the same stream as the
// printed SAS. The decision logic is otherwise free of process globals so it can
// be unit-tested.
func (g sasGate) confirm(sas string, promptOut io.Writer) (ok bool, reason string) {
	if g.confirmSAS != "" {
		if confirmMatches(g.confirmSAS, sas) {
			return true, ""
		}
		return false, "--confirm-sas does not match the safety number"
	}
	if g.skip {
		return true, ""
	}
	if g.isTTY {
		fmt.Fprint(promptOut, "Do the safety numbers match? [y/N] ")
		line, _ := bufio.NewReader(g.in).ReadString('\n')
		if isAffirmative(line) {
			return true, ""
		}
		return false, "safety number not confirmed"
	}
	return false, "safety number not confirmed: pass --confirm-sas <value> (must match) or --yes in a non-interactive run"
}

type pairMode int

const (
	pairResponder pairMode = iota // no code: make THIS machine pairable (was `mir-agent pair`)
	pairInitiator                 // a code: pair TO the machine that printed it (was `mir pair <code>`)
)

// classifyPair decides direction from the positional args left after flag
// parsing: none = responder, one = initiator with that code, more = error.
func classifyPair(positionals []string) (pairMode, string, error) {
	switch len(positionals) {
	case 0:
		return pairResponder, "", nil
	case 1:
		return pairInitiator, positionals[0], nil
	default:
		return 0, "", fmt.Errorf("usage: mir pair [<code>]  (no code = make this machine pairable; <code> = pair to it)")
	}
}

func (a *app) cmdPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	name := fs.String("name", hostname(), "machine display name (responder)")
	signalURL := fs.String("signal", defaults.SignalURL(), "signaling server base URL (responder)")
	webURL := fs.String("web", defaults.WebURL(), "browser SPA base URL the QR opens (responder)")
	confirmSAS := fs.String("confirm-sas", "", "non-interactive: the expected safety number; pairing is committed only if it matches the computed one")
	yes := fs.Bool("yes", false, "non-interactive: commit pairing without comparing the safety number (only if you trust the channel out-of-band)")
	_ = fs.Parse(args)

	mode, code, err := classifyPair(fs.Args())
	if err != nil {
		return err
	}
	gate := sasGate{
		confirmSAS: *confirmSAS,
		skip:       *yes,
		isTTY:      term.IsTerminal(int(os.Stdin.Fd())),
		in:         os.Stdin,
	}
	if mode == pairInitiator {
		return a.pairInitiate(*dir, code, gate)
	}
	return a.pairRespond(*dir, *name, *signalURL, *webURL, gate)
}

// pairInitiate is the body of the old client `mir pair <code>`: pair TO a machine
// that printed a code, learning its host key + name and pinning it locally. The
// safety number is shown and CONFIRMED before the machine is persisted — matching
// the web client, which only stores the peer after "Safety number matches".
func (a *app) pairInitiate(dir, codeStr string, gate sasGate) error {
	signalURL, token, err := pairing.DecodeCode(codeStr)
	if err != nil {
		return err
	}
	idn, err := client.LoadOrCreateIdentity(dir)
	if err != nil {
		return err
	}
	w, err := idn.Wallet()
	if err != nil {
		return fmt.Errorf("pairing needs a wallet; run `mir keygen --wallet`: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	mc, closeConn, err := pairing.DialPair(ctx, signalURL, pairing.RoomID(token))
	if err != nil {
		return err
	}
	defer closeConn()
	info, binding, err := pairing.RunInitiator(ctx, mc, token, w)
	if err != nil {
		return err
	}
	m := client.Machine{Name: info.Name, MachineID: info.MachineID, HostPubHex: info.HostPubHex, SignalURL: signalURL}

	// Show the safety number FIRST, then require confirmation BEFORE persisting —
	// otherwise the printed number is advisory and a MITM is never caught.
	safety := sas.FromBinding(binding)
	fmt.Fprintf(a.out, "  safety number: %s  (must match the machine's)\n", safety)
	if ok, reason := gate.confirm(safety, a.out); !ok {
		return fmt.Errorf("pairing cancelled: %s", reason)
	}
	if err := client.AddMachine(dir, m); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "✓ paired machine %q — try: mir attach %s\n", m.Name, m.Name)
	return nil
}

// pairRespond is the body of the old agent `mir-agent pair`: make THIS machine
// pairable — print a code + QR and wait for an owner to pair, then pin them. The
// owner is trusted only AFTER the safety number is shown and confirmed, matching
// the web client's "Safety number matches" gate.
func (a *app) pairRespond(dir, name, signalURL, webURL string, gate sasGate) error {
	cfg, err := agent.LoadOrInit(dir, name, signalURL)
	if err != nil {
		return err
	}
	token := pairing.NewToken()
	code := pairing.EncodeCode(signalURL, token)
	pairURL := strings.TrimRight(webURL, "/") + "/#" + code

	fmt.Fprintln(a.out, "Pair this machine:")
	fmt.Fprint(a.out, "\n  📱 Scan with your phone's camera — it opens the app ready to pair:\n\n")
	qrterminal.GenerateHalfBlock(pairURL, qrterminal.L, a.out)
	fmt.Fprintf(a.out, "\n  …or open: %s\n", pairURL)
	fmt.Fprintf(a.out, "  …or on the CLI:  mir pair %s\n", code)
	fmt.Fprintf(a.out, "\nwaiting for pairing (5 min)…\n")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	mc, closeConn, err := pairing.DialPair(ctx, signalURL, pairing.RoomID(token))
	if err != nil {
		return err
	}
	defer closeConn()

	info := pairing.AgentInfo{HostPubHex: cfg.HostPubHex, MachineID: cfg.MachineID, Name: cfg.MachineName}
	wallet, binding, err := pairing.RunResponder(ctx, mc, token, info)
	if err != nil {
		return err
	}

	// Show the safety number FIRST, then require confirmation BEFORE pinning the
	// owner — otherwise the printed number is advisory and a MITM is never caught.
	safety := sas.FromBinding(binding)
	fmt.Fprintf(a.out, "  safety number: %s  (must match the client's)\n", safety)
	if ok, reason := gate.confirm(safety, a.out); !ok {
		return fmt.Errorf("pairing cancelled: %s", reason)
	}
	if err := agent.PinOwner(dir, wallet); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "✓ paired — trusting wallet %s…\n", wallet[:16])
	return nil
}
