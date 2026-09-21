// Command pulse reads Pulse Analytics data from the terminal.
package main

import (
	"os"

	"github.com/ciphera-net/pulse-cli/internal/cli"
)

// version is stamped by the release build:
//
//	-ldflags "-X main.version=v1.2.3"
var version = "dev"

func main() {
	cli.Version = version
	os.Exit(cli.Execute())
}
