// Package cmd is the sensor-lens CLI: a small hand-rolled dispatcher over the
// core packages. Each subcommand is a run* function returning an error, which
// Execute maps to a non-zero exit.
package cmd

import (
	"fmt"
	"os"
)

const usage = `sensor-lens — collect SwitchBot temperature, humidity and CO2 readings.

Usage:
  sensor-lens <command> [flags]

Commands:
  devices [--json] [--raw] [--refresh]   List the devices on your account
  now     [--json] [--devices ids]       Read the sensors right now
  daemon                                 Run the resident collector
  history [flags]                        One metric over time (--device --metric --since --until)
  report  [flags]                        min / max / avg per metric over a range
  gaps    [flags]                        Windows with no readings — what to import
  import  [flags]                        Merge a CSV exported from the SwitchBot app
  export  [flags]                        Write stored readings out (--format csv|json)
  status  [--json]                       Daemon state, DB path, calls spent today
  doctor                                 Diagnose config, credentials and quota
  install                                Register the login-time LaunchAgent
  uninstall                              Remove the LaunchAgent
  prune   --keep-days N                  Delete readings older than N days
  version                                Print the version

Credentials come from ` + "`config.toml`" + ` (or SWITCHBOT_TOKEN / SWITCHBOT_SECRET).
Run ` + "`sensor-lens doctor`" + ` to see what resolved on this machine.

The API has no history endpoint, so a stretch the daemon missed can only be
recovered from the app: export that window to CSV and ` + "`sensor-lens import`" + ` it.
Re-importing is safe — only genuinely missing samples are added.
`

// Execute runs the CLI. version is injected at build time.
func Execute(version string) {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Print(usage)
		return
	}
	name, rest := args[0], args[1:]

	var err error
	switch name {
	case "devices":
		err = runDevices(rest)
	case "now":
		err = runNow(rest)
	case "daemon":
		err = runDaemon(rest)
	case "history":
		err = runHistory(rest)
	case "report":
		err = runReport(rest)
	case "gaps":
		err = runGaps(rest)
	case "import":
		err = runImport(rest)
	case "export":
		err = runExport(rest)
	case "status":
		err = runStatus(rest)
	case "doctor":
		err = runDoctor(rest)
	case "install":
		err = runInstall(rest)
	case "uninstall":
		err = runUninstall(rest)
	case "prune":
		err = runPrune(rest)
	case "version", "--version", "-v":
		fmt.Printf("sensor-lens %s\n", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", name, usage)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "sensor-lens: %v\n", err)
		os.Exit(1)
	}
}
