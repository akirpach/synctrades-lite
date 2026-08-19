// Command synctrades syncs a user's Schwab trade history into their own
// Google Sheet. See the repo's CLAUDE.md for the product and architecture
// decisions behind it.
package main

import "github.com/akirpach/synctrades-lite/internal/cli"

func main() {
	cli.Execute()
}
