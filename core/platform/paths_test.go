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
	if filepath.Base(got) != ConfigFileName {
		t.Errorf("ConfigFilePath() = %q, want it to end in %s", got, ConfigFileName)
	}
	if filepath.Base(filepath.Dir(got)) != AppName {
		t.Errorf("ConfigFilePath() = %q, want it under a %s directory", got, AppName)
	}
}

func TestConfigSearchPathsAreDistinctAndNamed(t *testing.T) {
	paths, err := ConfigSearchPaths()
	if err != nil {
		t.Fatalf("ConfigSearchPaths() error = %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("ConfigSearchPaths() = none")
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			t.Errorf("duplicate search path %q", p)
		}
		seen[p] = true
		if filepath.Base(p) != ConfigFileName {
			t.Errorf("search path %q does not end in %s", p, ConfigFileName)
		}
	}
}

func TestConfigFilePathPrefersAnExistingFile(t *testing.T) {
	// XDG_CONFIG_HOME is searched first, so a file placed there must win over
	// the platform default — a config that exists and is silently never read is
	// the worst possible outcome.
	//
	// HOME is redirected too, or the developer's own config file joins the
	// search and the test stops being about anything.
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", dir)

	want := filepath.Join(dir, AppName, ConfigFileName)
	if err := os.MkdirAll(filepath.Dir(want), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(want, []byte("[switchbot]\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := ConfigFilePath()
	if err != nil {
		t.Fatalf("ConfigFilePath() error = %v", err)
	}
	if got != want {
		t.Errorf("ConfigFilePath() = %q, want the existing %q", got, want)
	}
}

func TestConfigFilePathFallsBackToCanonical(t *testing.T) {
	t.Setenv("HOME", t.TempDir())            // no real config to stumble on
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // empty: nothing to find

	got, err := ConfigFilePath()
	if err != nil {
		t.Fatalf("ConfigFilePath() error = %v", err)
	}
	canonical, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error = %v", err)
	}
	if want := filepath.Join(canonical, ConfigFileName); got != want {
		t.Errorf("ConfigFilePath() = %q, want the canonical %q", got, want)
	}
}
