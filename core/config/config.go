// Package config resolves runtime configuration from a small, optional TOML
// file overlaid on built-in defaults, with environment overrides for the
// credentials. It uses a minimal hand-rolled parser (flat sectioned
// key = value, plus single-line arrays) to avoid an external dependency — the
// config surface is tiny and fully controlled.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Environment variables that override the credentials in the file. They exist
// for interactive use; the daemon runs under launchd where the environment is
// not the user's, so the file stays the canonical source for it.
const (
	EnvToken  = "SWITCHBOT_TOKEN"
	EnvSecret = "SWITCHBOT_SECRET"
)

// DailyCallLimit is the quota SwitchBot enforces per account per day. Going
// over it makes the API answer "Unauthorized" — the same response an invalid
// token produces — so staying under it is the caller's job.
const DailyCallLimit = 10000

// CredentialSource says where a credential was resolved from, so `doctor` can
// explain what it is actually using.
type CredentialSource string

const (
	SourceUnset  CredentialSource = "unset"
	SourceFile   CredentialSource = "config.toml"
	SourceEnvVar CredentialSource = "environment"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	// Token and Secret authenticate against the SwitchBot Open API.
	Token  string
	Secret string
	// TokenSource and SecretSource record where each came from.
	TokenSource  CredentialSource
	SecretSource CredentialSource

	// IntervalSeconds is how often the daemon polls each collected device.
	IntervalSeconds int
	// Devices is the collect set: the device IDs or names to poll. Empty means
	// every device whose status carries an ambient measurement.
	//
	// This is *not* the set shown in the menu bar — the GUI owns that choice
	// separately, so you can collect a whole house and display two readings.
	Devices []string

	// DailyBudget caps the API calls sensor-lens will spend in a local day. It
	// sits below DailyCallLimit so that ad-hoc `now` / `devices` calls, and
	// anything else using the same account, still have room.
	DailyBudget int
	// TimeoutSeconds bounds each HTTP exchange.
	TimeoutSeconds int

	// DBPath is where readings are stored.
	DBPath string
	// RetentionDays is how long raw readings are kept; 0 means forever.
	RetentionDays int
}

// Defaults returns the built-in defaults. dataDir seeds DBPath.
func Defaults(dataDir string) Config {
	return Config{
		TokenSource:     SourceUnset,
		SecretSource:    SourceUnset,
		IntervalSeconds: 300,
		DailyBudget:     8000,
		TimeoutSeconds:  20,
		DBPath:          filepath.Join(dataDir, "sensors.db"),
		RetentionDays:   400,
	}
}

// Load resolves config from path (which may be absent) overlaid on
// Defaults(dataDir), then applies the credential environment overrides.
func Load(path, dataDir string) (Config, error) {
	cfg := Defaults(dataDir)
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return cfg, err
	}
	if err == nil {
		if cfg, err = apply(cfg, data); err != nil {
			return cfg, err
		}
	}
	return applyEnv(cfg, os.Getenv), nil
}

// applyEnv overlays the credential environment variables. Separated from Load
// so tests need not mutate the process environment.
func applyEnv(cfg Config, getenv func(string) string) Config {
	if v := getenv(EnvToken); v != "" {
		cfg.Token, cfg.TokenSource = v, SourceEnvVar
	}
	if v := getenv(EnvSecret); v != "" {
		cfg.Secret, cfg.SecretSource = v, SourceEnvVar
	}
	return cfg
}

// HasCredentials reports whether both halves of the signature are available.
func (c Config) HasCredentials() bool { return c.Token != "" && c.Secret != "" }

// ProjectedDailyCalls returns how many status calls the daemon will spend in a
// day polling deviceCount devices at the configured interval. The device list
// costs one more call per refresh, which is once a day.
func (c Config) ProjectedDailyCalls(deviceCount int) int {
	if c.IntervalSeconds <= 0 || deviceCount <= 0 {
		return 0
	}
	const secondsPerDay = 86400
	return deviceCount*(secondsPerDay/c.IntervalSeconds) + 1
}

