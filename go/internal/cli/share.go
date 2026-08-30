// go/internal/cli/share.go
//
// G1b: `mir share` mints an owner-signed guest grant over the one-time pair
// room and installs it on the agent; `mir join` claims an invite. The room and
// the SAS ceremony are pairing's (§2 of the G1 spec); the trust decision is
// the OWNER's alone — the guest just joined and risks nothing — so share has
// no --yes and refuses to run without a terminal.
package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"golang.org/x/term"

	"github.com/srcful/terminal-relay/go/internal/client"
	"github.com/srcful/terminal-relay/go/internal/defaults"
	"github.com/srcful/terminal-relay/go/internal/identity"
	"github.com/srcful/terminal-relay/go/internal/pairing"
	"github.com/srcful/terminal-relay/go/internal/sas"
)

// shareIsTTY is a seam for tests; sharing a shell always asks a person.
var shareIsTTY = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

const inviteWindow = 5 * time.Minute

func (a *app) cmdShare(args []string) error {
	fs := flag.NewFlagSet("share", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	ttl := fs.Duration("ttl", identity.GrantDefaultTTL, "how long the guest's access lasts (max 24h)")
	write := fs.Bool("write", false, "give the guest a writable shell (full control as your user)")
	session := fs.String("session", "main", "tmux session the share covers")
	webURL := fs.String("web", defaults.WebURL(), "browser SPA base URL the invite link opens")
	ice := iceFlags(fs)
	_ = fs.Parse(args)
	if len(fs.Args()) != 1 {
		return fmt.Errorf("usage: mir share <machine> [--ttl 1h] [--write] [--session main]")
	}
	name := fs.Arg(0)
	if !shareIsTTY() {
		return fmt.Errorf("sharing needs a person at the terminal to compare the safety number — there is no --yes; run `%s share` interactively", a.binary)
	}

	idn, err := a.identity(*dir)
	if err != nil {
		return err
	}
	if err := a.requireRootedIdentity(idn); err != nil {
		return err
	}
	machines, err := a.resolveMachines(context.Background(), *dir, []string{name}, idn)
	if err != nil {
		return err
	}
	m := machines[0]

	mode := "ro"
	if *write {
		mode = "rw"
		fmt.Fprintf(a.out, "Write access means full control of %q as your user — the guest can run anything you can.\n", m.Name)
		fmt.Fprintf(a.out, "Type the machine name (%s) to confirm write access: ", m.Name)
		line, _ := bufio.NewReader(a.in).ReadString('\n')
		if strings.TrimSpace(line) != m.Name {
			fmt.Fprintln(a.out, "write share cancelled — the name didn't match; nothing was shared.")
			return nil
		}
	}

	token, err := pairing.NewToken()
	if err != nil {
		return err
	}
	code := pairing.EncodeCode(m.SignalURL, token)
	joinURL := strings.TrimRight(*webURL, "/") + "/#join-" + code
	expires := time.Now().Add(*ttl)
	fmt.Fprintf(a.out, "Share %q — %s access until %s.\n", m.Name, modeWord(mode), expires.Format("15:04"))
	fmt.Fprint(a.out, "\n  📱 Have your guest scan this:\n\n")
	qrterminal.GenerateHalfBlock(joinURL, qrterminal.L, a.out)
	fmt.Fprintf(a.out, "\n  …or open: %s\n", joinURL)
	fmt.Fprintf(a.out, "  …or on the CLI:  %s join %s\n", a.binary, code)
	fmt.Fprintf(a.out, "\nwaiting for your guest (%d min)…\n", int(inviteWindow.Minutes()))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, inviteWindow)
	defer cancel()

	mc, closeConn, err := pairing.DialPair(ctx, m.SignalURL, pairing.RoomID(token))
	if err != nil {
		return humanRelayErr(a.binary, err)
	}
	defer closeConn()
	started, err := pairing.StartResponder(ctx, mc, token, pairing.AgentInfo{
		HostPubHex: m.HostPubHex, MachineID: m.MachineID, Name: m.Name,
	})
	if err != nil {
		return humanPairHandshakeErr(err)
	}
	guestWallet, _, err := started.Finish(ctx)
	if err != nil {
		return humanPairHandshakeErr(err)
	}
	// §2(3): the guest also presents its signed transport binding; a claim whose
	// binding doesn't verify against the claimed guest key never reaches the
	// safety-number prompt.
	bindingBytes, err := mc.Recv(ctx)
	if err != nil {
		return fmt.Errorf("the guest disconnected before finishing — mint a new invite to try again (cause: %v)", err)
	}
	sb, err := identity.ParseSignedBinding(bindingBytes)
	if err == nil {
		err = identity.VerifyBinding(sb)
	}
	if err != nil || sb.Wallet != guestWallet {
		return fmt.Errorf("the guest's device binding did not verify — mint a new invite and try again")
	}

	fmt.Fprintf(a.out, "  safety number: %s\n", sas.FromBinding(started.Binding))
	fmt.Fprintf(a.out, "  guest id:      %.8s…\n", guestWallet)
	fmt.Fprintln(a.out, "Ask the guest to read their safety number aloud — share only if it matches.")
	fmt.Fprintf(a.out, "Share %q (%s) with this guest? [y/N] ", m.Name, modeWord(mode))
	line, _ := bufio.NewReader(a.in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
	default:
		fmt.Fprintln(a.out, "declined — nothing was shared.")
		return nil
	}

	signer, err := idn.Signer()
	if err != nil {
		return err
	}
	sg, err := identity.MintGrant(signer, m.MachineID, guestWallet, *session, mode, *ttl, time.Now())
	if err != nil {
		return err
	}
	record, err := sg.JSON()
	if err != nil {
		return err
	}

	// The agent learns first: a guest never holds a grant the machine does not
	// know about. Failure here means nothing was shared, full stop.
	amc, sess, cleanup, err := client.Attach(ctx, m, idn, ice())
	if err != nil {
		return fmt.Errorf("%q is unreachable (%v) — nothing was shared; bring it online and mint a new invite", m.Name, err)
	}
	defer cleanup()
	if err := client.GrantOverSession(ctx, amc, sess, record, sg.GID, 8*time.Second); err != nil {
		return fmt.Errorf("%q did not accept the share (%v) — it may run an agent from before sharing; run `%s update` on it and mint a new invite. Nothing was shared", m.Name, err, a.binary)
	}
	if err := mc.Send([]byte(record)); err != nil {
		return fmt.Errorf("the machine accepted the share but the guest disconnected — the access expires %s on its own, or revoke id %s once share revoke ships", expires.Format("15:04"), sg.GID)
	}
	fmt.Fprintf(a.out, "✓ shared %q with %.8s… — %s, expires %s (id %s)\n", m.Name, guestWallet, modeWord(mode), expires.Format("15:04"), sg.GID)
	return nil
}

