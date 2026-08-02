// Package importer reads the CSV the SwitchBot app exports and turns it into
// readings.
//
// This is the only way to recover history: the Open API has no endpoint for
// past values, it answers with the current status and nothing else. The data
// does exist — the meters hold weeks of it and the app holds years — and the
// app's "Export Data" button is the door to it. Merging an export back in is
// what fills a window the daemon was not running for.
package importer

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nlink-jp/sensor-lens/core/store"
)

// timestampLayout is how the app writes times: "2026-08-01 00:00:48".
//
// There is no zone or offset in the file, so the values can only be read as
// local time — the same clock the app displayed them on. An export carried to
// a machine in another zone will land at the wrong instant, which is why
// Options.Location exists.
const timestampLayout = "2006-01-02 15:04:05"

// column describes one recognized CSV column.
type column struct {
	metric  string
	derived bool                  // computed by the app, never returned by the API
	convert func(float64) float64 // nil means take the value as-is
}

func fromFahrenheit(f float64) float64 { return (f - 32) * 5 / 9 }

// columns maps a header cell — normalized to the text before its unit
// parenthesis, lower-cased — onto the metric it carries.
//
// The Celsius and Fahrenheit variants both land on temperature_c: which one the
// app writes depends on a display setting, and a database that mixed the two
// would be silently wrong.
var columns = map[string]column{
	"temperature_celsius":    {metric: "temperature_c"},
	"temperature_fahrenheit": {metric: "temperature_c", convert: fromFahrenheit},
	"relative_humidity":      {metric: "humidity_pct"},
	"co2":                    {metric: "co2_ppm"},
	"absolute_humidity":      {metric: "absolute_humidity_gm3", derived: true},
	"dpt_celsius":            {metric: "dew_point_c", derived: true},
	"dpt_fahrenheit":         {metric: "dew_point_c", derived: true, convert: fromFahrenheit},
	"vpd":                    {metric: "vpd_kpa", derived: true},
}

// Options configures a parse.
type Options struct {
	// DeviceID attributes every row; the CSV itself carries no device identity.
	DeviceID string
	// Location interprets the zone-less timestamps. Nil means time.Local.
	Location *time.Location
	// IncludeDerived also imports the columns the app computes (absolute
	// humidity, dew point, VPD).
	//
	// Off by default on purpose: the API never reports them, so importing them
	// would produce series that exist only inside imported windows and stop
	// dead everywhere else — a chart that looks broken rather than one that
	// looks sparse.
	IncludeDerived bool
}

// RowError is one row that could not be read. Rows fail individually: a single
// truncated line at the end of a 90,000-row export must not throw away the
// other 89,999.
type RowError struct {
	Line int    `json:"line"`
	Err  string `json:"error"`
}

// Result is what a parse produced.
type Result struct {
	Readings []store.Reading `json:"-"`
	// Rows is how many data rows were read successfully.
	Rows int `json:"rows"`
	// Skipped is how many rows were dropped.
	Skipped int `json:"skipped"`
	// Metrics are the metric names found, in column order.
	Metrics []string `json:"metrics"`
	// Ignored are header cells that matched no known column.
	Ignored []string `json:"ignored,omitempty"`
	// Errors are the first few row failures, for a legible report.
	Errors []RowError `json:"errors,omitempty"`
	// First and Last bound the imported window.
	First int64 `json:"first"`
	Last  int64 `json:"last"`
}

// maxReportedErrors caps how many row failures are carried back; the count in
// Skipped is always complete.
const maxReportedErrors = 10

