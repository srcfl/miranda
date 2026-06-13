package client

import "testing"

// attachLocators encodes the LAN-first-then-relay default and the --relay-only
// escape. The transport paths themselves are covered by the locator/quicmsg/agent
// tests; this pins the composition decision.
func TestAttachLocators(t *testing.T) {
	def := attachLocators(false)
	if len(def) != 2 {
		t.Fatalf("default: want 2 locators (LAN, relay), got %d", len(def))
	}
	if _, ok := def[0].(lanLocator); !ok {
		t.Errorf("default: locator[0] = %T, want lanLocator (LAN tried first)", def[0])
	}
	if _, ok := def[1].(relayLocator); !ok {
		t.Errorf("default: locator[1] = %T, want relayLocator", def[1])
	}

	only := attachLocators(true)
	if len(only) != 1 {
		t.Fatalf("relayOnly: want 1 locator, got %d", len(only))
	}
	if _, ok := only[0].(relayLocator); !ok {
		t.Errorf("relayOnly: locator[0] = %T, want relayLocator (LAN skipped)", only[0])
	}
}
