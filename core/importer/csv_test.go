package importer

import (
	"strings"
	"testing"
	"time"
)

// The headers below are byte-for-byte what the SwitchBot app writes, taken from
// real exports (the rows are synthetic — the real ones say when someone is home).
const (
	co2Header   = `"Timestamp","Temperature_Celsius(°C)","Relative_Humidity(%)","CO2(ppm)","Absolute_Humidity(g/m³)","DPT_Celsius(°C)","VPD(kPa)"`
	meterHeader = `"Timestamp","Temperature_Celsius(°C)","Relative_Humidity(%)","Absolute_Humidity(g/m³)","DPT_Celsius(°C)","VPD(kPa)"`
)

func parse(t *testing.T, body string, opt Options) Result {
	t.Helper()
	if opt.DeviceID == "" {
		opt.DeviceID = "AAA"
	}
	if opt.Location == nil {
		opt.Location = time.UTC
	}
	res, err := Parse(strings.NewReader(body), opt)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	return res
}

func TestParseCO2Export(t *testing.T) {
	res := parse(t, co2Header+"\n"+
		`"2026-08-01 00:00:48","27.2","47","1148","12.2","14.9","1.91"`+"\n"+
		`"2026-08-01 00:01:48","27.3","48","1150","12.2","14.9","1.91"`+"\n", Options{})

	if res.Rows != 2 {
		t.Errorf("Rows = %d, want 2", res.Rows)
	}
	// Three metrics per row: the derived columns are left out by default.
	if len(res.Readings) != 6 {
		t.Fatalf("Readings = %d, want 6", len(res.Readings))
	}

	want := map[string]float64{"temperature_c": 27.2, "humidity_pct": 47, "co2_ppm": 1148}
	first := time.Date(2026, 8, 1, 0, 0, 48, 0, time.UTC).Unix()
	for _, r := range res.Readings[:3] {
		if v, ok := want[r.Metric]; !ok || v != r.Value {
			t.Errorf("reading %s = %v, want %v", r.Metric, r.Value, want[r.Metric])
		}
		if r.TS != first {
			t.Errorf("reading %s ts = %d, want %d", r.Metric, r.TS, first)
		}
		if r.DeviceID != "AAA" {
			t.Errorf("reading %s device = %q, want AAA", r.Metric, r.DeviceID)
		}
	}
	if res.First != first {
		t.Errorf("First = %d, want %d", res.First, first)
	}
}

func TestParseSkipsDerivedColumnsByDefault(t *testing.T) {
	res := parse(t, co2Header+"\n"+
		`"2026-08-01 00:00:48","27.2","47","1148","12.2","14.9","1.91"`+"\n", Options{})

	for _, r := range res.Readings {
		switch r.Metric {
		case "absolute_humidity_gm3", "dew_point_c", "vpd_kpa":
			// These exist only in exports; importing them by default would make
			// a chart that dies the moment the imported window ends.
			t.Errorf("derived metric %s imported without IncludeDerived", r.Metric)
		}
	}
}

func TestParseIncludeDerived(t *testing.T) {
	res := parse(t, co2Header+"\n"+
		`"2026-08-01 00:00:48","27.2","47","1148","12.2","14.9","1.91"`+"\n",
		Options{IncludeDerived: true})

	if len(res.Readings) != 6 {
		t.Fatalf("Readings = %d, want all 6 columns", len(res.Readings))
	}
	got := map[string]float64{}
	for _, r := range res.Readings {
		got[r.Metric] = r.Value
	}
	if got["dew_point_c"] != 14.9 || got["vpd_kpa"] != 1.91 || got["absolute_humidity_gm3"] != 12.2 {
		t.Errorf("derived metrics = %+v", got)
	}
}

func TestParseMeterExportHasNoCO2(t *testing.T) {
	res := parse(t, meterHeader+"\n"+
		`"2026-08-01 00:00:14","26.8","50","12.7","15.5","1.76"`+"\n", Options{})

	for _, r := range res.Readings {
		if r.Metric == "co2_ppm" {
			t.Error("a plain meter export produced a CO2 reading")
		}
	}
	if len(res.Readings) != 2 {
		t.Errorf("Readings = %d, want temperature and humidity", len(res.Readings))
	}
}

