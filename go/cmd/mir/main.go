// go/cmd/mir/main.go — the mir node. All logic lives in internal/cli so the
// deprecated mir-agent shim can share it verbatim.
package main

import (
	"os"

	"github.com/srcful/terminal-relay/go/internal/cli"
)

func main() { os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr)) }
