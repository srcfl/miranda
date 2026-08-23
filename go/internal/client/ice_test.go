package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveICEPrefersTURNOmitsGoogleSTUN(t *testing.T) {
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

	got, err := ResolveICE(context.Background(), srv.URL, []string{defaultSTUN})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("ICE list = %#v, want one TURN server", got)
	}
	if len(got[0].URLs) != 1 || got[0].URLs[0] != "turn:relay.example:3478" {
		t.Fatalf("TURN URLs = %v", got[0].URLs)
	}
	if got[0].Username != "u" || got[0].Credential != "p" {
		t.Fatalf("TURN creds = %q/%q", got[0].Username, got[0].Credential)
	}
	for _, s := range got {
		for _, u := range s.URLs {
			if u == defaultSTUN {
				t.Fatalf("Google STUN must not appear when TURN is offered: %v", got)
			}
		}
	}
}

func TestResolveICEFallsBackToSTUNWhenTURNMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	got, err := ResolveICE(context.Background(), srv.URL, []string{defaultSTUN})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].URLs) != 1 || got[0].URLs[0] != defaultSTUN {
		t.Fatalf("STUN fallback = %#v", got)
	}
}
