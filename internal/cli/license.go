package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/akirpach/synctrades-lite/internal/license"
	"github.com/akirpach/synctrades-lite/internal/store"
)

func newLicenseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "license",
		Short: "Manage your synctrades license",
	}
	cmd.AddCommand(newLicenseActivateCmd())
	return cmd
}

func newLicenseActivateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "activate <key>",
		Short: "Activate a license key from your purchase confirmation page",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLicenseActivate(args[0])
		},
	}
}

func runLicenseActivate(key string) error {
	claims, err := license.Verify(key)
	if err != nil {
		return fmt.Errorf("activating license: %w", err)
	}

	s, err := store.Default()
	if err != nil {
		return fmt.Errorf("opening the local credential store: %w", err)
	}
	if err := s.Update(func(c *store.Credentials) error {
		c.License.Token = key
		return nil
	}); err != nil {
		return fmt.Errorf("saving license to %s: %w", s.Path(), err)
	}

	fmt.Println("License activated.")
	if claims.Email != "" {
		fmt.Printf("Licensed to: %s\n", claims.Email)
	}
	return nil
}

// requireLicense confirms a valid license before letting a command do real
// work. There is no free tier: this is the enforcement point. Verification
// is a local signature check, so this never touches the network and there is
// no distinction between "invalid" and "couldn't check" to make.
func requireLicense(s *store.Store) error {
	creds, err := s.Load()
	if err != nil && !errors.Is(err, store.ErrNotConfigured) {
		return err
	}
	if !creds.License.HasToken() {
		return errors.New("no license activated; buy one and run `synctrades license activate <key>`")
	}
	if _, err := license.Verify(creds.License.Token); err != nil {
		return fmt.Errorf("%w; run `synctrades license activate <key>` with a current key", err)
	}
	return nil
}
