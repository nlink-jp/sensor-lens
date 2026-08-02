package platform

import "errors"

// ErrDaemonUnsupported is returned by the daemon functions on non-darwin.
var ErrDaemonUnsupported = errors.New(
	"daemon scheduling is only built-in on macOS — run `sensor-lens daemon` manually (or from your own supervisor)")

// DaemonInfo describes the installed (or would-be) polling service.
type DaemonInfo struct {
	Kind       string // e.g. "launchd"
	Label      string // service identifier
	ConfigPath string // where the service config lives
	Loaded     bool   // whether it is currently registered/loaded

	// ProgramPath is the binary the service was installed to run. launchd
	// records an absolute path at install time, so a binary that later moves —
	// most easily by living inside a .app someone drags to the Trash — leaves a
	// service that fails silently every time it fires.
	ProgramPath string
	// ProgramMissing reports that ProgramPath no longer exists.
	ProgramMissing bool
}

// RenderDaemonConfig returns the scheduler config that would run
// `<binPath> daemon` as a resident LaunchAgent, without installing anything.
func RenderDaemonConfig(binPath string) (string, error) {
	return renderDaemonConfig(binPath)
}

// InstallDaemon writes and registers the resident polling service.
func InstallDaemon(binPath string) (DaemonInfo, error) {
	return installDaemon(binPath)
}

// UninstallDaemon unregisters and removes the service.
func UninstallDaemon() (DaemonInfo, error) { return uninstallDaemon() }

// DaemonStatus reports whether the service is installed/loaded.
func DaemonStatus() (DaemonInfo, error) { return daemonStatus() }
