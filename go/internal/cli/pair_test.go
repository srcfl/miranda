package cli

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
	"github.com/srcful/terminal-relay/go/internal/pairing"
	"github.com/srcful/terminal-relay/go/internal/sas"
	"github.com/srcful/terminal-relay/go/internal/signal"
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

// TestPairRespondPrintsSASBeforeMsg3 drives shipped pairRespond over a real
// /pair bridge. The web initiator withholds msg3 until SAS confirm; the agent
// must print the safety number after msg2 so the QR client has something to
// compare. Finish is held until that print is observed.
func TestPairRespondPrintsSASBeforeMsg3(t *testing.T) {
	srv := httptest.NewServer(signal.New().Handler())
	defer srv.Close()

	dir := t.TempDir()
	out := &safeBuf{}
	a := &app{out: out, errOut: io.Discard}
	errCh := make(chan error, 1)
	go func() {
		errCh <- a.pairRespond(context.Background(), dir, "box", srv.URL, "http://127.0.0.1", sasGate{skip: true}, true)
	}()

	deadline := time.Now().Add(8 * time.Second)
	var code string
	for time.Now().Before(deadline) && code == "" {
		if err, ended := tryEnded(errCh); ended {
			t.Fatalf("pairRespond ended before a code: %v\n%s", err, out.String())
		}
		s := out.String()
		if i := strings.Index(s, "mir pair "); i >= 0 {
			fields := strings.Fields(s[i+len("mir pair "):])
			if len(fields) > 0 {
				code = fields[0]
			}
		}
		if code == "" {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if code == "" {
		t.Fatalf("no pairing code in output:\n%s", out.String())
	}

	signalURL, token, err := pairing.DecodeCode(code)
	if err != nil {
		t.Fatalf("DecodeCode(%q): %v", code, err)
	}
	signer, err := identity.DeriveSigner(bytes.Repeat([]byte{0x44}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	mc, closeConn, err := pairing.DialPair(ctx, signalURL, pairing.RoomID(token))
	if err != nil {
		t.Fatalf("initiator DialPair: %v", err)
	}
	defer closeConn()

	started, err := pairing.StartInitiator(ctx, mc, token, signer)
	if err != nil {
		t.Fatalf("StartInitiator: %v", err)
	}
	wantSAS := sas.FromBinding(started.Binding)
	if wantSAS == "" {
		t.Fatal("empty initiator SAS after msg2")
	}

	sawSAS := false
	for time.Now().Before(deadline) {
		s := out.String()
		if strings.Contains(s, "safety number:") {
			if !strings.Contains(s, wantSAS) {
				t.Fatalf("agent SAS missing %s in:\n%s", wantSAS, s)
			}
			sawSAS = true
			break
		}
		if err, ended := tryEnded(errCh); ended {
			t.Fatalf("pairRespond ended before SAS: %v\n%s", err, s)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !sawSAS {
		t.Fatalf("agent never printed SAS after msg2:\n%s", out.String())
	}
	if strings.Contains(out.String(), "✓ paired") {
		t.Fatal("agent pinned the owner before initiator sent msg3")
	}

	if err := started.Finish(func(*pairing.AgentInfo) (string, error) { return "opaque", nil }); err != nil {
		t.Fatalf("initiator Finish: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("pairRespond: %v\n%s", err, out.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("pairRespond hung after msg3:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "✓ paired") {
		t.Fatalf("expected owner pin after msg3:\n%s", out.String())
	}
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func tryEnded(ch <-chan error) (error, bool) {
	select {
	case err := <-ch:
		return err, true
	default:
		return nil, false
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
