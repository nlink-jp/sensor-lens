package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg := Defaults("/data")

	if cfg.IntervalSeconds != 300 {
		t.Errorf("IntervalSeconds = %d, want 300", cfg.IntervalSeconds)
	}
	if cfg.DBPath != filepath.Join("/data", "sensors.db") {
		t.Errorf("DBPath = %q", cfg.DBPath)
	}
	// The budget must leave room under the account-wide daily limit for ad-hoc
	// calls and anything else using the same token.
	if cfg.DailyBudget >= DailyCallLimit {
		t.Errorf("DailyBudget = %d, want < %d", cfg.DailyBudget, DailyCallLimit)
	}
	if cfg.HasCredentials() {
		t.Error("HasCredentials() = true with no credentials configured")
	}
}

func TestApply(t *testing.T) {
	const toml = `
[switchbot]
token  = "TOK"   # inline comment must not leak into the value
secret = "SEC"

[polling]
interval_seconds = 120
devices = ["AAA", "BBB"]

[api]
daily_budget = 5000
timeout_seconds = 5

[storage]
db_path = "/tmp/x/sensors.db"
retention_days = 30
`
	cfg, err := apply(Defaults("/data"), []byte(toml))
	if err != nil {
		t.Fatalf("apply() error = %v", err)
	}

	if cfg.Token != "TOK" || cfg.Secret != "SEC" {
		t.Errorf("credentials = %q/%q, want TOK/SEC", cfg.Token, cfg.Secret)
	}
	if cfg.TokenSource != SourceFile {
		t.Errorf("TokenSource = %q, want %q", cfg.TokenSource, SourceFile)
	}
	if cfg.IntervalSeconds != 120 {
		t.Errorf("IntervalSeconds = %d, want 120", cfg.IntervalSeconds)
	}
	if want := []string{"AAA", "BBB"}; !reflect.DeepEqual(cfg.Devices, want) {
		t.Errorf("Devices = %v, want %v", cfg.Devices, want)
	}
	if cfg.DailyBudget != 5000 || cfg.TimeoutSeconds != 5 {
		t.Errorf("api = %d/%d, want 5000/5", cfg.DailyBudget, cfg.TimeoutSeconds)
	}
	if cfg.DBPath != "/tmp/x/sensors.db" || cfg.RetentionDays != 30 {
		t.Errorf("storage = %q/%d", cfg.DBPath, cfg.RetentionDays)
	}
}

func TestApplyRejectsBadValues(t *testing.T) {
	tests := map[string]string{
		// Faster than the meters themselves refresh — it only burns quota.
		"interval too small":   "[polling]\ninterval_seconds = 5\n",
		"interval not numeric": "[polling]\ninterval_seconds = often\n",
		"budget over limit":    "[api]\ndaily_budget = 20000\n",
		"budget zero":          "[api]\ndaily_budget = 0\n",
		"timeout zero":         "[api]\ntimeout_seconds = 0\n",
		"negative retention":   "[storage]\nretention_days = -1\n",
		"malformed section":    "[polling\ninterval_seconds = 60\n",
		"missing equals":       "[polling]\ninterval_seconds 60\n",
	}

	for name, toml := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := apply(Defaults("/data"), []byte(toml)); err == nil {
				t.Error("apply() succeeded, want error")
			}
		})
	}
}

func TestApplyDevicesForms(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
	}{
		{"array", `devices = ["AAA", "BBB"]`, []string{"AAA", "BBB"}},
		{"bare scalar", `devices = "AAA"`, []string{"AAA"}},
		{"empty array", `devices = []`, nil},
		{"names with spaces", `devices = ["Living Room", "Bedroom"]`, []string{"Living Room", "Bedroom"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := apply(Defaults("/data"), []byte("[polling]\n"+tc.line+"\n"))
			if err != nil {
				t.Fatalf("apply() error = %v", err)
			}
			if !reflect.DeepEqual(cfg.Devices, tc.want) {
				t.Errorf("Devices = %v, want %v", cfg.Devices, tc.want)
			}
		})
	}
}

func TestApplyEnvOverridesFile(t *testing.T) {
	cfg, err := apply(Defaults("/data"), []byte("[switchbot]\ntoken = \"FILE\"\nsecret = \"FILESEC\"\n"))
	if err != nil {
		t.Fatalf("apply() error = %v", err)
	}

	cfg = applyEnv(cfg, func(k string) string {
		if k == EnvToken {
			return "ENVTOK"
		}
		return ""
	})

	if cfg.Token != "ENVTOK" || cfg.TokenSource != SourceEnvVar {
		t.Errorf("token = %q from %q, want ENVTOK from environment", cfg.Token, cfg.TokenSource)
	}
	// An unset variable must not blank out what the file provided.
	if cfg.Secret != "FILESEC" || cfg.SecretSource != SourceFile {
		t.Errorf("secret = %q from %q, want FILESEC from config.toml", cfg.Secret, cfg.SecretSource)
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.toml"), "/data")
	if err != nil {
		t.Fatalf("Load() error = %v, want defaults", err)
	}
	if cfg.IntervalSeconds != Defaults("/data").IntervalSeconds {
		t.Error("Load() did not fall back to defaults")
	}
}

func TestProjectedDailyCalls(t *testing.T) {
	cfg := Defaults("/data") // 300 s

	// 6 devices every 5 minutes = 6 * 288 status calls + 1 device-list refresh.
	if got, want := cfg.ProjectedDailyCalls(6), 6*288+1; got != want {
		t.Errorf("ProjectedDailyCalls(6) = %d, want %d", got, want)
	}
	if got := cfg.ProjectedDailyCalls(0); got != 0 {
		t.Errorf("ProjectedDailyCalls(0) = %d, want 0", got)
	}
	// The default configuration must comfortably fit a realistic home.
	if got := cfg.ProjectedDailyCalls(6); got > cfg.DailyBudget {
		t.Errorf("defaults overspend the budget: %d > %d", got, cfg.DailyBudget)
	}
}

func TestMinIntervalSeconds(t *testing.T) {
	cfg := Defaults("/data")
	cfg.DailyBudget = 8000

	got := cfg.MinIntervalSeconds(20)
	cfg.IntervalSeconds = got
	if spend := cfg.ProjectedDailyCalls(20); spend > 8000 {
		t.Errorf("MinIntervalSeconds(20) = %d still spends %d > 8000", got, spend)
	}
	// One second faster must break the budget, or the suggestion is too lax.
	cfg.IntervalSeconds = got - 1
	if spend := cfg.ProjectedDailyCalls(20); spend <= 8000 {
		t.Errorf("MinIntervalSeconds(20) = %d is not tight: %d fits too", got, spend)
	}
}

func TestExpandHome(t *testing.T) {
	got := expandHome("~/sensors.db")
	if strings.HasPrefix(got, "~") {
		t.Errorf("expandHome() = %q, still starts with ~", got)
	}
	if got := expandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("expandHome(/abs/path) = %q, want it unchanged", got)
	}
}
