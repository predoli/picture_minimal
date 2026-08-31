// Package auth persists the Nextcloud credential the frame obtains for itself.
//
// The credential lives in the state directory rather than /etc because the
// daemon writes it, and because update.sh must never touch it: an update that
// forced every frame back to the pairing screen would be worse than no update.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Credentials is exactly what Login Flow v2 hands back, plus nothing.
type Credentials struct {
	Server      string `json:"server"`
	LoginName   string `json:"loginName"`
	AppPassword string `json:"appPassword"`
}

func (c Credentials) Valid() bool {
	return c.Server != "" && c.LoginName != "" && c.AppPassword != ""
}

// ErrNotPaired means the frame has no credential yet and should show the QR.
var ErrNotPaired = errors.New("not paired")

func Load(path string) (Credentials, error) {
	var creds Credentials

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return creds, ErrNotPaired
		}
		return creds, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		// A truncated or corrupt file is indistinguishable from never having
		// paired, and re-pairing is a recoverable, self-service action.
		return Credentials{}, ErrNotPaired
	}
	if !creds.Valid() {
		return Credentials{}, ErrNotPaired
	}
	return creds, nil
}

// Save writes the credential atomically at mode 0600.
//
// Login Flow v2 returns the app password exactly once, so a partial write here
// would strand the frame: it would believe it had paired while holding an
// unusable credential. Write to a temp file and rename.
func Save(path string, creds Credentials) error {
	if !creds.Valid() {
		return fmt.Errorf("refusing to save incomplete credentials")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".auth-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Clear drops the stored credential, sending the frame back to pairing. Called
// when Nextcloud rejects the app password, which is what the user revoking it
// looks like from here.
func Clear(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
