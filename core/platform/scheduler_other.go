//go:build !darwin

package platform

func renderDaemonConfig(string) (string, error) { return "", ErrDaemonUnsupported }
func installDaemon(string) (DaemonInfo, error)  { return DaemonInfo{}, ErrDaemonUnsupported }
func uninstallDaemon() (DaemonInfo, error)      { return DaemonInfo{}, ErrDaemonUnsupported }
func daemonStatus() (DaemonInfo, error)         { return DaemonInfo{}, ErrDaemonUnsupported }