// MinIntervalSeconds returns the shortest polling interval that keeps
// deviceCount devices inside the configured daily budget.
func (c Config) MinIntervalSeconds(deviceCount int) int {
	if deviceCount <= 0 || c.DailyBudget <= 1 {
		return c.IntervalSeconds
	}
	const secondsPerDay = 86400
	perDevice := (c.DailyBudget - 1) / deviceCount
	if perDevice <= 0 {
		return secondsPerDay
	}
	interval := secondsPerDay / perDevice
	if secondsPerDay%perDevice != 0 {
		interval++
	}
	return interval
}

// apply overlays parsed TOML onto cfg. Exposed via Load; separated for testing.
func apply(cfg Config, data []byte) (Config, error) {
	kv, err := parse(data)
	if err != nil {
		return cfg, err
	}

	if v, ok := kv["switchbot.token"]; ok && v != "" {
		cfg.Token, cfg.TokenSource = v, SourceFile
	}
	if v, ok := kv["switchbot.secret"]; ok && v != "" {
		cfg.Secret, cfg.SecretSource = v, SourceFile
	}
	if v, ok := kv["polling.interval_seconds"]; ok {
		n, err := strconv.Atoi(v)
		// Below 10 s the quota evaporates and the meters have not refreshed
		// anyway — SwitchBot devices report every minute or two.
		if err != nil || n < 10 {
			return cfg, fmt.Errorf("polling.interval_seconds: want integer >= 10, got %q", v)
		}
		cfg.IntervalSeconds = n
	}
	if v, ok := kv["polling.devices"]; ok {
		cfg.Devices = splitList(v)
	}
	if v, ok := kv["api.daily_budget"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > DailyCallLimit {
			return cfg, fmt.Errorf("api.daily_budget: want integer 1..%d, got %q", DailyCallLimit, v)
		}
		cfg.DailyBudget = n
	}
	if v, ok := kv["api.timeout_seconds"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return cfg, fmt.Errorf("api.timeout_seconds: want positive integer, got %q", v)
		}
		cfg.TimeoutSeconds = n
	}
	if v, ok := kv["storage.db_path"]; ok && v != "" {
		cfg.DBPath = expandHome(v)
	}
	if v, ok := kv["storage.retention_days"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return cfg, fmt.Errorf("storage.retention_days: want non-negative integer (0 = keep forever), got %q", v)
		}
		cfg.RetentionDays = n
	}
	return cfg, nil
}

// expandHome expands a leading ~ to the user's home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

// parse reads a flat sectioned TOML into "section.key" -> value. Only scalars
// and single-line arrays are supported; surrounding quotes are stripped and
// trailing unquoted comments removed.
func parse(data []byte) (map[string]string, error) {
	out := map[string]string{}
	section := ""
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("line %d: malformed section header %q", i+1, line)
			}
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("line %d: expected key = value, got %q", i+1, line)
		}
		key := strings.TrimSpace(line[:eq])
		val := parseValue(strings.TrimSpace(line[eq+1:]))
		if section != "" {
			key = section + "." + key
		}
		out[key] = val
	}
	return out, nil
}

// parseValue returns the value's content: for a quoted value the text inside
// the quotes, for an array the raw bracketed text, otherwise the scalar with
// any trailing # comment trimmed.
func parseValue(v string) string {
	if len(v) > 0 && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			return v[1 : 1+end]
		}
		// Unterminated quote: fall through to comment-trimming.
	}
	if strings.HasPrefix(v, "[") {
		if end := strings.IndexByte(v, ']'); end >= 0 {
			return v[:end+1]
		}
	}
	if h := strings.IndexByte(v, '#'); h >= 0 {
		v = strings.TrimSpace(v[:h])
	}
	return v
}

// splitList turns a single-line TOML array — `["a", "b"]` — into its elements.
// A bare scalar is accepted as a one-element list so `devices = "AAA"` works.
func splitList(v string) []string {
	v = strings.TrimSpace(v)
	v = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")

	var out []string
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, `"'`)
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
