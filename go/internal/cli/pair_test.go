package cli

import (
	"io"
	"strings"
	"testing"
)

func TestConfirmMatches(t *testing.T) {
	const s = "a3f1-9c2b-77de-4051"
	cases := []struct {
		in   string
		want bool
	}{
		{s, true},
		{"  " + s + "\n", true},        // surrounding whitespace ignored
		{strings.ToUpper(s), true},     // case-insensitive
		{"a3f1-9c2b-77de-4050", false}, // one digit off
		{"", false},                    // empty never matches
		{"a3f19c2b77de4051", false},    // dashes are significant (re-typed verbatim)
	}
	for _, c := range cases {
		if got := confirmMatches(c.in, s); got != c.want {
			t.Errorf("confirmMatches(%q, %q) = %v, want %v", c.in, s, got, c.want)
		}
	}
	// An empty computed SAS must never match (defensive; should not happen).
	if confirmMatches("", "") {
		t.Error("confirmMatches(empty, empty) = true, want false")
	}
}

func TestIsAffirmative(t *testing.T) {
	yes := []string{"y", "Y", "yes", "YES", " yes \n", "Yes"}
	for _, s := range yes {
		if !isAffirmative(s) {
			t.Errorf("isAffirmative(%q) = false, want true", s)
		}
	}
	no := []string{"", "n", "no", "nope", "yeah", "ya", "1", "\n", "okay"}
	for _, s := range no {
		if isAffirmative(s) {
			t.Errorf("isAffirmative(%q) = true, want false", s)
		}
	}
}

func TestSASGateConfirm(t *testing.T) {
	const sas = "a3f1-9c2b-77de-4051"

	// --confirm-sas matching -> persist.
	if ok, _ := (sasGate{confirmSAS: sas}).confirm(sas, io.Discard); !ok {
		t.Error("matching --confirm-sas should permit persistence")
	}
	// --confirm-sas not matching -> refuse (even on a TTY: the flag is authoritative).
	if ok, reason := (sasGate{confirmSAS: "0000-0000-0000-0000", isTTY: true}).confirm(sas, io.Discard); ok {
		t.Error("mismatched --confirm-sas should refuse")
	} else if reason == "" {
		t.Error("refusal should carry a reason")
	}
	// --yes -> persist without comparison.
	if ok, _ := (sasGate{skip: true}).confirm(sas, io.Discard); !ok {
		t.Error("--yes should permit persistence")
	}
	// Interactive TTY, answer "y" -> persist.
	if ok, _ := (sasGate{isTTY: true, in: strings.NewReader("y\n")}).confirm(sas, io.Discard); !ok {
		t.Error("interactive 'y' should permit persistence")
	}
	// Interactive TTY, answer "n" -> refuse.
	if ok, _ := (sasGate{isTTY: true, in: strings.NewReader("n\n")}).confirm(sas, io.Discard); ok {
		t.Error("interactive 'n' should refuse")
	}
	// Interactive TTY, empty answer (just Enter) -> refuse (fail closed on default N).
	if ok, _ := (sasGate{isTTY: true, in: strings.NewReader("\n")}).confirm(sas, io.Discard); ok {
		t.Error("interactive empty answer should refuse")
	}
	// Non-interactive, no flag -> refuse (fail closed).
	if ok, reason := (sasGate{}).confirm(sas, io.Discard); ok {
		t.Error("non-interactive with no flag should refuse")
	} else if reason == "" {
		t.Error("refusal should carry a reason")
	}
}

func TestSASGatePromptsOnTTY(t *testing.T) {
	var out strings.Builder
	(sasGate{isTTY: true, in: strings.NewReader("y\n")}).confirm("a3f1-9c2b-77de-4051", &out)
	if !strings.Contains(out.String(), "Do the safety numbers match?") {
		t.Errorf("interactive confirm should print the prompt; got %q", out.String())
	}
}

func TestClassifyPair(t *testing.T) {
	if m, code, err := classifyPair(nil); err != nil || m != pairResponder || code != "" {
		t.Fatalf("no args = %v,%q,%v; want responder", m, code, err)
	}
	if m, code, err := classifyPair([]string{"ABC123"}); err != nil || m != pairInitiator || code != "ABC123" {
		t.Fatalf("one arg = %v,%q,%v; want initiator ABC123", m, code, err)
	}
	if _, _, err := classifyPair([]string{"a", "b"}); err == nil {
		t.Fatal("two args should error")
	}
}
