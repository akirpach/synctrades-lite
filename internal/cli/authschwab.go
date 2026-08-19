package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/akirpach/synctrades-lite/internal/schwab"
	"github.com/akirpach/synctrades-lite/internal/store"
)

func newAuthSchwabCmd() *cobra.Command {
	var clientID, clientSecret, redirectURI string

	cmd := &cobra.Command{
		Use:   "schwab",
		Short: "Log in to your Schwab developer app and store the resulting tokens",
		Long: `Opens your browser to Schwab's login page. After you approve access, the
callback page will fail to load - that is expected, since nothing is
listening on the callback address. Copy that failed page's address bar;
synctrades picks it up automatically once it's on your clipboard, or you can
paste it in when prompted.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthSchwab(clientID, clientSecret, redirectURI)
		},
	}

	cmd.Flags().StringVar(&clientID, "client-id", "", "Schwab app key (from your Schwab developer app)")
	cmd.Flags().StringVar(&clientSecret, "client-secret", "", "Schwab app secret")
	cmd.Flags().StringVar(&redirectURI, "redirect-uri", "", "the callback URL registered on your Schwab app")

	return cmd
}

func runAuthSchwab(clientID, clientSecret, redirectURI string) error {
	s, err := store.Default()
	if err != nil {
		return fmt.Errorf("opening the local credential store: %w", err)
	}

	existing, err := s.Load()
	if err != nil && !errors.Is(err, store.ErrNotConfigured) {
		return err
	}

	cfg := schwab.Config{
		ClientID:     firstNonEmpty(clientID, existing.Schwab.ClientID),
		ClientSecret: firstNonEmpty(clientSecret, existing.Schwab.ClientSecret),
		RedirectURI:  firstNonEmpty(redirectURI, existing.Schwab.RedirectURI),
	}

	stdin := bufio.NewReader(os.Stdin)
	cfg, err = fillMissingCredentials(stdin, cfg)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	state, err := schwab.GenerateState()
	if err != nil {
		return err
	}
	authorizeURL := cfg.AuthorizeURL(state)

	fmt.Println("Opening your browser to log in to Schwab...")
	if err := openBrowser(authorizeURL); err != nil {
		fmt.Printf("(could not open a browser automatically: %v)\n", err)
	}
	fmt.Println("If nothing opens, visit this URL yourself:")
	fmt.Println("  " + authorizeURL)
	fmt.Println()
	fmt.Println("Log in and approve access. The callback page will fail to load - that's expected.")
	fmt.Println("Copy that failed page's full address bar. synctrades will pick it up")
	fmt.Println("automatically once it's on your clipboard, or paste it below and press Enter:")
	fmt.Println()

	code, err := waitForCallback(stdin, state)
	if err != nil {
		return fmt.Errorf("getting the authorization code: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tok, err := schwab.NewTokenClient(cfg).Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("exchanging the authorization code: %w", err)
	}

	if err := s.Update(func(c *store.Credentials) error {
		c.Schwab.ClientID = cfg.ClientID
		c.Schwab.ClientSecret = cfg.ClientSecret
		c.Schwab.RedirectURI = cfg.RedirectURI
		c.Schwab.SetToken(tok)
		return nil
	}); err != nil {
		return fmt.Errorf("saving credentials to %s: %w", s.Path(), err)
	}

	fmt.Println()
	fmt.Printf("Signed in. Tokens saved to %s.\n", s.Path())
	fmt.Printf("Access token expires %s.\n", tok.ExpiresAt.Local().Format(time.RFC3339))
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func fillMissingCredentials(stdin *bufio.Reader, cfg schwab.Config) (schwab.Config, error) {
	if cfg.ClientID == "" {
		v, err := prompt(stdin, "Schwab App Key: ")
		if err != nil {
			return cfg, err
		}
		cfg.ClientID = v
	}
	if cfg.ClientSecret == "" {
		v, err := promptSecret(stdin, "Schwab App Secret: ")
		if err != nil {
			return cfg, err
		}
		cfg.ClientSecret = v
	}
	if cfg.RedirectURI == "" {
		v, err := prompt(stdin, "Callback URL registered on your Schwab app: ")
		if err != nil {
			return cfg, err
		}
		cfg.RedirectURI = v
	}
	return cfg, nil
}

func prompt(stdin *bufio.Reader, label string) (string, error) {
	fmt.Print(label)
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// promptSecret reads without echoing back to the terminal, where possible.
func promptSecret(stdin *bufio.Reader, label string) (string, error) {
	fmt.Print(label)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", fmt.Errorf("reading input: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	// Not a real terminal (piped input): nothing to mask, fall back to the
	// shared reader so this still works under a script or a test harness.
	return prompt(stdin, "")
}
