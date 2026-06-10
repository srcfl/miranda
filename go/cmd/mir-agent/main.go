// go/cmd/mir-agent/main.go — DEPRECATED shim. mir-agent is now an alias for mir;
// it forwards to the shared internal/cli with a deprecation notice. Kept so
// existing installs / systemd units keep working through the deprecation window.
package main

import (
	"os"

	"github.com/srcful/terminal-relay/go/internal/cli"
)

func main() { os.Exit(cli.RunAgentCompat(os.Args[1:], os.Stdout, os.Stderr)) }
