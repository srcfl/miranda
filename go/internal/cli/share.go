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
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/sas"
)

// shareIsTTY is a seam for tests; sharing a shell always asks a person.
var shareIsTTY = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

const inviteWindow = 5 * time.Minute

func (a *app) cmdShare(args []string) error {
	// Subcommands first (they take no mint flags): ls / revoke.
	if len(args) > 0 {
		switch args[0] {
		case "ls", "list":
			return a.cmdShareLs(args[1:])
		case "revoke":
			return a.cmdShareRevoke(args[1:])
		}
	}
	fs := flag.NewFlagSet("share", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	ttl := fs.Duration("ttl", identity.GrantDefaultTTL, "how long the guest's access lasts (max 24h)")
	write := fs.Bool("write", false, "give the guest a writable shell (full control as your user)")
	session := fs.String("session", "main", "tmux session the share covers")
	webURL := fs.String("web", defaults.WebURL(), "browser SPA base URL the invite link opens")
	ice := iceFlags(fs)
	_ = fs.Parse(args)
	if len(fs.Args()) != 1 {
		return fmt.Errorf("usage: mir share <machine> [--ttl 1h] [--write] [--session main] | mir share ls | mir share revoke <id>")
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
	return a.shareResolved(context.Background(), *dir, idn, m, *ttl, *write, *session, *webURL, ice())
}

// shareResolved runs the mint ceremony for an already-resolved machine: the
// write consent, the invite, the room, the safety number, the grant, delivery
// to the agent, and the local record. The overview's `s` action calls it too,
// with its own stdin reader.
func (a *app) shareResolved(parent context.Context, dir string, idn *client.Identity, m client.Machine, ttl time.Duration, write bool, session, webURL string, ice []peer.ICEServer) error {
	mode := "ro"
	if write {
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
	joinURL := strings.TrimRight(webURL, "/") + "/#join-" + code
	expires := time.Now().Add(ttl)
	fmt.Fprintf(a.out, "Share %q — %s access until %s.\n", m.Name, modeWord(mode), expires.Format("15:04"))
	fmt.Fprint(a.out, "\n  📱 Have your guest scan this:\n\n")
	qrterminal.GenerateHalfBlock(joinURL, qrterminal.L, a.out)
	fmt.Fprintf(a.out, "\n  …or open: %s\n", joinURL)
	fmt.Fprintf(a.out, "  …or on the CLI:  %s join %s\n", a.binary, code)
	fmt.Fprintf(a.out, "\nwaiting for your guest (%d min)…\n", int(inviteWindow.Minutes()))

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
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
	sg, err := identity.MintGrant(signer, m.MachineID, guestWallet, session, mode, ttl, time.Now())
	if err != nil {
		return err
	}
	record, err := sg.JSON()
	if err != nil {
		return err
	}

	// The agent learns first: a guest never holds a grant the machine does not
	// know about. Failure here means nothing was shared, full stop.
	amc, sess, cleanup, err := client.Attach(ctx, m, idn, ice)
	if err != nil {
		return fmt.Errorf("%q is unreachable (%v) — nothing was shared; bring it online and mint a new invite", m.Name, err)
	}
	defer cleanup()
	if err := client.GrantOverSession(ctx, amc, sess, record, sg.GID, 8*time.Second); err != nil {
		return fmt.Errorf("%q did not accept the share (%v) — it may run an agent from before sharing; run `%s update` on it and mint a new invite. Nothing was shared", m.Name, err, a.binary)
	}
	// Record the mint locally so `mir share ls`/`revoke` can find it. The agent
	// holds the enforced copy either way.
	if err := client.SaveOwnerShare(dir, record, m.Name); err != nil {
		fmt.Fprintf(a.errOut, "warning: could not record the share locally: %v\n", err)
	}
	if err := mc.Send([]byte(record)); err != nil {
		return fmt.Errorf("the machine accepted the share but the guest disconnected — the access expires %s on its own, or end it now: `%s share revoke %s`", expires.Format("15:04"), a.binary, sg.GID[:8])
	}
	fmt.Fprintf(a.out, "✓ shared %q with %.8s… — %s, expires %s (id %s)\n", m.Name, guestWallet, modeWord(mode), expires.Format("15:04"), sg.GID[:8])
	fmt.Fprintf(a.out, "  end it early: %s share revoke %s\n", a.binary, sg.GID[:8])
	return nil
}

// expiryPhrase renders a grant's clock state for lists ("expires in 42 min").
func expiryPhrase(na int64, revoked bool, now time.Time) string {
	if revoked {
		return "revoked"
	}
	left := time.Unix(na, 0).Sub(now)
	switch {
	case left <= 0:
		return "expired"
	case left < time.Minute:
		return "expires in under a minute"
	case left < time.Hour:
		return fmt.Sprintf("expires in %d min", int(left.Minutes()))
	default:
		return fmt.Sprintf("expires in %dh %02dmin", int(left.Hours()), int(left.Minutes())%60)
	}
}

// cmdShareLs lists this device's recorded mints.
func (a *app) cmdShareLs(args []string) error {
	fs := flag.NewFlagSet("share ls", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	_ = fs.Parse(args)
	shares, err := client.ListOwnerShares(*dir)
	if err != nil {
		return err
	}
	if len(shares) == 0 {
		fmt.Fprintf(a.out, "no shares minted from this device — `%s share <machine>` invites someone in\n", a.binary)
		return nil
	}
	now := time.Now()
	for _, s := range shares {
		fmt.Fprintf(a.out, "%s  %-16s %-10s %-8s %s\n",
			s.Grant.GID[:8], s.MachineName, modeWord(s.Grant.Mode), s.Grant.Scope, expiryPhrase(s.Grant.NA, s.Revoked, now))
	}
	return nil
}

// cmdShareRevoke ends one share now: deliver the tombstone to the machine's
// agent and mark the local record. Revocation is agent-local (spec §4), so an
// unreachable machine means the revoke has not happened yet — but an offline
// machine cannot serve the guest either, which the copy says plainly.
func (a *app) cmdShareRevoke(args []string) error {
	fs := flag.NewFlagSet("share revoke", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	ice := iceFlags(fs)
	_ = fs.Parse(args)
	if len(fs.Args()) != 1 {
		return fmt.Errorf("usage: %s share revoke <id>   (ids: `%s share ls`)", a.binary, a.binary)
	}
	share, err := client.ResolveShareGID(*dir, fs.Arg(0))
	if err != nil {
		return err
	}
	gid := share.Grant.GID
	if share.Revoked {
		fmt.Fprintf(a.out, "share %s is already revoked\n", gid[:8])
		return nil
	}
	idn, err := a.identity(*dir)
	if err != nil {
		return err
	}
	if err := a.requireRootedIdentity(idn); err != nil {
		return err
	}
	machines, err := a.resolveMachines(context.Background(), *dir, []string{share.MachineName}, idn)
	if err != nil {
		return err
	}
	m := machines[0]

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mc, sess, cleanup, err := client.Attach(ctx, m, idn, ice())
	if err != nil {
		return fmt.Errorf("%q is unreachable (%v) — the share is NOT revoked yet, but while the machine is offline the guest cannot reach it either; re-run when it is back online. It expires %s on its own",
			m.Name, err, time.Unix(share.Grant.NA, 0).Format("15:04"))
	}
	defer cleanup()
	if err := client.RevokeGrantOverSession(ctx, mc, sess, gid, 8*time.Second); err != nil {
		return fmt.Errorf("%q did not confirm the revoke (%v) — it may run an older agent; run `%s update` on it and re-run", m.Name, err, a.binary)
	}
	if err := client.MarkShareRevoked(*dir, gid); err != nil {
		fmt.Fprintf(a.errOut, "warning: revoked on the machine, but the local record could not be updated: %v\n", err)
	}
	fmt.Fprintf(a.out, "✓ revoked %s — %q dropped any live guest immediately\n", gid[:8], m.Name)
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
	fmt.Fprintf(a.out, "  open it: %s attach %s   (or the web app)\n", a.binary, info.Name)
	return nil
}