// Parse reads an exported CSV.
func Parse(r io.Reader, opt Options) (Result, error) {
	if opt.DeviceID == "" {
		return Result{}, errors.New("importer: no device id: the export does not identify the device it came from")
	}
	loc := opt.Location
	if loc == nil {
		loc = time.Local
	}

	cr := csv.NewReader(skipBOM(r))
	cr.FieldsPerRecord = -1 // rows are validated per-row, not by the reader
	cr.ReuseRecord = true

	header, err := cr.Read()
	if err != nil {
		if err == io.EOF {
			return Result{}, errors.New("importer: file is empty")
		}
		return Result{}, fmt.Errorf("importer: read header: %w", err)
	}

	tsIndex := -1
	type target struct {
		index int
		col   column
	}
	var targets []target
	res := Result{}

	for i, cell := range header {
		name := normalizeHeader(cell)
		if name == "timestamp" {
			tsIndex = i
			continue
		}
		col, ok := columns[name]
		if !ok {
			res.Ignored = append(res.Ignored, strings.TrimSpace(cell))
			continue
		}
		if col.derived && !opt.IncludeDerived {
			continue
		}
		targets = append(targets, target{index: i, col: col})
		res.Metrics = append(res.Metrics, col.metric)
	}

	if tsIndex < 0 {
		return res, fmt.Errorf("importer: no Timestamp column (header was %q)", strings.Join(header, ","))
	}
	if len(targets) == 0 {
		return res, errors.New("importer: no recognized measurement columns")
	}

	line := 1
	for {
		record, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			res.Skipped++
			res.addError(line, err.Error())
			// A parse error from the CSV reader itself may be unrecoverable;
			// io.EOF above is the only clean end, so keep going but do not spin.
			if errors.Is(err, csv.ErrFieldCount) {
				continue
			}
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				continue
			}
			return res, fmt.Errorf("importer: line %d: %w", line, err)
		}

		if tsIndex >= len(record) {
			res.Skipped++
			res.addError(line, "row is shorter than the header")
			continue
		}
		ts, err := time.ParseInLocation(timestampLayout, strings.TrimSpace(record[tsIndex]), loc)
		if err != nil {
			res.Skipped++
			res.addError(line, fmt.Sprintf("bad timestamp %q", record[tsIndex]))
			continue
		}
		unix := ts.Unix()

		got := false
		for _, t := range targets {
			if t.index >= len(record) {
				continue
			}
			raw := strings.TrimSpace(record[t.index])
			if raw == "" {
				continue // a sensor that reported nothing this minute
			}
			v, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				continue
			}
			if t.col.convert != nil {
				v = t.col.convert(v)
			}
			res.Readings = append(res.Readings, store.Reading{
				DeviceID: opt.DeviceID, Metric: t.col.metric, TS: unix, Value: v,
			})
			got = true
		}
		if !got {
			res.Skipped++
			res.addError(line, "no readable values")
			continue
		}

		res.Rows++
		if res.First == 0 || unix < res.First {
			res.First = unix
		}
		if unix > res.Last {
			res.Last = unix
		}
	}

	if res.Rows == 0 {
		return res, errors.New("importer: no usable rows")
	}
	return res, nil
}

func (r *Result) addError(line int, msg string) {
	if len(r.Errors) < maxReportedErrors {
		r.Errors = append(r.Errors, RowError{Line: line, Err: msg})
	}
}

// skipBOM drops a leading UTF-8 byte order mark.
//
// It has to happen before the CSV reader sees the bytes: a BOM sitting in front
// of the opening quote turns the first header cell into a "bare \" in
// non-quoted-field" parse error, which fails the whole file rather than one
// column.
func skipBOM(r io.Reader) io.Reader {
	br := bufio.NewReader(r)
	if b, err := br.Peek(3); err == nil && bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
		br.Discard(3)
	}
	return br
}

// normalizeHeader reduces a header cell to its comparable name: the text before
// the unit parenthesis, lower-cased, without the UTF-8 BOM the export may carry
// on its first cell.
func normalizeHeader(cell string) string {
	cell = strings.TrimPrefix(cell, "\ufeff")
	cell = strings.TrimSpace(cell)
	if i := strings.IndexByte(cell, '('); i >= 0 {
		cell = cell[:i]
	}
	return strings.ToLower(strings.TrimSpace(cell))
}

// DeviceNameFromFilename recovers the device name the app put in the export
// filename — "CO2センサー (Room 1)_data.csv" names the device
// "CO2センサー (Room 1)" — so an import can find its device without being told.
// It returns "" when the name does not follow that shape.
func DeviceNameFromFilename(path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	name, ok := strings.CutSuffix(base, "_data")
	if !ok {
		return ""
	}
	return strings.TrimSpace(name)
}
