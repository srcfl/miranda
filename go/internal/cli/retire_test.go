package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/client"
)

// withRevokeTTY swaps the retirement TTY seam for one test.
func withRevokeTTY(t *testing.T, isTTY bool) {
	t.Helper()
	prev := machineRevokeIsTTY
	machineRevokeIsTTY = func() bool { return isTTY }
	t.Cleanup(func() { machineRevokeIsTTY = prev })
}

func TestConfirmRetireAnswers(t *testing.T) {
	cases := []struct {
		answer string
		want   bool
	}{
		{"y\n", true}, {"Y\n", true}, {"yes\n", true}, {"YES\n", true},
		{"\n", false}, {"n\n", false}, {"no\n", false}, {"whatever\n", false},
	}
	for _, c := range cases {
		var out bytes.Buffer
		a := &app{in: strings.NewReader(c.answer), out: &out, errOut: io.Discard, binary: "mir"}
		if got := a.confirmRetire("box"); got != c.want {
			t.Fatalf("confirmRetire(%q) = %v, want %v", c.answer, got, c.want)
		}
		// The prompt must carry the four plain facts and default to No.
		for _, want := range []string{"every device", "no longer reach", "keeps running", "pair fresh", "[y/N]"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("prompt missing %q:\n%s", want, out.String())
			}
		}
	}
	// No reader at all can never consent.
	a := &app{out: io.Discard, errOut: io.Discard, binary: "mir"}
	if a.confirmRetire("box") {
		t.Fatal("confirmRetire with nil input must refuse")
	}
}

// TestMachineRevokeInteractiveDecline: on a TTY without --yes the command asks
// first; answering no changes nothing — and consent runs before any identity
// or network work, so no relay is needed to say no.
func TestMachineRevokeInteractiveDecline(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	withRevokeTTY(t, true)
	dir := t.TempDir()
	var out, errb bytes.Buffer
	a := &app{in: strings.NewReader("n\n"), out: &out, errOut: &errb, binary: "mir"}
	if code := a.run([]string{"machine", "revoke", "box", "--dir", dir}); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "nothing changed") {
		t.Fatalf("decline must say nothing changed:\n%s", out.String())
	}
	if records, err := client.ListRevocations(dir); err != nil || len(records) != 0 {
		t.Fatalf("decline must record nothing, got %v (err %v)", records, err)
	}
}

// TestMachineRevokeInteractiveAccept: answering yes retires for real — signed
// record persisted locally, published to the relay, and the way back printed.
func TestMachineRevokeInteractiveAccept(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	withRevokeTTY(t, true)
	posts := 0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/registry":
			_, _ = io.WriteString(w, "[]")
		case r.URL.Path == "/revocations" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, "[]")
		case r.URL.Path == "/revocations" && r.Method == http.MethodPost:
			posts++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer relay.Close()
	t.Setenv("MIR_SIGNAL", relay.URL)
	dir := t.TempDir()
	if _, err := client.LoadOrCreateIdentity(dir); err != nil {
		t.Fatal(err)
	}
	if err := client.AddMachine(dir, client.Machine{Name: "box", MachineID: "machine-1", HostPubHex: "aabb", SignalURL: relay.URL}); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	a := &app{in: strings.NewReader("y\n"), out: &out, errOut: &errb, binary: "mir"}
	if code := a.run([]string{"machine", "revoke", "box", "--dir", dir}); code != 0 {
		t.Fatalf("exit = %d, stdout=%q stderr=%q", code, out.String(), errb.String())
	}
	if posts != 1 {
		t.Fatalf("POST count = %d, want 1", posts)
	}
	if records, err := client.ListRevocations(dir); err != nil || len(records) != 1 || records[0].MachineID != "machine-1" {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	for _, want := range []string{"retired", "keeps running", "pair fresh"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("success copy missing %q:\n%s", want, out.String())
		}
	}
}

// TestMachineRevokeNonTTYFailsClosed: a scripted run without --yes cannot
// answer a prompt, so it must refuse — pointing at --yes — and touch nothing.
func TestMachineRevokeNonTTYFailsClosed(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	withRevokeTTY(t, false)
	dir := t.TempDir()
	var out, errb bytes.Buffer
	a := &app{in: strings.NewReader(""), out: &out, errOut: &errb, binary: "mir"}
	if code := a.run([]string{"machine", "revoke", "box", "--dir", dir}); code == 0 {
		t.Fatalf("non-TTY without --yes must fail, stdout=%q", out.String())
	}
	if !strings.Contains(errb.String(), "--yes") {
		t.Fatalf("refusal must point at --yes:\n%s", errb.String())
	}
	if strings.Contains(out.String(), "[y/N]") {
		t.Fatalf("non-TTY run must not prompt:\n%s", out.String())
	}
	if records, err := client.ListRevocations(dir); err != nil || len(records) != 0 {
		t.Fatalf("refusal must record nothing, got %v (err %v)", records, err)
	}
}
