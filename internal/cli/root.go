// Package cli wires synctrades-lite's cobra commands: auth, sync, and status.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "synctrades",
		Short: "Sync your Schwab trade history into your own Google Sheet",
		Long: `synctrades runs entirely on your machine: it authorizes against your own
Schwab developer app and your own Google service account, then appends new
trades to a sheet you control. It never touches anyone else's credentials or
data.`,
		SilenceUsage: true,
	}
	cmd.AddCommand(newAuthCmd())
	cmd.AddCommand(newSyncCmd())
	cmd.AddCommand(newStatusCmd())
	return cmd
}

// Execute runs the root command and exits non-zero on failure.
func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
