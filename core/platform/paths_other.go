//go:build !darwin

package platform

import (
	"os"
	"path/filepath"
)

func configDir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, AppName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", AppName), nil
}

func dataDir() (string, error) { return configDir() }
