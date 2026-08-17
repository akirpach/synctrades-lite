package store

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// stubKeyring replaces the OS keyring for the duration of a test and reports
// what was stored in it.
func stubKeyring(t *testing.T, get func(string, string) (string, error)) map[string]string {
	t.Helper()

	stored := map[string]string{}

	origGet, origSet, origDel := keyringGet, keyringSet, keyringDel
	t.Cleanup(func() { keyringGet, keyringSet, keyringDel = origGet, origSet, origDel })

	keyringGet = get
	keyringSet = func(service, user, value string) error {
		stored[service+"/"+user] = value
		return nil
	}
	keyringDel = func(service, user string) error {
		key := service + "/" + user
		if _, ok := stored[key]; !ok {
			return keyring.ErrNotFound
		}
		delete(stored, key)
		return nil
	}

	// Keep every file-fallback path inside a temp directory.
	t.Setenv("SYNCTRADES_CONFIG_DIR", t.TempDir())
	return stored
}

func notFound(string, string) (string, error) { return "", keyring.ErrNotFound }

func TestEncryptionKeyReadsAnExistingKeyringEntry(t *testing.T) {
	want := testKey()
	stubKeyring(t, func(string, string) (string, error) {
		return base64.StdEncoding.EncodeToString(want), nil
	})

	key, source, err := EncryptionKey()
	if err != nil {
		t.Fatalf("EncryptionKey: %v", err)
	}
	if source != KeyFromKeyring {
		t.Errorf("source = %q, want %q", source, KeyFromKeyring)
	}
	if string(key) != string(want) {
		t.Error("the returned key does not match the keyring entry")
	}
}

func TestEncryptionKeyMintsAndStoresOnFirstUse(t *testing.T) {
	stored := stubKeyring(t, notFound)

	key, source, err := EncryptionKey()
	if err != nil {
		t.Fatalf("EncryptionKey: %v", err)
	}
	if source != KeyFromKeyring {
		t.Errorf("source = %q, want %q", source, KeyFromKeyring)
	}
	if len(key) != keyLen {
		t.Errorf("key is %d bytes, want %d", len(key), keyLen)
	}

	saved, ok := stored[keyringService+"/"+keyringUser]
	if !ok {
		t.Fatal("the new key was not written to the keyring")
	}
	decoded, err := base64.StdEncoding.DecodeString(saved)
	if err != nil {
		t.Fatalf("the stored value is not base64: %v", err)
	}
	if string(decoded) != string(key) {
		t.Error("the stored key differs from the one returned")
	}
}

func TestEncryptionKeyRejectsACorruptKeyringEntry(t *testing.T) {
	// Better to stop than to silently mint a new key, which would leave the
	// user's existing credential file undecryptable with no explanation.
	stubKeyring(t, func(string, string) (string, error) { return "not base64 at all!!", nil })

	if _, _, err := EncryptionKey(); err == nil {
		t.Fatal("a corrupt keyring entry was accepted")
	}
}

func TestEncryptionKeyRejectsAWrongLengthKeyringEntry(t *testing.T) {
	stubKeyring(t, func(string, string) (string, error) {
		return base64.StdEncoding.EncodeToString([]byte("too short")), nil
	})

	_, _, err := EncryptionKey()
	if err == nil {
		t.Fatal("a short key was accepted")
	}
	if !strings.Contains(err.Error(), "bytes") {
		t.Errorf("error does not mention the length: %v", err)
	}
}

func TestEncryptionKeyFallsBackWhenTheKeyringIsUnavailable(t *testing.T) {
	// A headless Linux box has no Secret Service at all. Refusing to run there
	// would break the environment a CLI is most likely to be used in.
	stubKeyring(t, func(string, string) (string, error) {
		return "", errors.New("no keyring daemon")
	})

	key, source, err := EncryptionKey()
	if err != nil {
		t.Fatalf("EncryptionKey: %v", err)
	}
	if source != KeyFromFile {
		t.Errorf("source = %q, want %q", source, KeyFromFile)
	}
	if len(key) != keyLen {
		t.Errorf("key is %d bytes, want %d", len(key), keyLen)
	}
}

func TestEncryptionKeyFallsBackWhenTheKeyringRefusesToStore(t *testing.T) {
	origSet := keyringSet
	stubKeyring(t, notFound)
	keyringSet = func(string, string, string) error { return errors.New("access denied") }
	t.Cleanup(func() { keyringSet = origSet })

	_, source, err := EncryptionKey()
	if err != nil {
		t.Fatalf("EncryptionKey: %v", err)
	}
	if source != KeyFromFile {
		t.Errorf("source = %q, want %q", source, KeyFromFile)
	}
}

func TestFallbackKeyIsStableAcrossCalls(t *testing.T) {
	// If the fallback minted a new key each run, every previous save would
	// become undecryptable.
	stubKeyring(t, func(string, string) (string, error) {
		return "", errors.New("no keyring")
	})

	first, _, err := EncryptionKey()
	if err != nil {
		t.Fatalf("EncryptionKey: %v", err)
	}
	second, source, err := EncryptionKey()
	if err != nil {
		t.Fatalf("EncryptionKey: %v", err)
	}
	if source != KeyFromFile {
		t.Errorf("source = %q", source)
	}
	if string(first) != string(second) {
		t.Error("the fallback key changed between calls")
	}
}

func TestFallbackKeyFileIsRestricted(t *testing.T) {
	stubKeyring(t, func(string, string) (string, error) {
		return "", errors.New("no keyring")
	})
	if _, _, err := EncryptionKey(); err != nil {
		t.Fatalf("EncryptionKey: %v", err)
	}

	path, err := keyFilePath()
	if err != nil {
		t.Fatalf("keyFilePath: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the key file was not created: %v", err)
	}

	// Windows does not honor POSIX bits, so only assert where it means
	// something. Elsewhere the OS config directory ACL is the control.
	if os.PathSeparator == '/' && info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode is %v, want 0600", info.Mode().Perm())
	}
}

func TestFallbackRejectsACorruptKeyFile(t *testing.T) {
	stubKeyring(t, func(string, string) (string, error) {
		return "", errors.New("no keyring")
	})

	path, err := keyFilePath()
	if err != nil {
		t.Fatalf("keyFilePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("}}} not base64"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, _, err := EncryptionKey(); err == nil {
		t.Error("a corrupt key file was accepted")
	}
}

func TestDeleteEncryptionKeyRemovesBothLocations(t *testing.T) {
	stored := stubKeyring(t, notFound)

	if _, _, err := EncryptionKey(); err != nil {
		t.Fatalf("EncryptionKey: %v", err)
	}
	if len(stored) == 0 {
		t.Fatal("nothing was stored to delete")
	}

	// Also leave a fallback file behind, so both paths are exercised.
	path, err := keyFilePath()
	if err != nil {
		t.Fatalf("keyFilePath: %v", err)
	}
	if err := writeKeyFile(path, testKey()); err != nil {
		t.Fatalf("writeKeyFile: %v", err)
	}

	if err := DeleteEncryptionKey(); err != nil {
		t.Fatalf("DeleteEncryptionKey: %v", err)
	}
	if len(stored) != 0 {
		t.Errorf("the keyring entry survived: %v", stored)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("the key file survived")
	}
}

func TestDeleteEncryptionKeyIsIdempotent(t *testing.T) {
	stubKeyring(t, notFound)
	if err := DeleteEncryptionKey(); err != nil {
		t.Errorf("deleting a nonexistent key failed: %v", err)
	}
}
