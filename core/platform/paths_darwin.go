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
