package client

import (
	"strings"
	"testing"
)

// The attach banner's shared half names the machine and says where Ctrl-C goes.
// It must NOT name a way out: each entry path has a different one (a bare
// `mir attach` closes the client; the overview takes Ctrl-O then d), and a
// banner that promises the wrong gesture is worse than one that promises none.
func TestAttachHint(t *testing.T) {
	got := AttachHint("box")
	for _, want := range []string{"attached to box", "Ctrl-C goes to the shell"} {
		if !strings.Contains(got, want) {
			t.Errorf("AttachHint = %q, want it to contain %q", got, want)
		}
	}
	for _, unwanted := range []string{"Ctrl-O", "detach", "comes back"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("AttachHint = %q — the shared half must leave %q to the caller", got, unwanted)
		}
	}
}
