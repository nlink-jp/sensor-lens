// Package platform holds the OS-specific bits: where config and data live, and
// how the polling daemon is registered with the system scheduler. The daemon
// itself is portable; only its scheduling is macOS-specific, so non-darwin
// builds compile with a stub that tells you to run `sensor-lens daemon`
// yourself.
package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// AppName is the on-disk directory / service base name.
const AppName = "sensor-lens"

// File modes for the durable state. The config file holds the SwitchBot token
// and secret; the database is a minute-by-minute record of whether anyone is
// home. Both are owner-only.
const (
	DirMode  os.FileMode = 0o700
	FileMode os.FileMode = 0o600
)

// ConfigDir returns the per-user config directory.
func ConfigDir() (string, error) { return configDir() }

// DataDir returns the per-user durable data directory (holds the DB).
func DataDir() (string, error) { return dataDir() }

// ConfigFilePath returns the path to config.toml.
func ConfigFilePath() (string, error) {
	d, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.toml"), nil
}

// EnsureDir creates a directory owner-only, tightening one that already exists
// too loosely.
//
// Failing to tighten an existing directory is not an error: the user may have
// pointed the database at a shared or synced folder they do not own, and that
// is their call to make. What actually protects the readings is the mode on the
// database file itself, which is set separately and always ours to set.
func EnsureDir(path string) error {
	if err := os.MkdirAll(path, DirMode); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&^DirMode != 0 {
		_ = os.Chmod(path, DirMode)
	}
	return nil
}

// CheckFileMode reports whether a file is readable by anyone but its owner.
// Used by `doctor` to catch a config.toml that would leak the API token to
// other accounts on the machine.
func CheckFileMode(path string) (os.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		return mode, fmt.Errorf("%s is mode %04o; it holds credentials and should be %04o (chmod 600 it)", path, mode, FileMode)
	}
	return mode, nil
}
