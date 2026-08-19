// Command synctrades syncs a user's Schwab trade history into their own
// Google Sheet. See the repo's CLAUDE.md for the product and architecture
// decisions behind it.
package main

import "github.com/akirpach/synctrades-lite/internal/cli"

// version is set at build time via -ldflags "-X main.version=...", which
// goreleaser does for every tagged release. A plain `go build` leaves it at
// "dev".
var version = "dev"

func main() {
	cli.Execute(version)
}
