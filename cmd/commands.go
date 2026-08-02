package cmd

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nlink-jp/sensor-lens/core/aggregate"
	"github.com/nlink-jp/sensor-lens/core/config"
	"github.com/nlink-jp/sensor-lens/core/importer"
	"github.com/nlink-jp/sensor-lens/core/platform"
	"github.com/nlink-jp/sensor-lens/core/store"
)

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// ---------------------------------------------------------------- devices

func runDevices(args []string) error {
	fs := newFlagSet("devices")
	asJSON := fs.Bool("json", false, "emit JSON")
	raw := fs.Bool("raw", false, "print the untouched API response for each device")
	refresh := fs.Bool("refresh", false, "re-read the device list from the API")
	reclassify := fs.Bool("reclassify", false, "re-decide which devices are collected, re-probing known ones (costs one call per device)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := openEnv()
	if err != nil {
		return err
	}
	defer e.Close()
	ctx := signalContext()

	if *raw {
		return printRawStatuses(ctx, e, *asJSON)
	}

	devices, err := e.store.Devices(ctx)
	if err != nil {
		return err
	}
	if *refresh || *reclassify || len(devices) == 0 {
		p, err := e.poller(!*asJSON)
		if err != nil {
			return err
		}
		if devices, err = p.RefreshDevices(ctx, *reclassify); err != nil {
			return err
		}
	}

	if *asJSON {
		return writeJSON(devices)
	}
	fmt.Print(FormatDevices(devices))
	return nil
}

// printRawStatuses dumps what the API actually returns. Device types and their
// fields are not stable across models or firmware, so this is the ground truth
// when a reading is missing or a new field appears.
func printRawStatuses(ctx context.Context, e *env, asJSON bool) error {
	client, err := e.client()
	if err != nil {
		return err
	}
	remote, err := client.Devices(ctx)
	if err != nil {
		return err
	}

	out := make(map[string]any, len(remote))
	for _, d := range remote {
		body, err := client.DeviceStatus(ctx, d.DeviceID)
		if err != nil {
			out[d.DeviceName] = map[string]any{"error": err.Error()}
			continue
		}
		out[d.DeviceName] = body
	}
	if _, err := e.store.AddAPICalls(ctx, time.Now().Format("2006-01-02"), client.Calls()); err != nil {
		return err
	}
	return writeJSON(out)
}

// ---------------------------------------------------------------- now

func runNow(args []string) error {
	fs := newFlagSet("now")
	asJSON := fs.Bool("json", false, "emit JSON")
	devices := fs.String("devices", "", "comma-separated device IDs or names to show (default: all collected)")
	stored := fs.Bool("stored", false, "show the last stored readings instead of polling the API")
	ifStale := fs.Bool("if-stale", false, "poll only if the stored readings have gone stale")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := openEnv()
	if err != nil {
		return err
	}
	defer e.Close()
	ctx := signalContext()

	// --if-stale is how a front end collects on its own timer without needing
	// to know whether something else already is. If a daemon is running, the
	// readings are fresh and this costs nothing; if nothing is running, this
	// tick becomes the collector. No coordination protocol required.
	if *ifStale {
		last, err := e.store.LastReadingTime(ctx)
		if err != nil {
			return err
		}
		if !aggregate.IsStale(last, time.Now().Unix(), e.interval(), 1.0) {
			*stored = true
		}
	}

	if !*stored {
		p, err := e.poller(false)
		if err != nil {
			return err
		}
		known, err := e.store.Devices(ctx)
		if err != nil {
			return err
		}
		if len(known) == 0 {
			if _, err := p.RefreshDevices(ctx, false); err != nil {
				return err
			}
		}
		res, err := p.PollOnce(ctx)
		if err != nil {
			return err
		}
		for _, de := range res.Errors {
			fmt.Fprintf(os.Stderr, "sensor-lens: %s: %s\n", de.Name, de.Err)
		}
	}

	latest, err := e.store.Latest(ctx)
	if err != nil {
		return err
	}
	known, err := e.store.Devices(ctx)
	if err != nil {
		return err
	}

	grouped := GroupReadings(FilterCollected(latest, known), known, time.Now().Unix(), e.interval())
	if filter := splitCSV(*devices); len(filter) > 0 {
		grouped = filterReadings(grouped, filter)
	}

	if *asJSON {
		return writeJSON(grouped)
	}
	fmt.Print(FormatNow(grouped))
	return nil
}

// filterReadings keeps the entries matching any of the given IDs or names.
func filterReadings(in []DeviceReading, want []string) []DeviceReading {
	var out []DeviceReading
	for _, dr := range in {
		for _, w := range want {
			if strings.EqualFold(w, dr.DeviceID) || strings.EqualFold(w, dr.Name) {
				out = append(out, dr)
				break
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- daemon

func runDaemon(args []string) error {
	fs := newFlagSet("daemon")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := openEnv()
	if err != nil {
		return err
	}
	defer e.Close()

	// Only one collector per database. Two would not corrupt anything, but
	// would quietly spend twice the daily quota.
	lock, err := platform.AcquireCollectorLock(e.cfg.DBPath)
	if err != nil {
		if errors.Is(err, platform.ErrLocked) {
			return fmt.Errorf("%w — stop the other one (`sensor-lens uninstall`, or quit the menu-bar app) before starting this", err)
		}
		return err
	}
	defer lock.Release()

	p, err := e.poller(true)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "sensor-lens: polling every %ds, budget %d calls/day, db %s\n",
		e.cfg.IntervalSeconds, e.cfg.DailyBudget, e.cfg.DBPath)

	// A cancelled context is how the daemon is asked to stop, not a failure.
	if err := p.Run(signalContext()); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// ---------------------------------------------------------------- history

func runHistory(args []string) error {
	fs := newFlagSet("history")
	device := fs.String("device", "", "device ID or name (required)")
	metric := fs.String("metric", "", "metric name, e.g. co2_ppm (required)")
	since := fs.String("since", "-24h", "start of the range")
	until := fs.String("until", "now", "end of the range")
	bucket := fs.String("bucket", "", "downsample into buckets of this size, e.g. 5m")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *device == "" || *metric == "" {
		return errors.New("history needs --device and --metric (see `sensor-lens devices`)")
	}

	e, err := openEnv()
	if err != nil {
		return err
	}
	defer e.Close()
	ctx := signalContext()

	from, to, err := parseRange(*since, *until)
	if err != nil {
		return err
	}
	id, err := resolveDevice(ctx, e, *device)
	if err != nil {
		return err
	}

	readings, err := e.store.History(ctx, id, *metric, from.Unix(), to.Unix())
	if err != nil {
		return err
	}

	if *bucket != "" {
		d, err := time.ParseDuration(*bucket)
		if err != nil || d <= 0 {
			return fmt.Errorf("--bucket: want a positive duration like 5m, got %q", *bucket)
		}
		buckets := aggregate.Downsample(readings, int64(d/time.Second))
		if *asJSON {
			return writeJSON(buckets)
		}
		for _, b := range buckets {
			fmt.Printf("%s  n=%-4d min=%-8s avg=%-8s max=%s\n",
				time.Unix(b.Start, 0).Format("2006-01-02 15:04"), b.Count,
				FormatValue(*metric, b.Min), FormatValue(*metric, b.Avg), FormatValue(*metric, b.Max))
		}
		return nil
	}

	if *asJSON {
		return writeJSON(readings)
	}
	for _, r := range readings {
		fmt.Printf("%s  %s\n", time.Unix(r.TS, 0).Format("2006-01-02 15:04:05"), FormatValue(r.Metric, r.Value))
	}
	return nil
}

// ---------------------------------------------------------------- report

func runReport(args []string) error {
	fs := newFlagSet("report")
	since := fs.String("since", "-24h", "start of the range")
	until := fs.String("until", "now", "end of the range")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := openEnv()
	if err != nil {
		return err
	}
	defer e.Close()
	ctx := signalContext()

	from, to, err := parseRange(*since, *until)
	if err != nil {
		return err
	}
	readings, err := e.store.Range(ctx, from.Unix(), to.Unix())
	if err != nil {
		return err
	}
	devices, err := e.store.Devices(ctx)
	if err != nil {
		return err
	}

	summaries := aggregate.Summarize(readings)
	if *asJSON {
		return writeJSON(summaries)
	}
	fmt.Print(FormatSummaries(summaries, devices))
	return nil
}

// ---------------------------------------------------------------- gaps

func runGaps(args []string) error {
	fs := newFlagSet("gaps")
	since := fs.String("since", "-7d", "start of the range")
	until := fs.String("until", "now", "end of the range")
	factor := fs.Float64("factor", aggregate.DefaultGapFactor, "how many intervals of silence count as a gap")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := openEnv()
	if err != nil {
		return err
	}
	defer e.Close()
	ctx := signalContext()

	from, to, err := parseRange(*since, *until)
	if err != nil {
		return err
	}
	readings, err := e.store.Range(ctx, from.Unix(), to.Unix())
	if err != nil {
		return err
	}
	devices, err := e.store.Devices(ctx)
	if err != nil {
		return err
	}

	// A device no longer collected would otherwise report one endless gap.
	gaps := aggregate.Gaps(FilterCollected(readings, devices), aggregate.GapOptions{
		Expected: e.interval(),
		Factor:   *factor,
		Since:    from.Unix(),
		Until:    to.Unix(),
	})

	if *asJSON {
		return writeJSON(gaps)
	}
	fmt.Print(FormatGaps(gaps, devices))
	return nil
}

// ---------------------------------------------------------------- import

func runImport(args []string) error {
	fs := newFlagSet("import")
	file := fs.String("file", "", "CSV exported from the SwitchBot app (required)")
	device := fs.String("device", "", "device ID or name (default: taken from the filename)")
	dryRun := fs.Bool("dry-run", false, "report what would be added without writing")
	derived := fs.Bool("all-columns", false, "also import the app's computed columns (dew point, VPD, absolute humidity)")
	tz := fs.String("tz", "", "IANA zone the file's timestamps are in (default: this machine's)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("import needs --file (SwitchBot app → device → Export Data)")
	}

	e, err := openEnv()
	if err != nil {
		return err
	}
	defer e.Close()
	ctx := signalContext()

	// The CSV carries no device identity, only the name the app put in the
	// filename, so the device has to come from the flag or from that name.
	name := *device
	if name == "" {
		name = importer.DeviceNameFromFilename(*file)
		if name == "" {
			return fmt.Errorf("cannot tell which device %s belongs to: pass --device", *file)
		}
	}
	id, err := resolveDevice(ctx, e, name)
	if err != nil {
		return err
	}

	loc := time.Local
	if *tz != "" {
		if loc, err = time.LoadLocation(*tz); err != nil {
			return fmt.Errorf("--tz: %w", err)
		}
	}

	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer f.Close()

	parsed, err := importer.Parse(f, importer.Options{
		DeviceID: id, Location: loc, IncludeDerived: *derived,
	})
	if err != nil {
		return err
	}

	inserted, err := e.store.InsertReadings(ctx, parsed.Readings, *dryRun)
	if err != nil {
		return err
	}

	result := struct {
		importer.Result
		Device    string `json:"device"`
		DeviceID  string `json:"device_id"`
		Parsed    int    `json:"parsed_readings"`
		New       int    `json:"new_readings"`
		Duplicate int    `json:"duplicate_readings"`
		DryRun    bool   `json:"dry_run"`
	}{
		Result: parsed, Device: name, DeviceID: id,
		Parsed: len(parsed.Readings), New: inserted,
		Duplicate: len(parsed.Readings) - inserted, DryRun: *dryRun,
	}

	if *asJSON {
		return writeJSON(result)
	}

	verb := "imported"
	if *dryRun {
		verb = "would import"
	}
	fmt.Printf("%s: %d rows, %s %d new reading(s), %d already stored\n",
		name, parsed.Rows, verb, inserted, result.Duplicate)
	fmt.Printf("  window   %s .. %s\n",
		time.Unix(parsed.First, 0).Format("2006-01-02 15:04"),
		time.Unix(parsed.Last, 0).Format("2006-01-02 15:04"))
	fmt.Printf("  metrics  %s\n", strings.Join(parsed.Metrics, ", "))
	if len(parsed.Ignored) > 0 {
		fmt.Printf("  ignored  %s\n", strings.Join(parsed.Ignored, ", "))
	}
	if parsed.Skipped > 0 {
		fmt.Printf("  skipped  %d unreadable row(s)\n", parsed.Skipped)
		for _, re := range parsed.Errors {
			fmt.Printf("           line %d: %s\n", re.Line, re.Err)
		}
	}
	return nil
}

// ---------------------------------------------------------------- export

func runExport(args []string) error {
	fs := newFlagSet("export")
	format := fs.String("format", "csv", "csv or json")
	since := fs.String("since", "-7d", "start of the range")
	until := fs.String("until", "now", "end of the range")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := openEnv()
	if err != nil {
		return err
	}
	defer e.Close()
	ctx := signalContext()

	from, to, err := parseRange(*since, *until)
	if err != nil {
		return err
	}
	readings, err := e.store.Range(ctx, from.Unix(), to.Unix())
	if err != nil {
		return err
	}

	switch *format {
	case "json":
		return writeJSON(readings)
	case "csv":
		w := csv.NewWriter(os.Stdout)
		defer w.Flush()
		if err := w.Write([]string{"timestamp", "device_id", "metric", "value"}); err != nil {
			return err
		}
		for _, r := range readings {
			if err := w.Write([]string{
				time.Unix(r.TS, 0).Format(time.RFC3339),
				r.DeviceID, r.Metric,
				strconv.FormatFloat(r.Value, 'f', -1, 64),
			}); err != nil {
				return err
			}
		}
		return w.Error()
	default:
		return fmt.Errorf("--format: want csv or json, got %q", *format)
	}
}

// ---------------------------------------------------------------- status

// Status is what `status --json` emits; the GUI reads it.
type Status struct {
	Version      string `json:"schema_version"`
	DBPath       string `json:"db_path"`
	ConfigPath   string `json:"config_path"`
	DaemonKind   string `json:"daemon_kind,omitempty"`
	DaemonLoaded bool   `json:"daemon_loaded"`
	Installed    bool   `json:"daemon_installed"`
	Interval     int    `json:"interval_seconds"`
	Devices      int    `json:"devices"`
	Collected    int    `json:"collected"`
	Readings     int64  `json:"readings"`
	LastReading  int64  `json:"last_reading_ts"`
	Stale        bool   `json:"stale"`
	// Collecting says whether readings are arriving, judged purely by how
	// recent the newest one is — deliberately not by whether the LaunchAgent is
	// loaded. Collection may be coming from the daemon, from a menu-bar app
	// ticking `now --if-stale`, or from a daemon started by hand, and an
	// indicator that only believed in launchd would call two of those three
	// "not collecting" while data was visibly arriving.
	Collecting    bool `json:"collecting"`
	CallsToday    int  `json:"calls_today"`
	DailyBudget   int  `json:"daily_budget"`
	ProjectedDay  int  `json:"projected_calls_per_day"`
	HasCredential bool `json:"has_credentials"`
}

func runStatus(args []string) error {
	fs := newFlagSet("status")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := openEnv()
	if err != nil {
		return err
	}
	defer e.Close()
	ctx := signalContext()

	st, err := collectStatus(ctx, e)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(st)
	}

	fmt.Printf("collecting    %s\n", collectingWord(st))
	fmt.Printf("daemon        %s\n", daemonWord(st))
	fmt.Printf("interval      %ds\n", st.Interval)
	fmt.Printf("devices       %d known, %d collected\n", st.Devices, st.Collected)
	fmt.Printf("readings      %d stored\n", st.Readings)
	if st.LastReading > 0 {
		age := time.Since(time.Unix(st.LastReading, 0))
		note := ""
		if st.Stale {
			note = "  (stale)"
		}
		fmt.Printf("last reading  %s (%s ago)%s\n",
			time.Unix(st.LastReading, 0).Format("2006-01-02 15:04:05"), FormatDuration(age), note)
	} else {
		fmt.Printf("last reading  none yet\n")
	}
	fmt.Printf("api calls     %d today of %d budgeted (%d projected per day)\n",
		st.CallsToday, st.DailyBudget, st.ProjectedDay)
	fmt.Printf("database      %s\n", st.DBPath)
	fmt.Printf("config        %s\n", st.ConfigPath)
	return nil
}

func collectStatus(ctx context.Context, e *env) (Status, error) {
	st := Status{
		DBPath:        e.cfg.DBPath,
		ConfigPath:    e.configPath,
		Interval:      e.cfg.IntervalSeconds,
		DailyBudget:   e.cfg.DailyBudget,
		HasCredential: e.cfg.HasCredentials(),
	}

	devices, err := e.store.Devices(ctx)
	if err != nil {
		return st, err
	}
	st.Devices = len(devices)
	for _, d := range devices {
		if d.Enabled {
			st.Collected++
		}
	}
	st.ProjectedDay = e.cfg.ProjectedDailyCalls(st.Collected)

	if st.Readings, err = e.store.CountReadings(ctx); err != nil {
		return st, err
	}
	if st.LastReading, err = e.store.LastReadingTime(ctx); err != nil {
		return st, err
	}
	st.Stale = aggregate.IsStale(st.LastReading, time.Now().Unix(), e.interval(), 0)
	st.Collecting = st.LastReading > 0 && !st.Stale

	if st.CallsToday, err = e.store.APICalls(ctx, time.Now().Format("2006-01-02")); err != nil {
		return st, err
	}

	// Daemon scheduling only exists on macOS; elsewhere the fields stay empty
	// rather than turning the whole command into an error.
	if info, err := platform.DaemonStatus(); err == nil {
		st.DaemonKind = info.Kind
		st.DaemonLoaded = info.Loaded
		st.Installed = info.ConfigPath != "" && fileExists(info.ConfigPath)
	}
	return st, nil
}

// collectingWord describes whether data is arriving, without claiming to know
// who is gathering it.
func collectingWord(st Status) string {
	switch {
	case st.Collecting && st.DaemonLoaded:
		return "yes (daemon)"
	case st.Collecting:
		return "yes (something is polling — a menu-bar app, or a daemon run by hand)"
	case st.LastReading == 0:
		return "no readings yet"
	default:
		return "no — nothing has polled recently"
	}
}

func daemonWord(st Status) string {
	switch {
	case st.DaemonLoaded:
		return "running (launchd)"
	case st.Installed:
		return "installed but not loaded"
	case st.DaemonKind == "":
		return "not managed on this platform — run `sensor-lens daemon` yourself"
	default:
		return "not installed — run `sensor-lens install`"
	}
}

// ---------------------------------------------------------------- doctor

func runDoctor(args []string) error {
	fs := newFlagSet("doctor")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := openEnv()
	if err != nil {
		return err
	}
	defer e.Close()
	ctx := signalContext()

	fmt.Printf("config file   %s\n", e.configPath)
	if _, err := os.Stat(e.configPath); err == nil {
		if mode, err := platform.CheckFileMode(e.configPath); err != nil {
			fmt.Printf("  ✗ %v\n", err)
		} else {
			fmt.Printf("  ✓ mode %04o\n", mode)
		}
	} else {
		// Say where it looked. A config file that exists somewhere unread is
		// otherwise indistinguishable from one that was never written.
		fmt.Printf("  · absent (defaults in use); searched:\n")
		if paths, err := platform.ConfigSearchPaths(); err == nil {
			for _, p := range paths {
				fmt.Printf("      %s\n", p)
			}
		}
	}

	fmt.Printf("credentials   token from %s, secret from %s\n", e.cfg.TokenSource, e.cfg.SecretSource)
	if !e.cfg.HasCredentials() {
		fmt.Printf("  ✗ missing — create the token in the SwitchBot app (Profile →\n" +
			"    Preferences → tap App Version 10 times → Developer Options), then put it\n" +
			"    in the config file or set " + config.EnvToken + " / " + config.EnvSecret + "\n")
	} else {
		fmt.Printf("  ✓ present\n")
	}

	fmt.Printf("database      %s\n", e.cfg.DBPath)
	n, err := e.store.CountReadings(ctx)
	if err != nil {
		fmt.Printf("  ✗ %v\n", err)
	} else {
		fmt.Printf("  ✓ %d readings stored\n", n)
	}

	devices, err := e.store.Devices(ctx)
	if err != nil {
		return err
	}
	collected := 0
	for _, d := range devices {
		if d.Enabled {
			collected++
		}
	}

	fmt.Printf("quota         %d calls/day budgeted of the account's %d limit\n",
		e.cfg.DailyBudget, config.DailyCallLimit)
	projected := e.cfg.ProjectedDailyCalls(collected)
	spent, err := e.store.APICalls(ctx, time.Now().Format("2006-01-02"))
	if err != nil {
		return err
	}
	fmt.Printf("  · %d device(s) collected every %ds = %d calls/day (%d spent today)\n",
		collected, e.cfg.IntervalSeconds, projected, spent)
	if projected > e.cfg.DailyBudget {
		// Going over does not fail loudly: the API starts answering
		// "Unauthorized", which reads exactly like a bad token.
		fmt.Printf("  ✗ over budget — raise interval_seconds to at least %d, reduce the\n"+
			"    collect set, or raise api.daily_budget\n", e.cfg.MinIntervalSeconds(collected))
	} else {
		fmt.Printf("  ✓ within budget\n")
	}

	if e.cfg.HasCredentials() {
		client, err := e.client()
		if err != nil {
			return err
		}
		remote, err := client.Devices(ctx)
		if _, addErr := e.store.AddAPICalls(ctx, time.Now().Format("2006-01-02"), client.Calls()); addErr != nil {
			return addErr
		}
		fmt.Printf("api           %s\n", "https://api.switch-bot.com")
		if err != nil {
			fmt.Printf("  ✗ %v\n", err)
		} else {
			fmt.Printf("  ✓ reachable, %d device(s) on the account\n", len(remote))
		}
	}

	info, err := platform.DaemonStatus()
	if err != nil {
		fmt.Printf("daemon        %v\n", err)
		return nil
	}
	fmt.Printf("daemon        %s (%s)\n", info.Label, info.ConfigPath)
	if info.Loaded {
		fmt.Printf("  ✓ loaded\n")
	} else {
		fmt.Printf("  · not loaded — `sensor-lens install`\n")
	}
	return nil
}

// ---------------------------------------------------------------- install

func runInstall(args []string) error {
	fs := newFlagSet("install")
	force := fs.Bool("force", false, "install even if the schedule would exceed the daily budget")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := openEnv()
	if err != nil {
		return err
	}
	defer e.Close()
	ctx := signalContext()

	devices, err := e.store.Devices(ctx)
	if err != nil {
		return err
	}
	collected := 0
	for _, d := range devices {
		if d.Enabled {
			collected++
		}
	}
	// Refuse a schedule that would walk into the quota. Once over, every call
	// fails as "Unauthorized" and the daemon looks broken rather than throttled.
	if projected := e.cfg.ProjectedDailyCalls(collected); projected > e.cfg.DailyBudget && !*force {
		return fmt.Errorf(
			"this schedule would spend %d calls/day, over the %d budget:\n"+
				"  raise polling.interval_seconds to at least %d, collect fewer devices,\n"+
				"  or raise api.daily_budget (--force installs anyway)",
			projected, e.cfg.DailyBudget, e.cfg.MinIntervalSeconds(collected))
	}

	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate this binary: %w", err)
	}
	info, err := platform.InstallDaemon(bin)
	if err != nil {
		return err
	}
	fmt.Printf("installed %s\n  %s\n", info.Label, info.ConfigPath)
	return nil
}

func runUninstall(args []string) error {
	fs := newFlagSet("uninstall")
	if err := fs.Parse(args); err != nil {
		return err
	}
	info, err := platform.UninstallDaemon()
	if err != nil {
		return err
	}
	fmt.Printf("removed %s\n", info.Label)
	return nil
}

// ---------------------------------------------------------------- prune

func runPrune(args []string) error {
	fs := newFlagSet("prune")
	keepDays := fs.Int("keep-days", 0, "delete readings older than this many days (default: storage.retention_days)")
	device := fs.String("device", "", "delete every reading from this device instead")
	dryRun := fs.Bool("dry-run", false, "report what would go without deleting")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := openEnv()
	if err != nil {
		return err
	}
	defer e.Close()
	ctx := signalContext()

	if *device != "" {
		return pruneDevice(ctx, e, *device, *dryRun)
	}

	days := *keepDays
	if days == 0 {
		days = e.cfg.RetentionDays
	}
	if days <= 0 {
		return errors.New("nothing to do: retention is unlimited (pass --keep-days N)")
	}

	cutoff := time.Now().AddDate(0, 0, -days).Unix()
	if *dryRun {
		readings, err := e.store.Range(ctx, 0, cutoff-1)
		if err != nil {
			return err
		}
		fmt.Printf("would delete %d reading(s) older than %s\n",
			len(readings), time.Unix(cutoff, 0).Format("2006-01-02"))
		return nil
	}

	n, err := e.store.Prune(ctx, cutoff)
	if err != nil {
		return err
	}
	fmt.Printf("deleted %d reading(s) older than %s\n", n, time.Unix(cutoff, 0).Format("2006-01-02"))
	return nil
}

// pruneDevice drops one device's stored readings — for an appliance that was
// collected by mistake and whose rows are noise, not history.
func pruneDevice(ctx context.Context, e *env, ref string, dryRun bool) error {
	id, err := resolveDevice(ctx, e, ref)
	if err != nil {
		return err
	}
	n, err := e.store.CountDeviceReadings(ctx, id)
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Printf("would delete %d reading(s) from %s (%s)\n", n, ref, id)
		return nil
	}
	deleted, err := e.store.PruneDevice(ctx, id)
	if err != nil {
		return err
	}
	fmt.Printf("deleted %d reading(s) from %s (%s)\n", deleted, ref, id)
	return nil
}

// ---------------------------------------------------------------- helpers

// signalContext cancels on SIGINT/SIGTERM so the daemon shuts down cleanly and
// a long export can be interrupted.
func signalContext() context.Context {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	_ = stop // released when the process exits
	return ctx
}

func parseRange(since, until string) (time.Time, time.Time, error) {
	now := time.Now()
	from, err := ParseTime(since, now)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("--since: %w", err)
	}
	to, err := ParseTime(until, now)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("--until: %w", err)
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("--until (%s) must be after --since (%s)",
			to.Format(time.RFC3339), from.Format(time.RFC3339))
	}
	return from, to, nil
}

// resolveDevice turns an ID or a name into a device ID, refusing an ambiguous
// name rather than picking one.
func resolveDevice(ctx context.Context, e *env, ref string) (string, error) {
	devices, err := e.store.Devices(ctx)
	if err != nil {
		return "", err
	}
	var matches []store.Device
	for _, d := range devices {
		if strings.EqualFold(d.DeviceID, ref) {
			return d.DeviceID, nil
		}
		if strings.EqualFold(d.Name, ref) {
			matches = append(matches, d)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].DeviceID, nil
	case 0:
		return "", fmt.Errorf("no device called %q (see `sensor-lens devices`)", ref)
	default:
		ids := make([]string, len(matches))
		for i, d := range matches {
			ids[i] = d.DeviceID
		}
		return "", fmt.Errorf("%q matches several devices (%s): use the device ID",
			ref, strings.Join(ids, ", "))
	}
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
