package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akirpach/synctrades-lite/internal/schwab"
)

// testKey is a fixed key so tests never touch the OS keyring.
func testKey() []byte {
	key := make([]byte, keyLen)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "credentials.enc"), testKey())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func sampleCredentials() Credentials {
	return Credentials{
		Schwab: Schwab{
			ClientID:     "2gNbUjgfGCqngi3ZIW2QNZIJKorDAwoy",
			ClientSecret: "sixteencharsecrt",
			RedirectURI:  "https://127.0.0.1:5001/api/schwab/callback",
			AccessToken:  "access-token-value",
			RefreshToken: "refresh-token-value",
			ExpiresAt:    time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
		},
		Sheets: Sheets{
			ServiceAccountKeyPath: `C:\keys\service-account.json`,
			SpreadsheetID:         "1AbCdEfGhIjKlMnOpQrStUvWxYz",
			SheetName:             "Trades",
		},
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	s := testStore(t)
	want := sampleCredentials()

	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Schwab != want.Schwab {
		t.Errorf("schwab = %+v, want %+v", got.Schwab, want.Schwab)
	}
	if got.Sheets != want.Sheets {
		t.Errorf("sheets = %+v, want %+v", got.Sheets, want.Sheets)
	}
	if !got.Schwab.ExpiresAt.Equal(want.Schwab.ExpiresAt) {
		t.Errorf("expiresAt = %v, want %v", got.Schwab.ExpiresAt, want.Schwab.ExpiresAt)
	}
}

func TestFileOnDiskIsNotPlaintext(t *testing.T) {
	// The whole point of the store. If a token appears verbatim in the file,
	// encryption is not happening.
	s := testStore(t)
	creds := sampleCredentials()
	if err := s.Save(creds); err != nil {
		t.Fatalf("Save: %v", err)
	}

	blob, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("reading the file: %v", err)
	}

	for _, secret := range []string{
		creds.Schwab.AccessToken,
		creds.Schwab.RefreshToken,
		creds.Schwab.ClientSecret,
		creds.Sheets.SpreadsheetID,
	} {
		if secret == "" {
			continue
		}
		if bytes.Contains(blob, []byte(secret)) {
			t.Errorf("the file contains %q in the clear", secret)
		}
	}

	if blob[0] != formatVersion {
		t.Errorf("first byte = %d, want the format version %d", blob[0], formatVersion)
	}
}

func TestLoadWithoutAFileIsNotConfigured(t *testing.T) {
	s := testStore(t)
	if _, err := s.Load(); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("error = %v, want ErrNotConfigured", err)
	}
	if s.Exists() {
		t.Error("Exists reported true with no file")
	}
}

