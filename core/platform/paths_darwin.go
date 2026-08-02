//go:build darwin

package platform

import (
	"os"
	"path/filepath"
)

func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", AppName), nil
}

func dataDir() (string, error) { return configDir() }

// configSearchDirs prefers ~/.config over Application Support when both hold a
// config file: someone who put one there did so deliberately, while the
// Application Support copy may just be the one this tool created.
func configSearchDirs() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, 3)
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		dirs = append(dirs, filepath.Join(x, AppName))
	}
	dirs = append(dirs,
		filepath.Join(home, ".config", AppName),
		filepath.Join(home, "Library", "Application Support", AppName),
	)
	return dirs, nil
}
