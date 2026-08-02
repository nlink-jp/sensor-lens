package cmd

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/sensor-lens/core/aggregate"
	"github.com/nlink-jp/sensor-lens/core/store"
)

func TestFormatValue(t *testing.T) {
	tests := []struct {
		metric string
		value  float64
		want   string
	}{
		{"temperature_c", 27.24, "27.2°C"},
		{"humidity_pct", 47, "47%"},
		{"co2_ppm", 1148, "1148 ppm"},
		{"battery_pct", 100, "100%"},
		{"vpd_kpa", 1.914, "1.91 kPa"},
		// An unrecognized metric still has to print something sensible: the
		// extractor deliberately passes unknown API fields through.
		{"brightness", 42, "42"},
	}

	for _, tc := range tests {
		if got := FormatValue(tc.metric, tc.value); got != tc.want {
			t.Errorf("FormatValue(%s, %v) = %q, want %q", tc.metric, tc.value, got, tc.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := map[time.Duration]string{
		45 * time.Second:  "45s",
		5 * time.Minute:   "5m",
		90 * time.Minute:  "1h30m",
		50 * time.Hour:    "2d2h",
		-30 * time.Second: "30s",
	}

	for d, want := range tests {
		if got := FormatDuration(d); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestSortMetrics(t *testing.T) {
	got := SortMetrics([]string{"battery_pct", "vpd_kpa", "co2_ppm", "temperature_c", "humidity_pct"})

	want := []string{"temperature_c", "humidity_pct", "co2_ppm", "battery_pct", "vpd_kpa"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SortMetrics() = %v\nwant %v", got, want)
	}
}

func TestGroupReadings(t *testing.T) {
	devices := []store.Device{
		{DeviceID: "AAA", Name: "Room 1", DeviceType: "MeterPro(CO2)"},
		{DeviceID: "BBB", Name: "Bedroom", DeviceType: "Meter"},
	}
	readings := []store.Reading{
		{DeviceID: "AAA", Metric: "temperature_c", TS: 1000, Value: 27.2},
		{DeviceID: "AAA", Metric: "co2_ppm", TS: 1000, Value: 1148},
		{DeviceID: "BBB", Metric: "temperature_c", TS: 100, Value: 21},
	}

	got := GroupReadings(readings, devices, 1100, 300*time.Second)

	if len(got) != 2 {
		t.Fatalf("GroupReadings() = %d entries, want 2", len(got))
	}
	// Sorted by name: Bedroom before Room 1.
	if got[0].Name != "Bedroom" || got[1].Name != "Room 1" {
		t.Errorf("order = %s, %s", got[0].Name, got[1].Name)
	}
	if len(got[1].Metrics) != 2 || got[1].Metrics["co2_ppm"] != 1148 {
		t.Errorf("Room 1 metrics = %+v", got[1].Metrics)
	}
	// Bedroom last reported 1000 s ago at a 300 s cadence — it has gone quiet.
	if !got[0].Stale {
		t.Error("Bedroom is not marked stale despite reporting 1000s ago")
	}
	if got[1].Stale {
		t.Error("Room 1 marked stale despite a fresh reading")
	}
}

func TestGroupReadingsFallsBackToDeviceID(t *testing.T) {
	// A reading whose device is not in the devices table (imported before the
	// list was refreshed) must still be displayable.
	got := GroupReadings(
		[]store.Reading{{DeviceID: "UNKNOWN", Metric: "temperature_c", TS: 1000, Value: 20}},
		nil, 1000, 300*time.Second)

	if len(got) != 1 || got[0].Name != "UNKNOWN" {
		t.Errorf("GroupReadings() = %+v, want the device ID used as the name", got)
	}
}

func TestFormatNow(t *testing.T) {
	out := FormatNow([]DeviceReading{{
		Name:    "Room 1",
		Metrics: map[string]float64{"temperature_c": 27.2, "humidity_pct": 47, "co2_ppm": 1148},
		TS:      1000,
	}})

	// Reading order: how it feels, then the air.
	if i, j := strings.Index(out, "27.2°C"), strings.Index(out, "1148 ppm"); i < 0 || j < 0 || i > j {
		t.Errorf("FormatNow() = %q, want temperature before CO2", out)
	}
	if strings.Contains(out, "stale") {
		t.Errorf("FormatNow() marked a fresh reading stale: %q", out)
	}
}

func TestFormatNowMarksStale(t *testing.T) {
	out := FormatNow([]DeviceReading{{
		Name: "Shed", Metrics: map[string]float64{"temperature_c": 4}, TS: 1000, Stale: true,
	}})

	if !strings.Contains(out, "stale") {
		t.Errorf("FormatNow() = %q, want the stale reading called out", out)
	}
}

func TestFormatNowEmpty(t *testing.T) {
	if out := FormatNow(nil); !strings.Contains(out, "no readings") {
		t.Errorf("FormatNow(nil) = %q, want an explanation", out)
	}
}

func TestFormatGapsExplainsTheFix(t *testing.T) {
	out := FormatGaps([]aggregate.Gap{
		{DeviceID: "AAA", Metric: "co2_ppm", Start: 1000, End: 5000, Missing: 12},
	}, []store.Device{{DeviceID: "AAA", Name: "Room 1"}})

	if !strings.Contains(out, "Room 1") {
		t.Errorf("FormatGaps() = %q, want the device named", out)
	}
	// A gap report is only useful if it says what to do about it — the API
	// cannot backfill, only an app export can.
	if !strings.Contains(out, "import") || !strings.Contains(out, "Export Data") {
		t.Errorf("FormatGaps() = %q, want it to point at the export/import route", out)
	}
}

func TestFormatGapsWhenClean(t *testing.T) {
	if out := FormatGaps(nil, nil); !strings.Contains(out, "no gaps") {
		t.Errorf("FormatGaps(nil) = %q", out)
	}
}

func TestFormatSummaries(t *testing.T) {
	out := FormatSummaries([]aggregate.Summary{
		{DeviceID: "AAA", Metric: "co2_ppm", Count: 288, Min: 500, Avg: 800, Max: 1600},
	}, []store.Device{{DeviceID: "AAA", Name: "Room 1"}})

	for _, want := range []string{"Room 1", "co2_ppm", "288", "500 ppm", "1600 ppm"} {
		if !strings.Contains(out, want) {
			t.Errorf("FormatSummaries() = %q, missing %q", out, want)
		}
	}
}

func TestFormatDevices(t *testing.T) {
	out := FormatDevices([]store.Device{
		{DeviceID: "AAA", Name: "Room 1", DeviceType: "MeterPro(CO2)", Enabled: true},
		{DeviceID: "PLUG", Name: "Desk", DeviceType: "Plug Mini (JP)", Enabled: false},
	})

	if !strings.Contains(out, "MeterPro(CO2)") || !strings.Contains(out, "Plug Mini (JP)") {
		t.Errorf("FormatDevices() = %q", out)
	}
	// Whether a device is collected is the thing the user came to check.
	if !strings.Contains(out, "COLLECTED") {
		t.Errorf("FormatDevices() = %q, want the collected column", out)
	}
}

func TestParseTime(t *testing.T) {
	now := time.Date(2026, 8, 2, 15, 30, 0, 0, time.UTC)

	tests := []struct {
		in   string
		want time.Time
	}{
		{"now", now},
		{"-3h", now.Add(-3 * time.Hour)},
		{"-7d", now.AddDate(0, 0, -7)},
		{"-90m", now.Add(-90 * time.Minute)},
		{"2026-08-01", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
		{"2026-08-01 09:15", time.Date(2026, 8, 1, 9, 15, 0, 0, time.UTC)},
		{"2026-08-01 09:15:30", time.Date(2026, 8, 1, 9, 15, 30, 0, time.UTC)},
		{"2026-08-01T09:15:30", time.Date(2026, 8, 1, 9, 15, 30, 0, time.UTC)},
	}

	for _, tc := range tests {
		got, err := ParseTime(tc.in, now)
		if err != nil {
			t.Errorf("ParseTime(%q) error = %v", tc.in, err)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("ParseTime(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseTimeRejectsNonsense(t *testing.T) {
	now := time.Now()
	for _, in := range []string{"", "yesterday", "2026-13-45", "-3 hours"} {
		if _, err := ParseTime(in, now); err == nil {
			t.Errorf("ParseTime(%q) succeeded, want an error", in)
		}
	}
}

func TestParseRangeRejectsBackwardsRange(t *testing.T) {
	if _, _, err := parseRange("now", "-3h"); err == nil {
		t.Error("parseRange(now, -3h) succeeded; --until must be after --since")
	}
}

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" AAA , BBB ,, ")
	if !reflect.DeepEqual(got, []string{"AAA", "BBB"}) {
		t.Errorf("splitCSV() = %v", got)
	}
	if got := splitCSV(""); got != nil {
		t.Errorf("splitCSV(\"\") = %v, want nil", got)
	}
}

func TestFilterReadings(t *testing.T) {
	in := []DeviceReading{
		{DeviceID: "AAA", Name: "Room 1"},
		{DeviceID: "BBB", Name: "Bedroom"},
	}

	// The display set is chosen independently of what is collected, so
	// filtering by name is how a menu bar picks its two chips out of eight.
	got := filterReadings(in, []string{"bedroom"})
	if len(got) != 1 || got[0].DeviceID != "BBB" {
		t.Errorf("filterReadings(by name) = %+v", got)
	}

	got = filterReadings(in, []string{"AAA"})
	if len(got) != 1 || got[0].DeviceID != "AAA" {
		t.Errorf("filterReadings(by id) = %+v", got)
	}
}
