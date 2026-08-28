// Package store persists the user's Schwab tokens and Sheets configuration to
// an encrypted file on their own machine. There is no server and no database:
// one local user, one local file.
package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/akirpach/synctrades-lite/internal/schwab"
)

// formatVersion prefixes every encrypted file so a future change to the
// envelope can be detected rather than surfacing as a decryption failure.
const formatVersion byte = 1

// keyLen is 32 bytes, selecting AES-256.
const keyLen = 32

// ErrNotConfigured means no credential file exists yet, so the user has not run
// the auth commands. Callers should say what to run, not report a file error.
var ErrNotConfigured = errors.New("no saved credentials")

// ErrWrongKey means the file exists but will not decrypt with the key we have.
// Usually the OS keyring entry was deleted or the file was copied from another
// machine.
var ErrWrongKey = errors.New("saved credentials could not be decrypted with the key on this machine")

// Schwab is the user's Schwab app registration plus their current tokens.
//
// The three app fields are configuration the user supplies once from their own
// Schwab developer app. The token fields are session state and are cleared,
// without touching the app fields, when a refresh token dies.
type Schwab struct {
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret"`
	RedirectURI  string    `json:"redirect_uri"`
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

// Config returns the app registration for building authorize and token requests.
func (s Schwab) Config() schwab.Config {
	return schwab.Config{
		ClientID:     s.ClientID,
		ClientSecret: s.ClientSecret,
		RedirectURI:  s.RedirectURI,
	}
}

// Token returns the stored token pair.
func (s Schwab) Token() schwab.Token {
	return schwab.Token{
		AccessToken:  s.AccessToken,
		RefreshToken: s.RefreshToken,
		ExpiresAt:    s.ExpiresAt,
	}
}

// SetToken records a freshly issued or refreshed token pair.
func (s *Schwab) SetToken(t schwab.Token) {
	s.AccessToken = t.AccessToken
	s.RefreshToken = t.RefreshToken
	s.ExpiresAt = t.ExpiresAt
}

// ClearToken discards session state while keeping the app registration, so a
// dead refresh token costs the user a browser login and not a reconfiguration.
func (s *Schwab) ClearToken() {
	s.AccessToken = ""
	s.RefreshToken = ""
	s.ExpiresAt = time.Time{}
}

// HasToken reports whether there is a refresh token to work from.
func (s Schwab) HasToken() bool { return s.RefreshToken != "" }

// Sheets is where synced rows go.
type Sheets struct {
	ServiceAccountKeyPath string `json:"service_account_key_path"`
	SpreadsheetID         string `json:"spreadsheet_id"`
	SheetName             string `json:"sheet_name"`
}

// License is the user's purchased synctrades license token. There is no free
// tier: sync refuses to run without one. Verification is a local signature
// check (see internal/license), so there is nothing else to cache here.
type License struct {
	Token string `json:"token"`
}

// HasToken reports whether a license token has ever been activated.
func (l License) HasToken() bool { return l.Token != "" }

// Credentials is the whole persisted state.
type Credentials struct {
	Schwab  Schwab  `json:"schwab"`
	Sheets  Sheets  `json:"sheets"`
	License License `json:"license"`
}

// Store reads and writes one encrypted credential file.
type Store struct {
	path string
	key  []byte
}

// New returns a Store over an explicit path and key. Tests use this; callers
// normally want Default.
func New(path string, key []byte) (*Store, error) {
	if len(key) != keyLen {
		return nil, fmt.Errorf("encryption key is %d bytes, need %d", len(key), keyLen)
	}
	if path == "" {
		return nil, errors.New("credential path is empty")
	}
	return &Store{path: path, key: key}, nil
}

// Default returns a Store at the per-user config location, with its key from the
// OS keyring, creating the key on first use.
func Default() (*Store, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	key, _, err := EncryptionKey()
	if err != nil {
		return nil, err
	}
	return New(path, key)
}

// ProjectDirName is the directory that, when present in the working directory,
// makes the credential store project-local.
const ProjectDirName = ".synctrades"

// CredentialFileName is the encrypted store's filename in whichever directory
// is chosen.
const CredentialFileName = "credentials.enc"

// Origin describes which rule chose the credential location, so `status` can
// tell the user where their tokens actually live.
type Origin string

const (
	// OriginEnv means SYNCTRADES_CONFIG_DIR was set.
	OriginEnv Origin = "SYNCTRADES_CONFIG_DIR"
	// OriginProject means a .synctrades directory exists in the working directory.
	OriginProject Origin = "project directory"
	// OriginUserConfig means the per-user OS config directory was used.
	OriginUserConfig Origin = "user config directory"
)

// DefaultPath is the credential file location for this user.
func DefaultPath() (string, error) {
	path, _, err := ResolvePath()
	return path, err
}

// ResolvePath decides where credentials live and reports why, in this order:
//
//  1. SYNCTRADES_CONFIG_DIR, if set.
//  2. ./.synctrades/ in the working directory, if that directory already exists.
//  3. The OS per-user config directory.
//
// Rule 2 makes a project-local store opt-in rather than automatic. Nothing is
// created in the working directory unless the user made that directory
// deliberately, because a tool whose configuration silently depends on the
// current directory surprises anyone who runs it from somewhere else.
//
// Whichever rule wins, keep the chosen directory out of a cloud-synced tree.
// A store inside OneDrive, Dropbox or iCloud is uploaded automatically, which
// defeats the point of encrypting it locally: the key may sit beside it when no
// OS keyring is available.
func ResolvePath() (string, Origin, error) {
	if override := os.Getenv("SYNCTRADES_CONFIG_DIR"); override != "" {
		return filepath.Join(override, CredentialFileName), OriginEnv, nil
	}

	if info, err := os.Stat(ProjectDirName); err == nil && info.IsDir() {
		abs, err := filepath.Abs(filepath.Join(ProjectDirName, CredentialFileName))
		if err != nil {
			return "", "", fmt.Errorf("resolving %s: %w", ProjectDirName, err)
		}
		return abs, OriginProject, nil
	}

	dir, err := os.UserConfigDir()
	if err != nil {
		return "", "", fmt.Errorf("locating the user config directory: %w", err)
	}
	return filepath.Join(dir, "synctrades", CredentialFileName), OriginUserConfig, nil
}

// Path reports where this Store reads and writes.
func (s *Store) Path() string { return s.path }

// Exists reports whether a credential file is present.
func (s *Store) Exists() bool {
	_, err := os.Stat(s.path)
	return err == nil
}

// Load decrypts the credential file. It returns ErrNotConfigured when no file
// exists, which is an expected first-run state rather than a failure.
func (s *Store) Load() (Credentials, error) {
	blob, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{}, ErrNotConfigured
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("reading %s: %w", s.path, err)
	}

	plaintext, err := decrypt(s.key, blob)
	if err != nil {
		return Credentials{}, err
	}

	var creds Credentials
	if err := json.Unmarshal(plaintext, &creds); err != nil {
		return Credentials{}, fmt.Errorf("decoding credentials: %w", err)
	}
	return creds, nil
}

