package sas

import (
	"strings"
	"testing"
)

func TestFromBindingIsStableAndFormatted(t *testing.T) {
	b := []byte{1, 2, 3, 4, 5}
	a := FromBinding(b)
	if a != FromBinding(b) {
		t.Fatal("not deterministic")
	}
	// 4 groups of 4 hex, dash-separated.
	parts := strings.Split(a, "-")
	if len(parts) != 4 {
		t.Fatalf("expected 4 groups, got %q", a)
	}
	for _, p := range parts {
		if len(p) != 4 {
			t.Fatalf("group %q is not 4 hex chars (%q)", p, a)
		}
	}
}

func TestDifferentBindingsDifferentSAS(t *testing.T) {
	if FromBinding([]byte("alice")) == FromBinding([]byte("mallory")) {
		t.Fatal("distinct bindings must give distinct safety numbers")
	}
}
