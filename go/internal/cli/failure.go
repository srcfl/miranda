// go/internal/cli/failure.go — the error taxonomy (N3). One place turns the
// high-traffic failure causes into house copy: a plain sentence stating the
// fact, one next step, and the underlying cause in parentheses at the end —
// never a bare wrapped chain as the whole message. Wording only: every
// fail-closed path stays fail-closed.
package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/srcful/terminal-relay/go/internal/client"
)

// discoveryPausedNote is printed (stderr) when the encrypted registry could not
// be fetched but locally saved machines still let the command proceed.
const discoveryPausedNote = "note: the relay is unreachable — showing saved machines only; discovery resumes when you are back online"

// humanAttachErr rewrites a failed attach/run. The likely causes, in order: the
// machine is off or offline (agent unavailable / unreachable), or the relay
// itself cannot be reached.
func humanAttachErr(binary, name string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "dial signaling") {
		return humanRelayErr(binary, err)
	}
	if errors.Is(err, client.ErrReconnectGaveUp) || errors.Is(err, client.ErrUnreachable) ||
		strings.Contains(msg, "unreachable") || strings.Contains(msg, "agent unavailable") {
		return fmt.Errorf("machine %q is unreachable — start it with `%s up` on that machine, or see what is online with `%s list` (cause: %v)",
			name, binary, binary, err)
	}
	return err
}

// humanRelayErr rewrites a failure to reach the relay itself.
func humanRelayErr(binary string, err error) error {
	return fmt.Errorf("the relay is unreachable — check your connection, then run `%s doctor` (cause: %v)", binary, err)
}

// humanPairHandshakeErr rewrites a pairing handshake failure: past the relay
// dial, the overwhelmingly likely cause is a wrong or expired code.
func humanPairHandshakeErr(err error) error {
	return fmt.Errorf("pairing failed — the code is wrong or expired (codes last 5 minutes); get a fresh one from the machine (cause: %v)", err)
}

// humanUpdateErr rewrites a self-update failure.
func humanUpdateErr(err error) error {
	return fmt.Errorf("could not update — try again later, or download the release from https://github.com/srcfl/miranda/releases (cause: %v)", err)
}
