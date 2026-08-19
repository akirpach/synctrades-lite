package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/atotto/clipboard"

	"github.com/akirpach/synctrades-lite/internal/schwab"
)

// openBrowser launches the system browser at rawURL.
//
// On Windows it goes through FileProtocolHandler rather than `cmd /c start`,
// which mangles the & separators in a query string.
func openBrowser(rawURL string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start()
	case "darwin":
		return exec.Command("open", rawURL).Start()
	default:
		return exec.Command("xdg-open", rawURL).Start()
	}
}

type callbackResult struct {
	code string
	err  error
}

// waitForCallback returns the authorization code from whichever source
// produces a usable callback URL first: the clipboard, watched automatically,
// or a manual paste into the terminal.
//
// Clipboard access is not universal - headless sessions and some Linux setups
// have nothing to read from - so the manual path runs concurrently rather
// than only kicking in after a timeout. Either one winning is a normal
// outcome, not a fallback from failure.
func waitForCallback(stdin *bufio.Reader, state string) (string, error) {
	results := make(chan callbackResult, 2)
	stop := make(chan struct{})
	defer close(stop)

	if clipboard.Unsupported {
		fmt.Println("(clipboard auto-detect is unavailable in this environment; paste below)")
	} else {
		go func() {
			code, err := pollClipboard(state, stop)
			if err != nil {
				return // stopped because the other path already won
			}
			results <- callbackResult{code: code}
		}()
	}

	go func() {
		code, err := readCallbackFromStdin(stdin, state)
		results <- callbackResult{code: code, err: err}
	}()

	res := <-results
	return res.code, res.err
}

// pollClipboard watches the clipboard for a callback URL until stop closes.
func pollClipboard(state string, stop <-chan struct{}) (string, error) {
	var lastSeen string
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return "", errors.New("stopped")
		case <-ticker.C:
			text, err := clipboard.ReadAll()
			if err != nil || text == lastSeen {
				continue
			}
			lastSeen = text
			if code, perr := schwab.ParseCallbackURL(text, state); perr == nil {
				return code, nil
			}
		}
	}
}

// readCallbackFromStdin re-prompts on an unusable paste rather than ending
// the run over one stray click or truncated selection.
func readCallbackFromStdin(stdin *bufio.Reader, state string) (string, error) {
	const attempts = 5
	for attempt := 1; attempt <= attempts; attempt++ {
		line, readErr := stdin.ReadString('\n')

		if code, perr := schwab.ParseCallbackURL(line, state); perr == nil {
			return code, nil
		} else if line != "" {
			if attempt == attempts {
				return "", fmt.Errorf("gave up after %d attempts: %w", attempts, perr)
			}
			fmt.Printf("that paste is not usable: %v\n", perr)
			fmt.Printf("attempt %d of %d - paste the URL again:\n", attempt+1, attempts)
		}

		if readErr != nil {
			return "", fmt.Errorf("reading pasted URL: %w", readErr)
		}
	}
	return "", errors.New("gave up waiting for a usable URL")
}
