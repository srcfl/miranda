package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

func TestProvisionOwnerStoresOpaqueRegistryRecord(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrInit(dir, "workstation", "https://relay.example"); err != nil {
		t.Fatal(err)
	}
	const owner = "owner-id"
	const blob = "opaque-owner-encrypted-record"
	const registrationAuth = "public-owner-authorization"
	if err := ProvisionOwner(dir, owner, blob, registrationAuth); err != nil {
		t.Fatal(err)
	}
	owners, err := ReloadOwners(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0] != owner {
		t.Fatalf("owners = %v", owners)
	}
	if got := RegistryForOwner(dir, owner); got != blob {
		t.Fatalf("registry = %q, want %q", got, blob)
	}
	if got := RegistrationAuthForOwner(dir, owner); got != registrationAuth {
		t.Fatalf("registration auth = %q, want %q", got, registrationAuth)
	}
	data, err := os.ReadFile(configPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "owner_priv") || strings.Contains(string(data), "wallet_address") {
		t.Fatal("agent config unexpectedly contains owner identity material")
	}
}

func TestServeOncePublishesProvisionedOpaqueRecord(t *testing.T) {
	first := make(chan signal.SignalMsg, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		_, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		var m signal.SignalMsg
		if json.Unmarshal(data, &m) == nil {
			first <- m
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg, err := LoadOrInit(dir, "publisher", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	const owner = "owner-id"
	const blob = "opaque-owner-encrypted-record"
	if err := ProvisionOwner(dir, owner, blob, ""); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _, _, _ = rt.serveOnce(ctx, owner) }()

	select {
	case m := <-first:
		if m.Type != signal.TypeRegistry || m.Registry != blob {
			t.Fatalf("first message = %+v", m)
		}
	case <-ctx.Done():
		t.Fatal("relay never received provisioned registry record")
	}
}

func TestServeOnceWithoutProvisionDoesNotPublishRegistry(t *testing.T) {
	got := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		readCtx, cancel := context.WithTimeout(r.Context(), 150*time.Millisecond)
		defer cancel()
		if _, _, err := c.Read(readCtx); err == nil {
			got <- struct{}{}
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg, err := LoadOrInit(dir, "publisher", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	go func() { _, _, _ = rt.serveOnce(ctx, "owner-without-record") }()
	select {
	case <-got:
		t.Fatal("agent published an unprovisioned registry record")
	case <-ctx.Done():
	}
}
