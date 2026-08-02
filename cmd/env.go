package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/nlink-jp/sensor-lens/core/config"
	"github.com/nlink-jp/sensor-lens/core/platform"
	"github.com/nlink-jp/sensor-lens/core/poller"
	"github.com/nlink-jp/sensor-lens/core/store"
	"github.com/nlink-jp/sensor-lens/core/switchbot"
)

// env is the resolved runtime a command works in: config, paths and the store.
type env struct {
	cfg        config.Config
	configPath string
	dataDir    string
	store      *store.Store
}

// openEnv resolves config and opens the database. Callers must Close it.
func openEnv() (*env, error) {
	configPath, err := platform.ConfigFilePath()
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	dataDir, err := platform.DataDir()
	if err != nil {
		return nil, fmt.Errorf("resolve data dir: %w", err)
	}
	cfg, err := config.Load(configPath, dataDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", configPath, err)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	return &env{cfg: cfg, configPath: configPath, dataDir: dataDir, store: st}, nil
}

func (e *env) Close() error { return e.store.Close() }

// client builds an API client from the resolved credentials.
func (e *env) client() (*switchbot.Client, error) {
	if !e.cfg.HasCredentials() {
		return nil, fmt.Errorf("no SwitchBot credentials: set them in %s or via %s / %s (see `sensor-lens doctor`)",
			e.configPath, config.EnvToken, config.EnvSecret)
	}
	return switchbot.New(e.cfg.Token, e.cfg.Secret,
		switchbot.WithHTTPClient(&http.Client{
			Timeout: time.Duration(e.cfg.TimeoutSeconds) * time.Second,
		})), nil
}

// poller wires an API client to the store.
func (e *env) poller(verbose bool) (*poller.Poller, error) {
	client, err := e.client()
	if err != nil {
		return nil, err
	}
	opts := []poller.Option{}
	if verbose {
		opts = append(opts, poller.WithLogger(func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}))
	}
	return poller.New(client, e.store, e.cfg, opts...), nil
}

// interval is the nominal polling cadence, used for staleness and gap checks.
func (e *env) interval() time.Duration {
	return time.Duration(e.cfg.IntervalSeconds) * time.Second
}

// writeJSON prints v as indented JSON.
func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