// Save encrypts and writes the credentials, replacing any existing file.
//
// The write goes to a temporary file in the same directory and is then renamed,
// so an interrupted write cannot leave a half-written file that fails to
// decrypt and locks the user out of their own tokens.
func (s *Store) Save(creds Credentials) error {
	plaintext, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("encoding credentials: %w", err)
	}

	blob, err := encrypt(s.key, plaintext)
	if err != nil {
		return err
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("creating a temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	// Restrict before writing, so the plaintext-adjacent ciphertext is never
	// briefly world-readable. On Windows this only clears the read-only bit;
	// there the per-user config directory's ACL is what limits access.
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, os.ErrInvalid) {
		tmp.Close()
		return fmt.Errorf("restricting permissions on %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("flushing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}

	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replacing %s: %w", s.path, err)
	}
	return nil
}

// Update loads, applies mutate, and saves. A first run starts from empty
// credentials rather than failing, so the auth commands can use this directly.
func (s *Store) Update(mutate func(*Credentials) error) error {
	creds, err := s.Load()
	if err != nil && !errors.Is(err, ErrNotConfigured) {
		return err
	}
	if err := mutate(&creds); err != nil {
		return err
	}
	return s.Save(creds)
}

// Delete removes the credential file. A missing file is not an error.
func (s *Store) Delete() error {
	err := os.Remove(s.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", s.path, err)
	}
	return nil
}

func encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initializing cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initializing GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	// version || nonce || ciphertext+tag
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, formatVersion)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, nil), nil
}

func decrypt(key, blob []byte) ([]byte, error) {
	if len(blob) == 0 {
		return nil, fmt.Errorf("%w: the file is empty", ErrWrongKey)
	}
	if blob[0] != formatVersion {
		return nil, fmt.Errorf("credential file format version %d is not supported by this build", blob[0])
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initializing cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initializing GCM: %w", err)
	}

	body := blob[1:]
	if len(body) < gcm.NonceSize() {
		return nil, fmt.Errorf("%w: the file is truncated", ErrWrongKey)
	}

	nonce, ciphertext := body[:gcm.NonceSize()], body[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// GCM authenticates, so this is either the wrong key or a tampered
		// file. Neither is distinguishable, and both need the same remedy.
		return nil, ErrWrongKey
	}
	return plaintext, nil
}

// newKey returns a fresh random encryption key.
func newKey() ([]byte, error) {
	key := make([]byte, keyLen)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generating an encryption key: %w", err)
	}
	return key, nil
}
