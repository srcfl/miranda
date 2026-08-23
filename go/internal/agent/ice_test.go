package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/peer"
)

func TestIceForOmitsSTUNWhenTURNOffered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/turn-credentials" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"username": "u",
			"password": "p",
			"urls":     []string{"turn:relay.example:3478"},
		})
	}))
	defer srv.Close()

	rt := NewRuntime(&Config{SignalURL: srv.URL}, []string{"sh"}, []peer.ICEServer{
		{URLs: []string{"stun:stun.l.google.com:19302"}},
	})
	got := rt.iceFor(context.Background())
	if len(got) != 1 || len(got[0].URLs) != 1 || got[0].URLs[0] != "turn:relay.example:3478" {
		t.Fatalf("ICE with TURN = %#v, want TURN only", got)
	}
	for _, s := range got {
		for _, u := range s.URLs {
			if u == "stun:stun.l.google.com:19302" {
				t.Fatalf("Google STUN must not appear when TURN is offered: %#v", got)
			}
		}
	}
}

func TestIceForFallsBackToStaticWhenTURNMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	stun := []peer.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}
	rt := NewRuntime(&Config{SignalURL: srv.URL}, []string{"sh"}, stun)
	got := rt.iceFor(context.Background())
	if len(got) != 1 || got[0].URLs[0] != "stun:stun.l.google.com:19302" {
		t.Fatalf("STUN fallback = %#v", got)
	}
}
