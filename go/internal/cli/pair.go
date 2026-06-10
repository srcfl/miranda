package cli

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/client"
	"github.com/srcful/terminal-relay/go/internal/defaults"
	"github.com/srcful/terminal-relay/go/internal/pairing"
	"github.com/srcful/terminal-relay/go/internal/sas"
)

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
	_ = fs.Parse(args)

	mode, code, err := classifyPair(fs.Args())
	if err != nil {
		return err
	}
	if mode == pairInitiator {
		return a.pairInitiate(*dir, code)
	}
	return a.pairRespond(*dir, *name, *signalURL, *webURL)
}

// pairInitiate is the body of the old client `mir pair <code>`: pair TO a machine
// that printed a code, learning its host key + name and pinning it locally.
func (a *app) pairInitiate(dir, codeStr string) error {
	signalURL, token, err := pairing.DecodeCode(codeStr)
	if err != nil {
		return err
	}
	idn, err := client.LoadOrCreateIdentity(dir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	mc, closeConn, err := pairing.DialPair(ctx, signalURL, pairing.RoomID(token))
	if err != nil {
		return err
	}
	defer closeConn()
	info, binding, err := pairing.RunInitiator(ctx, mc, token, idn.OwnerPub())
	if err != nil {
		return err
	}
	m := client.Machine{Name: info.Name, MachineID: info.MachineID, HostPubHex: info.HostPubHex, SignalURL: signalURL}
	if err := client.AddMachine(dir, m); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "✓ paired machine %q — try: mir attach %s\n", m.Name, m.Name)
	fmt.Fprintf(a.out, "  safety number: %s  (must match the machine's)\n", sas.FromBinding(binding))
	return nil
}

// pairRespond is the body of the old agent `mir-agent pair`: make THIS machine
// pairable — print a code + QR and wait for an owner to pair, then pin them.
func (a *app) pairRespond(dir, name, signalURL, webURL string) error {
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
	ownerPub, binding, err := pairing.RunResponder(ctx, mc, token, info)
	if err != nil {
		return err
	}
	ownerHex := hex.EncodeToString(ownerPub)
	if err := agent.PinOwner(dir, ownerHex); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "✓ paired — trusting owner %s…\n", ownerHex[:16])
	fmt.Fprintf(a.out, "  safety number: %s  (must match the client's)\n", sas.FromBinding(binding))
	return nil
}