func modeWord(mode string) string {
	if mode == "rw" {
		return "read-write"
	}
	return "read-only"
}

func (a *app) cmdJoin(args []string) error {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	_ = fs.Parse(args)
	if len(fs.Args()) != 1 {
		return fmt.Errorf("usage: mir join <code>")
	}
	signalURL, token, err := pairing.DecodeCode(fs.Arg(0))
	if err != nil {
		return err
	}
	idn, err := a.identity(*dir)
	if err != nil {
		return err
	}
	if err := a.requireRootedIdentity(idn); err != nil {
		return err
	}
	w, err := idn.Signer()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), inviteWindow)
	defer cancel()
	mc, closeConn, err := pairing.DialPair(ctx, signalURL, pairing.RoomID(token))
	if err != nil {
		return humanRelayErr(a.binary, err)
	}
	defer closeConn()
	started, err := pairing.StartInitiator(ctx, mc, token, w)
	if err != nil {
		return humanPairHandshakeErr(err)
	}
	info := started.Info
	fmt.Fprintf(a.out, "joining %q…\n", info.Name)
	fmt.Fprintf(a.out, "  safety number: %s  (read it aloud to the person sharing)\n", sas.FromBinding(started.Binding))
	// The guest risks nothing by proceeding — the owner holds the y/N. Prove the
	// guest key (msg3), present the transport binding, then wait for a verdict.
	if err := started.Finish(nil); err != nil {
		return err
	}
	if err := mc.Send([]byte(idn.BindingJSON)); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "waiting for the owner to approve…")

	verdict, err := mc.Recv(ctx)
	if err != nil {
		return fmt.Errorf("the invite was declined or expired — nothing was set up")
	}
	sg, err := identity.ParseSignedGrant(verdict)
	if err == nil {
		err = identity.VerifyGrant(sg)
	}
	if err != nil {
		return fmt.Errorf("the share record did not verify — ask for a new invite (cause: %v)", err)
	}
	if sg.Guest != idn.OwnerID || sg.Machine != info.MachineID {
		return fmt.Errorf("the share was minted for a different device or machine — ask for a new invite")
	}

	if err := client.AddMachine(*dir, client.Machine{
		Name: info.Name, MachineID: info.MachineID, HostPubHex: info.HostPubHex, SignalURL: signalURL,
		Owner: sg.Owner, // a guest entry: attach routes under the machine owner, authenticates as us
	}); err != nil {
		return err
	}
	record, _ := sg.JSON()
	if err := client.SaveGuestGrant(*dir, sg.GID, record); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "✓ joined %q as a guest — %s access until %s\n", info.Name, modeWord(sg.Mode), time.Unix(sg.NA, 0).Format("15:04"))
	return nil
}
