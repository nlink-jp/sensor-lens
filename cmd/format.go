package cmd

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nlink-jp/sensor-lens/core/aggregate"
	"github.com/nlink-jp/sensor-lens/core/store"
)

// Everything in this file is a pure function: given values in, text out. The
// commands do the I/O; the shaping lives here so it can be tested without a
// database, a network or a clock.

// units renders a metric's value the way that metric is read.
//
// A prefix is carried wherever the number alone would be ambiguous on a line
// with others: two bare percentages side by side do not say which is humidity
// and which is charge, and an illuminance level of "1" says nothing at all.
var units = map[string]struct {
	prefix    string
	suffix    string
	precision int
}{
	"temperature_c":         {"", "°C", 1},
	"humidity_pct":          {"", "%", 0},
	"co2_ppm":               {"", " ppm", 0},
	"battery_pct":           {"bat ", "%", 0},
	"light_level":           {"light ", "", 0},
	"move_detected":         {"motion ", "", 0},
	"dew_point_c":           {"dew ", "°C", 1},
	"vpd_kpa":               {"vpd ", " kPa", 2},
	"absolute_humidity_gm3": {"abs ", " g/m³", 1},
}

// FormatValue renders one reading's value with its unit.
//
// An unrecognized metric is printed as name=value: extraction deliberately
// passes unknown API fields through, so this path is reached whenever a device
// reports something new, and a bare number would be unreadable.
func FormatValue(metric string, v float64) string {
	u, ok := units[metric]
	if !ok {
		return metric + "=" + strconv.FormatFloat(v, 'f', -1, 64)
	}
	return u.prefix + strconv.FormatFloat(v, 'f', u.precision, 64) + u.suffix
}

// FormatDuration renders a span the way a person would say it.
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// DeviceReading is one device's current state, as `now` and `status` present it.
type DeviceReading struct {
	DeviceID   string             `json:"device_id"`
	Name       string             `json:"name"`
	DeviceType string             `json:"device_type"`
	Metrics    map[string]float64 `json:"metrics"`
	TS         int64              `json:"ts"`
	Stale      bool               `json:"stale"`
}