func TestParseFahrenheitIsConverted(t *testing.T) {
	// The app writes whichever unit it is displaying; a database mixing the two
	// would be silently, invisibly wrong.
	res := parse(t, `"Timestamp","Temperature_Fahrenheit(°F)","Relative_Humidity(%)"`+"\n"+
		`"2026-08-01 00:00:00","212","50"`+"\n", Options{})

	for _, r := range res.Readings {
		if r.Metric == "temperature_c" && r.Value != 100 {
			t.Errorf("212°F imported as %v, want 100 (°C)", r.Value)
		}
	}
}

func TestParseTimestampsUseTheGivenZone(t *testing.T) {
	// The file carries no zone, so the same text is a different instant
	// depending on where it is read.
	tokyo := time.FixedZone("JST", 9*3600)
	res := parse(t, meterHeader+"\n"+
		`"2026-08-01 09:00:00","26.8","50","12.7","15.5","1.76"`+"\n",
		Options{Location: tokyo})

	want := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Unix()
	if res.First != want {
		t.Errorf("First = %d, want %d (09:00 JST is 00:00 UTC)", res.First, want)
	}
}

func TestParseSurvivesBadRows(t *testing.T) {
	res := parse(t, meterHeader+"\n"+
		`"2026-08-01 00:00:14","26.8","50","12.7","15.5","1.76"`+"\n"+
		`"not a timestamp","26.8","50","12.7","15.5","1.76"`+"\n"+
		`"2026-08-01 00:02:14","","","","",""`+"\n"+
		`"2026-08-01 00:03:14","27.0","51","12.7","15.5","1.76"`+"\n", Options{})

	// One truncated line must not throw away the rest of a 90,000-row export.
	if res.Rows != 2 {
		t.Errorf("Rows = %d, want 2 good rows", res.Rows)
	}
	if res.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2", res.Skipped)
	}
	if len(res.Errors) != 2 {
		t.Errorf("Errors = %+v, want both failures reported", res.Errors)
	}
}

func TestParsePartialRowKeepsWhatItHas(t *testing.T) {
	// A meter that reported temperature but not humidity for one minute.
	res := parse(t, meterHeader+"\n"+
		`"2026-08-01 00:00:14","26.8","","12.7","15.5","1.76"`+"\n", Options{})

	if len(res.Readings) != 1 || res.Readings[0].Metric != "temperature_c" {
		t.Errorf("Readings = %+v, want just the temperature", res.Readings)
	}
	if res.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0: the row had a usable value", res.Skipped)
	}
}

func TestParseReportsUnknownColumns(t *testing.T) {
	res := parse(t, `"Timestamp","Temperature_Celsius(°C)","Something_New(x)"`+"\n"+
		`"2026-08-01 00:00:00","26.8","1"`+"\n", Options{})

	if len(res.Ignored) != 1 || res.Ignored[0] != "Something_New(x)" {
		t.Errorf("Ignored = %v, want the unknown column named", res.Ignored)
	}
}

func TestParseStripsBOM(t *testing.T) {
	res := parse(t, "\ufeff"+meterHeader+"\n"+
		`"2026-08-01 00:00:14","26.8","50","12.7","15.5","1.76"`+"\n", Options{})

	if res.Rows != 1 {
		t.Errorf("Rows = %d; a leading BOM must not hide the Timestamp column", res.Rows)
	}
}

func TestParseRejectsUnusableInput(t *testing.T) {
	tests := map[string]struct {
		body string
		opt  Options
	}{
		"empty file":        {"", Options{DeviceID: "AAA"}},
		"no timestamp":      {`"Temperature_Celsius(°C)"` + "\n" + `"26.8"` + "\n", Options{DeviceID: "AAA"}},
		"no known columns":  {`"Timestamp","Mood"` + "\n" + `"2026-08-01 00:00:00","good"` + "\n", Options{DeviceID: "AAA"}},
		"no usable rows":    {meterHeader + "\n" + `"bad","1","2","3","4","5"` + "\n", Options{DeviceID: "AAA"}},
		"missing device id": {meterHeader + "\n", Options{}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(tc.body), tc.opt); err == nil {
				t.Error("Parse() succeeded, want error")
			}
		})
	}
}

func TestDeviceNameFromFilename(t *testing.T) {
	tests := map[string]string{
		"CO2センサー (Room 1)_data.csv":    "CO2センサー (Room 1)",
		"/tmp/exports/防水温湿度計_data.csv": "防水温湿度計",
		"デイリーステーション_data.csv":          "デイリーステーション",
		"readings.csv": "",
		"":             "",
	}

	for path, want := range tests {
		if got := DeviceNameFromFilename(path); got != want {
			t.Errorf("DeviceNameFromFilename(%q) = %q, want %q", path, got, want)
		}
	}
}
