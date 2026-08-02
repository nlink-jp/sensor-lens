package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDirTightensLoosePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir() error = %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != DirMode {
		t.Errorf("mode = %04o, want %04o", got, DirMode)
	}
}

func TestEnsureDirAcceptsADirectoryItCannotTighten(t *testing.T) {
	// A database pointed at a shared or synced folder must still work: the
	// file's own 0600 is what protects the readings, and the directory is the
	// user's to set. /tmp is 1777 and not ours to change.
	if err := EnsureDir(os.TempDir()); err != nil {
		t.Errorf("EnsureDir(%q) error = %v, want nil", os.TempDir(), err)
	}
}

func TestCheckFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[switchbot]\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := CheckFileMode(path); err != nil {
		t.Errorf("CheckFileMode(0600) error = %v, want nil", err)
	}

	// A group- or world-readable file leaks the SwitchBot token to every other
	// account on the machine, so it must be reported.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if _, err := CheckFileMode(path); err == nil {
		t.Error("CheckFileMode(0644) = nil, want an error naming the credentials risk")
	}
}

func TestCheckFileModeMissingFile(t *testing.T) {
	if _, err := CheckFileMode(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("CheckFileMode(missing) = nil, want error")
	}
}

func TestConfigFilePath(t *testing.T) {
	got, err := ConfigFilePath()
	if err != nil {
		t.Fatalf("ConfigFilePath() error = %v", err)
	}
	if filepath.Base(got) != "config.toml" {
		t.Errorf("ConfigFilePath() = %q, want it to end in config.toml", got)
	}
	if filepath.Base(filepath.Dir(got)) != AppName {
		t.Errorf("ConfigFilePath() = %q, want it under a %s directory", got, AppName)
	}
}
