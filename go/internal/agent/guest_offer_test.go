// go/internal/agent/guest_offer_test.go — the guest branch of authorizeOffer:
// who a stored grant does and does not let through. No tmux; pure authorization.
package agent

import (
	"bytes"
	"encoding/base64"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

func offerSigner(t *testing.T, fill byte) *identity.Signer {
	t.Helper()
	s, err := identity.DeriveSigner(bytes.Repeat([]byte{fill}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// signedOffer builds the binding + auth a principal presents for an attach. The
// x25519 hex is arbitrary here — the unit path never runs Noise — but must be
// 64 hex so the binding validates.
func signedOffer(t *testing.T, s *identity.Signer, machineID, session, sdp string) signal.SignalMsg {
	t.Helper()
	const x = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	sb, err := s.SignBinding(s.Address[:8], x, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	rec, err := sb.JSON()
	if err != nil {
		t.Fatal(err)
	}
	auth := s.SignAuth(identity.AttachChallenge(session, machineID, sdp))
	return signal.SignalMsg{
		Type: signal.TypeOffer, Session: session, SDP: sdp,
		Binding: rec, Auth: base64.StdEncoding.EncodeToString(auth),
	}
}

func guestRuntime(t *testing.T, owner string) *Runtime {
	t.Helper()
	dir := t.TempDir()
	cfg, err := LoadOrInit(dir, "box", "https://relay.example")
	if err != nil {
		t.Fatal(err)
	}
	cfg.PairedOwners = []string{owner}
	return NewRuntime(cfg, defaultLaunch, nil)
}

func TestAuthorizeOfferGuestAccepted(t *testing.T) {
	owner, guest := offerSigner(t, 0x11), offerSigner(t, 0x22)
	rt := guestRuntime(t, owner.Address)
	sg, err := identity.MintGrant(owner, rt.cfg.MachineID, guest.Address, "main", "ro", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := AddGrant(rt.cfg.Dir, sg); err != nil {
		t.Fatal(err)
	}
	m := signedOffer(t, guest, rt.cfg.MachineID, "sess-1", "sdp")
	auth, err := rt.authorizeOffer(owner.Address, m)
	if err != nil {
		t.Fatalf("valid guest refused: %v", err)
	}
	if auth.grant == nil || auth.grant.GID != sg.GID {
		t.Fatalf("guest not recognized: %+v", auth)
	}
}

func TestAuthorizeOfferOwnerStillWorks(t *testing.T) {
	owner := offerSigner(t, 0x11)
	rt := guestRuntime(t, owner.Address)
	m := signedOffer(t, owner, rt.cfg.MachineID, "sess-owner", "sdp")
	auth, err := rt.authorizeOffer(owner.Address, m)
	if err != nil {
		t.Fatalf("owner attach refused: %v", err)
	}
	if auth.grant != nil {
		t.Fatalf("owner mistaken for a guest: %+v", auth.grant)
	}
}

func TestAuthorizeOfferGuestRejections(t *testing.T) {
	owner, guest, other := offerSigner(t, 0x11), offerSigner(t, 0x22), offerSigner(t, 0x33)
	now := time.Now()

	install := func(rt *Runtime, sg *identity.SignedGrant) {
		if err := AddGrant(rt.cfg.Dir, sg); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("no grant at all", func(t *testing.T) {
		rt := guestRuntime(t, owner.Address)
		m := signedOffer(t, guest, rt.cfg.MachineID, "s", "sdp")
		if _, err := rt.authorizeOffer(owner.Address, m); err == nil {
			t.Fatal("guest with no grant was let in")
		}
	})

	t.Run("expired grant", func(t *testing.T) {
		rt := guestRuntime(t, owner.Address)
		sg, _ := owner.SignGrant(identity.Grant{
			V: 1, Owner: owner.Address, Machine: rt.cfg.MachineID, Guest: guest.Address,
			Scope: "main", Mode: "ro", NB: now.Add(-2 * time.Hour).Unix(), NA: now.Add(-time.Hour).Unix(),
			GID: "aaaabbbbccccddee",
		})
		install(rt, sg)
		m := signedOffer(t, guest, rt.cfg.MachineID, "s", "sdp")
		if _, err := rt.authorizeOffer(owner.Address, m); err == nil {
			t.Fatal("expired grant was accepted")
		}
	})

	t.Run("not yet valid grant", func(t *testing.T) {
		rt := guestRuntime(t, owner.Address)
		sg, _ := owner.SignGrant(identity.Grant{
			V: 1, Owner: owner.Address, Machine: rt.cfg.MachineID, Guest: guest.Address,
			Scope: "main", Mode: "ro", NB: now.Add(time.Hour).Unix(), NA: now.Add(2 * time.Hour).Unix(),
			GID: "aaaabbbbccccdd11",
		})
		install(rt, sg)
		m := signedOffer(t, guest, rt.cfg.MachineID, "s", "sdp")
		if _, err := rt.authorizeOffer(owner.Address, m); err == nil {
			t.Fatal("not-yet-valid grant was accepted")
		}
	})

	t.Run("tombstoned grant", func(t *testing.T) {
		rt := guestRuntime(t, owner.Address)
		sg, _ := identity.MintGrant(owner, rt.cfg.MachineID, guest.Address, "main", "ro", time.Hour, now)
		install(rt, sg)
		if err := TombstoneGrant(rt.cfg.Dir, sg.GID); err != nil {
			t.Fatal(err)
		}
		m := signedOffer(t, guest, rt.cfg.MachineID, "s", "sdp")
		if _, err := rt.authorizeOffer(owner.Address, m); err == nil {
			t.Fatal("tombstoned grant was accepted")
		}
	})

	t.Run("grant for a different owner", func(t *testing.T) {
		rt := guestRuntime(t, owner.Address)
		// Signed by `other`, who is not this machine's owner — VerifyGrant passes
		// against `other`, but findValidGuestGrant requires Owner == the routed owner.
		sg, _ := identity.MintGrant(other, rt.cfg.MachineID, guest.Address, "main", "ro", time.Hour, now)
		install(rt, sg)
		m := signedOffer(t, guest, rt.cfg.MachineID, "s", "sdp")
		if _, err := rt.authorizeOffer(owner.Address, m); err == nil {
			t.Fatal("grant naming a different owner was accepted")
		}
	})

	t.Run("grant for a different machine", func(t *testing.T) {
		rt := guestRuntime(t, owner.Address)
		sg, _ := identity.MintGrant(owner, "some-other-machine", guest.Address, "main", "ro", time.Hour, now)
		install(rt, sg)
		m := signedOffer(t, guest, rt.cfg.MachineID, "s", "sdp")
		if _, err := rt.authorizeOffer(owner.Address, m); err == nil {
			t.Fatal("grant for another machine was accepted")
		}
	})

	t.Run("grant bound to a different guest key", func(t *testing.T) {
		rt := guestRuntime(t, owner.Address)
		sg, _ := identity.MintGrant(owner, rt.cfg.MachineID, other.Address, "main", "ro", time.Hour, now)
		install(rt, sg)
		// `guest` presents the offer, but the grant is bound to `other`.
		m := signedOffer(t, guest, rt.cfg.MachineID, "s", "sdp")
		if _, err := rt.authorizeOffer(owner.Address, m); err == nil {
			t.Fatal("guest used a grant bound to another key")
		}
	})

	t.Run("tampered auth signature", func(t *testing.T) {
		rt := guestRuntime(t, owner.Address)
		sg, _ := identity.MintGrant(owner, rt.cfg.MachineID, guest.Address, "main", "ro", time.Hour, now)
		install(rt, sg)
		m := signedOffer(t, guest, rt.cfg.MachineID, "s", "sdp")
		m.Auth = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x00}, 64))
		if _, err := rt.authorizeOffer(owner.Address, m); err == nil {
			t.Fatal("bad auth signature accepted")
		}
	})

	t.Run("auth over a different sdp", func(t *testing.T) {
		rt := guestRuntime(t, owner.Address)
		sg, _ := identity.MintGrant(owner, rt.cfg.MachineID, guest.Address, "main", "ro", time.Hour, now)
		install(rt, sg)
		m := signedOffer(t, guest, rt.cfg.MachineID, "s", "sdp-A")
		m.SDP = "sdp-B" // signature no longer covers the offered SDP
		if _, err := rt.authorizeOffer(owner.Address, m); err == nil {
			t.Fatal("auth not bound to the offered SDP")
		}
	})

	t.Run("replay of the same session", func(t *testing.T) {
		rt := guestRuntime(t, owner.Address)
		sg, _ := identity.MintGrant(owner, rt.cfg.MachineID, guest.Address, "main", "ro", time.Hour, now)
		install(rt, sg)
		m := signedOffer(t, guest, rt.cfg.MachineID, "sess-replay", "sdp")
		if _, err := rt.authorizeOffer(owner.Address, m); err != nil {
			t.Fatalf("first attach refused: %v", err)
		}
		if _, err := rt.authorizeOffer(owner.Address, m); err == nil {
			t.Fatal("replayed session id accepted twice")
		}
	})
}