// GroupReadings folds a flat reading list into one entry per device, newest
// timestamp per device, marking the ones that have gone quiet.
func GroupReadings(readings []store.Reading, devices []store.Device, now int64, interval time.Duration) []DeviceReading {
	meta := make(map[string]store.Device, len(devices))
	for _, d := range devices {
		meta[d.DeviceID] = d
	}

	index := map[string]*DeviceReading{}
	for _, r := range readings {
		dr, ok := index[r.DeviceID]
		if !ok {
			d := meta[r.DeviceID]
			name := d.Name
			if name == "" {
				name = r.DeviceID
			}
			dr = &DeviceReading{
				DeviceID: r.DeviceID, Name: name, DeviceType: d.DeviceType,
				Metrics: map[string]float64{},
			}
			index[r.DeviceID] = dr
		}
		dr.Metrics[r.Metric] = r.Value
		if r.TS > dr.TS {
			dr.TS = r.TS
		}
	}

	out := make([]DeviceReading, 0, len(index))
	for _, dr := range index {
		dr.Stale = aggregate.IsStale(dr.TS, now, interval, 0)
		out = append(out, *dr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FilterCollected keeps only readings from devices currently being collected.
//
// A device dropped from the collect set keeps its stored history — that is
// history, and deleting it silently would be wrong — but it has no "now", and
// leaving its last values on screen would show a room that stopped being
// measured as though it still were. A reading whose device is not in the table
// at all is kept: it is not evidence of anything having been turned off.
func FilterCollected(readings []store.Reading, devices []store.Device) []store.Reading {
	known := make(map[string]bool, len(devices))
	for _, d := range devices {
		known[d.DeviceID] = d.Enabled
	}

	out := make([]store.Reading, 0, len(readings))
	for _, r := range readings {
		if enabled, ok := known[r.DeviceID]; !ok || enabled {
			out = append(out, r)
		}
	}
	return out
}

// preferredOrder is how a person reads a room: what it feels like, then the
// air, then the housekeeping.
var preferredOrder = []string{"temperature_c", "humidity_pct", "co2_ppm", "light_level", "battery_pct"}

// SortMetrics orders metric names for display: the well-known ones in reading
// order, then anything else alphabetically.
func SortMetrics(metrics []string) []string {
	rank := make(map[string]int, len(preferredOrder))
	for i, m := range preferredOrder {
		rank[m] = i
	}
	out := append([]string(nil), metrics...)
	sort.Slice(out, func(i, j int) bool {
		ri, oki := rank[out[i]]
		rj, okj := rank[out[j]]
		switch {
		case oki && okj:
			return ri < rj
		case oki:
			return true
		case okj:
			return false
		default:
			return out[i] < out[j]
		}
	})
	return out
}

// FormatNow renders the current readings as a table.
func FormatNow(readings []DeviceReading) string {
	if len(readings) == 0 {
		return "no readings yet — run `sensor-lens now` with credentials configured, or start the daemon\n"
	}

	var b strings.Builder
	for _, dr := range readings {
		names := make([]string, 0, len(dr.Metrics))
		for m := range dr.Metrics {
			names = append(names, m)
		}
		parts := make([]string, 0, len(names))
		for _, m := range SortMetrics(names) {
			parts = append(parts, FormatValue(m, dr.Metrics[m]))
		}

		fmt.Fprintf(&b, "%-24s %s", dr.Name, strings.Join(parts, "  "))
		if dr.Stale {
			fmt.Fprintf(&b, "   (stale, last seen %s)", time.Unix(dr.TS, 0).Format("2006-01-02 15:04"))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// FormatDevices renders the device list.
func FormatDevices(devices []store.Device) string {
	if len(devices) == 0 {
		return "no devices known yet — run `sensor-lens devices --refresh`\n"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-24s %-18s %-18s %s\n", "NAME", "TYPE", "DEVICE ID", "COLLECTED")
	for _, d := range devices {
		collected := "no"
		if d.Enabled {
			collected = "yes"
		}
		fmt.Fprintf(&b, "%-24s %-18s %-18s %s\n", d.Name, d.DeviceType, d.DeviceID, collected)
	}
	return b.String()
}

// FormatSummaries renders a report.
func FormatSummaries(summaries []aggregate.Summary, devices []store.Device) string {
	if len(summaries) == 0 {
		return "no readings in that range\n"
	}
	names := make(map[string]string, len(devices))
	for _, d := range devices {
		names[d.DeviceID] = d.Name
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-24s %-16s %8s %10s %10s %10s\n", "DEVICE", "METRIC", "SAMPLES", "MIN", "AVG", "MAX")
	for _, s := range summaries {
		name := names[s.DeviceID]
		if name == "" {
			name = s.DeviceID
		}
		fmt.Fprintf(&b, "%-24s %-16s %8d %10s %10s %10s\n", name, s.Metric, s.Count,
			FormatValue(s.Metric, s.Min), FormatValue(s.Metric, s.Avg), FormatValue(s.Metric, s.Max))
	}
	return b.String()
}

// FormatGaps renders the gap report, which doubles as the instruction for what
// to export from the app and import back.
func FormatGaps(gaps []aggregate.Gap, devices []store.Device) string {
	if len(gaps) == 0 {
		return "no gaps — every device reported throughout the range\n"
	}
	names := make(map[string]string, len(devices))
	for _, d := range devices {
		names[d.DeviceID] = d.Name
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-24s %-16s %-17s %-17s %9s %8s\n", "DEVICE", "METRIC", "FROM", "TO", "DURATION", "MISSING")
	for _, g := range gaps {
		name := names[g.DeviceID]
		if name == "" {
			name = g.DeviceID
		}
		fmt.Fprintf(&b, "%-24s %-16s %-17s %-17s %9s %8d\n", name, g.Metric,
			time.Unix(g.Start, 0).Format("2006-01-02 15:04"),
			time.Unix(g.End, 0).Format("2006-01-02 15:04"),
			FormatDuration(g.Duration()), g.Missing)
	}
	b.WriteString("\nTo fill these in: export the window from the SwitchBot app\n" +
		"(device → Export Data), then `sensor-lens import --file <csv>`.\n")
	return b.String()
}

// ParseTime resolves a --since / --until value against now.
//
// Relative forms are negative offsets ("-3h", "-7d") because that is how these
// flags are always used: how far back to look.
func ParseTime(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if s == "now" {
		return now, nil
	}

	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
		if d, err := parseOffset(s); err == nil {
			return now.Add(d), nil
		}
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot read %q as a time: use 2026-08-01, 2026-08-01 15:04, or an offset like -3h / -7d", s)
}

// parseOffset extends time.ParseDuration with a day unit, which is the unit
// people actually reach for when looking back over sensor history.
func parseOffset(s string) (time.Duration, error) {
	if rest, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.ParseFloat(rest, 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(n * 24 * float64(time.Hour)), nil
	}
	return time.ParseDuration(s)
}
