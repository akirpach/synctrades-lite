package store

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

// Keyring identifiers. Changing either orphans every existing user's key and
// makes their saved credentials undecryptable, so treat them as permanent.
const (
	keyringService = "synctrades-lite"
	keyringUser    = "credential-encryption-key"
)

// KeySource records where the encryption key came from, so the CLI can warn
// when it is not in the OS keyring.
type KeySource string

const (
	// KeyFromKeyring means the OS credential store holds the key: Windows
	// Credential Manager, macOS Keychain, or Secret Service on Linux.
	KeyFromKeyring KeySource = "os-keyring"

	// KeyFromFile means no keyring was available and the key sits beside the
	// credential file. This still prevents plaintext tokens on disk and
	// protects against backups, cloud sync and log scraping picking them up,
	// but it does not defend against anyone who can read both files.
	KeyFromFile KeySource = "local-file"
)

// keyring indirection, so tests never touch the real OS credential store.
var (
	keyringGet = keyring.Get
	keyringSet = keyring.Set
	keyringDel = keyring.Delete
)

// EncryptionKey returns this machine's credential encryption key, creating and
// storing one on first use. It reports where the key came from.
//
// The OS keyring is tried first. A headless Linux box with no Secret Service
// has no keyring at all, and failing there would make the tool unusable in
// exactly the environment a CLI is most likely to run in, so a restricted local
// key file is the documented fallback.
func EncryptionKey() ([]byte, KeySource, error) {
	if encoded, err := keyringGet(keyringService, keyringUser); err == nil {
		key, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if decodeErr != nil {
			return nil, "", fmt.Errorf("the keyring entry for %s is corrupt: %w", keyringService, decodeErr)
		}
		if len(key) != keyLen {
			return nil, "", fmt.Errorf("the keyring entry for %s is %d bytes, expected %d",
				keyringService, len(key), keyLen)
		}
		return key, KeyFromKeyring, nil
	} else if !errors.Is(err, keyring.ErrNotFound) {
		// The keyring exists but is unusable, or there is no keyring at all.
		// Either way, fall back rather than refusing to run.
		return fileKey()
	}

	// No entry yet: mint one and try to store it in the keyring.
	key, err := newKey()
	if err != nil {
		return nil, "", err
	}
	if err := keyringSet(keyringService, keyringUser, base64.StdEncoding.EncodeToString(key)); err != nil {
		return fileKey()
	}
	return key, KeyFromKeyring, nil
}

// DeleteEncryptionKey removes the key from wherever it is stored. Saved
// credentials become permanently undecryptable, so this is for a full reset.
func DeleteEncryptionKey() error {
	var errs []error

	if err := keyringDel(keyringService, keyringUser); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		errs = append(errs, fmt.Errorf("removing the keyring entry: %w", err))
	}

	path, err := keyFilePath()
	if err != nil {
		errs = append(errs, err)
	} else if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("removing %s: %w", path, err))
	}

	return errors.Join(errs...)
}

// keyFilePath is the fallback key location, beside the credential file.
func keyFilePath() (string, error) {
	credPath, err := DefaultPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(credPath), "key"), nil
}

// fileKey reads the fallback key file, creating it if absent.
func fileKey() ([]byte, KeySource, error) {
	path, err := keyFilePath()
	if err != nil {
		return nil, "", err
	}

	encoded, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, decodeErr := base64.StdEncoding.DecodeString(string(encoded))
		if decodeErr != nil {
			return nil, "", fmt.Errorf("the key file %s is corrupt: %w", path, decodeErr)
		}
		if len(key) != keyLen {
			return nil, "", fmt.Errorf("the key file %s holds %d bytes, expected %d", path, len(key), keyLen)
		}
		return key, KeyFromFile, nil

	case errors.Is(err, os.ErrNotExist):
		key, genErr := newKey()
		if genErr != nil {
			return nil, "", genErr
		}
		if writeErr := writeKeyFile(path, key); writeErr != nil {
			return nil, "", writeErr
		}
		return key, KeyFromFile, nil

	default:
		return nil, "", fmt.Errorf("reading %s: %w", path, err)
	}
}

func writeKeyFile(path string, key []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