func TestLoadWithTheWrongKeyFails(t *testing.T) {
	s := testStore(t)
	if err := s.Save(sampleCredentials()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	otherKey := testKey()
	otherKey[0] ^= 0xff
	other, err := New(s.Path(), otherKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := other.Load(); !errors.Is(err, ErrWrongKey) {
		t.Errorf("error = %v, want ErrWrongKey", err)
	}
}

func TestTamperedFileIsRejected(t *testing.T) {
	// GCM authenticates, so a flipped bit must fail rather than decode to
	// garbage that then parses as JSON.
	s := testStore(t)
	if err := s.Save(sampleCredentials()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	blob, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	blob[len(blob)-1] ^= 0x01
	if err := os.WriteFile(s.Path(), blob, 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	if _, err := s.Load(); !errors.Is(err, ErrWrongKey) {
		t.Errorf("error = %v, want ErrWrongKey", err)
	}
}

func TestUnknownFormatVersionIsReported(t *testing.T) {
	s := testStore(t)
	if err := s.Save(sampleCredentials()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	blob, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	blob[0] = 99
	if err := os.WriteFile(s.Path(), blob, 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	_, err = s.Load()
	if err == nil {
		t.Fatal("an unknown format version was accepted")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("error does not mention the version: %v", err)
	}
}

func TestEachSaveUsesAFreshNonce(t *testing.T) {
	// Reusing a nonce with the same key breaks GCM's guarantees outright.
	s := testStore(t)
	creds := sampleCredentials()

	if err := s.Save(creds); err != nil {
		t.Fatalf("Save: %v", err)
	}
	first, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("reading: %v", err)
	}

	if err := s.Save(creds); err != nil {
		t.Fatalf("Save: %v", err)
	}
	second, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("reading: %v", err)
	}

	if bytes.Equal(first, second) {
		t.Error("identical credentials produced an identical file, so the nonce is being reused")
	}
}

func TestSaveLeavesNoTemporaryFiles(t *testing.T) {
	s := testStore(t)
	if err := s.Save(sampleCredentials()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatalf("reading the directory: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a temporary file survived: %s", e.Name())
		}
	}
}

func TestUpdateStartsFromEmptyOnFirstRun(t *testing.T) {
	s := testStore(t)

	err := s.Update(func(c *Credentials) error {
		c.Schwab.ClientID = "first-run-client-id"
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Schwab.ClientID != "first-run-client-id" {
		t.Errorf("clientID = %q", got.Schwab.ClientID)
	}
}

func TestUpdateDoesNotSaveWhenMutateFails(t *testing.T) {
	s := testStore(t)
	if err := s.Save(sampleCredentials()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	sentinel := errors.New("nope")
	err := s.Update(func(c *Credentials) error {
		c.Schwab.ClientID = "should-not-persist"
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the sentinel", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Schwab.ClientID == "should-not-persist" {
		t.Error("a failed mutation was persisted anyway")
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	s := testStore(t)
	if err := s.Save(sampleCredentials()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if s.Exists() {
		t.Error("the file survived Delete")
	}
	if err := s.Delete(); err != nil {
		t.Errorf("deleting a missing file failed: %v", err)
	}
}

func TestNewRejectsBadArguments(t *testing.T) {
	if _, err := New("path", []byte("too short")); err == nil {
		t.Error("a short key was accepted")
	}
	if _, err := New("", testKey()); err == nil {
		t.Error("an empty path was accepted")
	}
}

func TestClearTokenKeepsTheAppRegistration(t *testing.T) {
	// A dead refresh token should cost a browser login, not a reconfiguration.
	creds := sampleCredentials()
	creds.Schwab.ClearToken()

	if creds.Schwab.HasToken() {
		t.Error("HasToken still true after ClearToken")
	}
	if creds.Schwab.AccessToken != "" || creds.Schwab.RefreshToken != "" {
		t.Error("tokens survived ClearToken")
	}
	if !creds.Schwab.ExpiresAt.IsZero() {
		t.Error("expiry survived ClearToken")
	}
	if creds.Schwab.ClientID == "" || creds.Schwab.ClientSecret == "" || creds.Schwab.RedirectURI == "" {
		t.Error("ClearToken discarded the app registration")
	}
}

func TestSchwabConfigAndTokenConversion(t *testing.T) {
	creds := sampleCredentials()

	cfg := creds.Schwab.Config()
	if err := cfg.Validate(); err != nil {
		t.Errorf("the sample config does not validate: %v", err)
	}
	if cfg.ClientID != creds.Schwab.ClientID || cfg.RedirectURI != creds.Schwab.RedirectURI {
		t.Errorf("config = %+v", cfg)
	}

	tok := creds.Schwab.Token()
	if tok.AccessToken != creds.Schwab.AccessToken || tok.RefreshToken != creds.Schwab.RefreshToken {
		t.Errorf("token = %+v", tok)
	}

	var s Schwab
	s.SetToken(schwab.Token{AccessToken: "a", RefreshToken: "r", ExpiresAt: tok.ExpiresAt})
	if s.AccessToken != "a" || s.RefreshToken != "r" || !s.ExpiresAt.Equal(tok.ExpiresAt) {
		t.Errorf("SetToken produced %+v", s)
	}
}

func TestResolvePathPrefersTheEnvironmentOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SYNCTRADES_CONFIG_DIR", dir)

	path, origin, err := ResolvePath()
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if want := filepath.Join(dir, CredentialFileName); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if origin != OriginEnv {
		t.Errorf("origin = %q, want %q", origin, OriginEnv)
	}
}

func TestResolvePathUsesAProjectDirectoryWhenItExists(t *testing.T) {
	t.Setenv("SYNCTRADES_CONFIG_DIR", "")

	// Run from a scratch directory containing .synctrades.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ProjectDirName), 0o700); err != nil {
		t.Fatalf("creating %s: %v", ProjectDirName, err)
	}
	t.Chdir(dir)

	path, origin, err := ResolvePath()
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if origin != OriginProject {
		t.Errorf("origin = %q, want %q", origin, OriginProject)
	}
	if filepath.Base(filepath.Dir(path)) != ProjectDirName {
		t.Errorf("path = %q, want it inside %s", path, ProjectDirName)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("path %q is relative; it must survive a later chdir", path)
	}
}

func TestResolvePathDoesNotCreateAProjectDirectory(t *testing.T) {
	// Project-local storage is opt-in. Auto-creating it would make the store
	// silently depend on where the command was run from.
	t.Setenv("SYNCTRADES_CONFIG_DIR", "")
	dir := t.TempDir()
	t.Chdir(dir)

	_, origin, err := ResolvePath()
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if origin != OriginUserConfig {
		t.Errorf("origin = %q, want %q", origin, OriginUserConfig)
	}
	if _, err := os.Stat(filepath.Join(dir, ProjectDirName)); err == nil {
		t.Errorf("%s was created without being asked for", ProjectDirName)
	}
}

func TestResolvePathFallsBackToTheUserConfigDirectory(t *testing.T) {
	t.Setenv("SYNCTRADES_CONFIG_DIR", "")
	t.Chdir(t.TempDir())

	path, origin, err := ResolvePath()
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if origin != OriginUserConfig {
		t.Errorf("origin = %q, want %q", origin, OriginUserConfig)
	}

	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no user config dir on this platform: %v", err)
	}
	if !strings.HasPrefix(path, configDir) {
		t.Errorf("path %q is outside the user config directory %q", path, configDir)
	}
}
