// Package cli wires synctrades-lite's cobra commands: auth, sync, and status.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newRootCmd(version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "synctrades",
		Short: "Sync your Schwab trade history into your own Google Sheet",
		Long: `synctrades runs entirely on your machine: it authorizes against your own
Schwab developer app and your own Google service account, then appends new
trades to a sheet you control. It never touches anyone else's credentials or
data.`,
		Version:      version,
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			printBanner()
			_ = cmd.Help()
		},
	}
	cmd.AddCommand(newAuthCmd())
	cmd.AddCommand(newSyncCmd())
	cmd.AddCommand(newStatusCmd())
	return cmd
}

// Execute runs the root command and exits non-zero on failure. version is
// injected by goreleaser at build time; a plain `go build`/`go run` reports
// "dev".
func Execute(version string) {
	if err := newRootCmd(version).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
