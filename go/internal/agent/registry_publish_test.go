// go/internal/agent/registry_publish_test.go
package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/srcful/terminal-relay/go/internal/identity"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

// TestRegistryBlobOpens proves the agent seals a device record under K_reg that a
// wallet-holder can open: registryBlob() returns base64(nonce||ct) which, decoded
// and run through OpenRecord with the wallet's K_reg and the machine_id AAD, yields
// the {name,host_pub,...} JSON. A wrong machine_id (AAD) must fail to open.
func TestRegistryBlobOpens(t *testing.T) {
	secret := bytes.Repeat([]byte{0x42}, 32)
	cfg := &Config{
		MachineID:   "machine-xyz",
		MachineName: "fredde-laptop",
		HostPubHex:  "deadbeefcafef00d",
		SignalURL:   "https://relay.example",
	}
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	rt.WalletSecret = secret
	rt.WalletAddress = "WalletAddrBase58"

	b64, err := rt.registryBlob()
	if err != nil {
		t.Fatalf("registryBlob: %v", err)
	}
	blob, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}

	key, err := identity.RegistryKey(secret)
	if err != nil {
		t.Fatalf("RegistryKey: %v", err)
	}
	pt, err := identity.OpenRecord(key, blob, cfg.MachineID)
	if err != nil {
		t.Fatalf("OpenRecord (right machine_id): %v", err)
	}
	var rec map[string]any
	if err := json.Unmarshal(pt, &rec); err != nil {
		t.Fatalf("record JSON: %v", err)
	}
	if rec["name"] != cfg.MachineName {
		t.Fatalf("record name = %v, want %q", rec["name"], cfg.MachineName)
	}
	if rec["host_pub"] != cfg.HostPubHex {
		t.Fatalf("record host_pub = %v, want %q", rec["host_pub"], cfg.HostPubHex)
	}
	if rec["signal_url"] != cfg.SignalURL {
		t.Fatalf("record signal_url = %v, want %q", rec["signal_url"], cfg.SignalURL)
	}

	// AAD is machine_id: opening under a different machine_id must fail.
	if _, err := identity.OpenRecord(key, blob, "other-machine"); err == nil {
		t.Fatal("OpenRecord with wrong machine_id (AAD) should fail, but succeeded")
	}
}

// TestRegistryBlobLegacyNoWallet proves a wallet-less Runtime never produces a
// blob (legacy mir up publishes nothing).
func TestRegistryBlobLegacyNoWallet(t *testing.T) {
	cfg := &Config{MachineID: "m1", MachineName: "legacy"}
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	if _, err := rt.registryBlob(); err == nil {
		t.Fatal("registryBlob with no WalletSecret should error, but succeeded")
	}
}

// TestServeOncePublishesRegistry proves that when serving the self-wallet owner,
// the agent's FIRST message on the live registration is a TypeRegistry whose blob
// opens to the device record. A fake relay captures the first frame.
func TestServeOncePublishesRegistry(t *testing.T) {
	secret := bytes.Repeat([]byte{0x55}, 32)
	wallet := "SelfWalletBase58"

	first := make(chan signal.SignalMsg, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		var m signal.SignalMsg
		if json.Unmarshal(data, &m) == nil {
			first <- m
		}
		// hold the registration open until the test ends
		_, _, _ = c.Read(r.Context())
	}))
	defer srv.Close()

	cfg := &Config{
		SignalURL:    srv.URL,
		MachineID:    "machine-pub-1",
		MachineName:  "publisher",
		HostPubHex:   "0011223344556677",
		PairedOwners: []string{wallet},
	}
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	rt.WalletSecret = secret
	rt.WalletAddress = wallet

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _, _, _ = rt.serveOnce(ctx, wallet) }()

	select {
	case m := <-first:
		if m.Type != signal.TypeRegistry {
			t.Fatalf("first message type = %q, want %q", m.Type, signal.TypeRegistry)
		}
		blob, err := base64.StdEncoding.DecodeString(m.Registry)
		if err != nil {
			t.Fatalf("registry base64: %v", err)
		}
		key, err := identity.RegistryKey(secret)
		if err != nil {
			t.Fatalf("RegistryKey: %v", err)
		}
		pt, err := identity.OpenRecord(key, blob, cfg.MachineID)
		if err != nil {
			t.Fatalf("OpenRecord: %v", err)
		}
		var rec map[string]any
		if err := json.Unmarshal(pt, &rec); err != nil {
			t.Fatalf("record JSON: %v", err)
		}
		if rec["name"] != cfg.MachineName || rec["host_pub"] != cfg.HostPubHex {
			t.Fatalf("record = %v, want name=%q host_pub=%q", rec, cfg.MachineName, cfg.HostPubHex)
		}
	case <-ctx.Done():
		t.Fatal("relay never received the first registry message")
	}
}

// TestServeOnceNoPublishForOtherOwner proves the agent does NOT publish a registry
// blob when serving an owner that is not its own wallet (it lacks that wallet's
// K_reg). For a non-self owner the first frame must not be a registry message.
func TestServeOnceNoPublishForOtherOwner(t *testing.T) {
	secret := bytes.Repeat([]byte{0x55}, 32)

	got := make(chan string, 1) // first message type, or "" if the conn closed without one
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		typ := ""
		_, data, err := c.Read(r.Context())
		if err == nil {
			var m signal.SignalMsg
			if json.Unmarshal(data, &m) == nil {
				typ = m.Type
			}
		}
		got <- typ
		_, _, _ = c.Read(r.Context())
	}))
	defer srv.Close()

	cfg := &Config{
		SignalURL:    srv.URL,
		MachineID:    "machine-pub-1",
		MachineName:  "publisher",
		HostPubHex:   "0011223344556677",
		PairedOwners: []string{"OtherOwner", "SelfWallet"},
	}
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	rt.WalletSecret = secret
	rt.WalletAddress = "SelfWallet"

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	go func() { _, _, _ = rt.serveOnce(ctx, "OtherOwner") }()

	select {
	case typ := <-got:
		if typ == signal.TypeRegistry {
			t.Fatal("agent published a registry blob for a non-self owner")
		}
	case <-ctx.Done():
		// no message at all is also correct (the agent only sends on offers).
	}
}
