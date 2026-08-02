//go:build darwin

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderDaemonConfig(t *testing.T) {
	got, err := RenderDaemonConfig("/opt/sensor-lens")
	if err != nil {
		t.Fatalf("RenderDaemonConfig() error = %v", err)
	}

	for _, want := range []string{
		"<string>" + daemonLabel + "</string>",
		"<string>/opt/sensor-lens</string>",
		"<string>daemon</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q:\n%s", want, got)
		}
	}

	// A resident job, not a per-tick one: the poller holds the running call
	// count and backoff state in memory.
	if strings.Contains(got, "StartInterval") {
		t.Error("plist uses StartInterval; the daemon is meant to be resident")
	}
	// The plist is world-readable, so credentials must never reach it.
	if strings.Contains(got, "EnvironmentVariables") {
		t.Error("plist carries EnvironmentVariables; credentials belong in the 0600 config file")
	}
}

func TestResolveDaemonLogPath(t *testing.T) {
	if got := resolveDaemonLogPath("/data", nil); got != filepath.Join("/data", "daemon.log") {
		t.Errorf("resolveDaemonLogPath() = %q, want it in the data dir", got)
	}

	// The fallback must stay inside the per-user $TMPDIR, never world-writable /tmp.
	got := resolveDaemonLogPath("", errors.New("no home"))
	if !strings.HasPrefix(got, os.TempDir()) {
		t.Errorf("fallback log path = %q, want it under %q", got, os.TempDir())
	}
}
