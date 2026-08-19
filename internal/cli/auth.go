package cli

import "github.com/spf13/cobra"

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authorize against Schwab or Google Sheets",
	}
	cmd.AddCommand(newAuthSchwabCmd())
	return cmd
}
