package cli

import (
	"fmt"
	"strings"

	"github.com/srcful/terminal-relay/go/internal/agent"
)

const defaultTmuxShell = "tmux:new:-A:-s:main"

// resolveLaunch picks the PTY argv and persisted engine from --engine / --shell
// and the last successful `mir up`. --engine and --shell together is an error:
// one is the product mode, the other is a raw argv override.
func resolveLaunch(engineFlag, shell string, engineSet, shellSet bool, persisted string) (launch []string, persistEngine string, notice string, err error) {
	if engineSet && shellSet {
		return nil, "", "", fmt.Errorf("--engine and --shell cannot be used together")
	}
	if engineSet {
		switch strings.TrimSpace(engineFlag) {
		case "herdr":
			return []string{"herdr"}, "herdr", herdrPhoneNotice(), nil
		case "tmux", "":
			return strings.Split(defaultTmuxShell, ":"), "tmux", "", nil
		default:
			return nil, "", "", fmt.Errorf("unknown engine %q (known: tmux, herdr)", engineFlag)
		}
	}
	if shellSet {
		return strings.Split(shell, ":"), "", "", nil
	}
	if persisted == "herdr" {
		return []string{"herdr"}, "herdr", herdrPhoneNotice(), nil
	}
	if shell == "" {
		shell = defaultTmuxShell
	}
	return strings.Split(shell, ":"), "", "", nil
}

func persistEngineAfterUp(dir, engine string) error {
	if engine == "" {
		return nil
	}
	return agent.SetEngine(dir, engine)
}

func herdrPhoneNotice() string {
	return strings.TrimSpace(`
herdr is the PTY on this machine. On the phone, open Miranda, tap this
machine, and you land in this herdr — same session as the desktop TUI.

Miranda's window strip and mir share stay tmux-only. Switch machines
with the ⇄ list; switch herdr tabs with herdr's own prefix (Ctrl-B).
`) + "\n"
}
