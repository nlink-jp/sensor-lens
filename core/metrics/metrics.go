// Package metrics turns a SwitchBot status body into readings.
//
// The extraction is deliberately shape-driven rather than model-driven: every
// numeric (or boolean) scalar in the body becomes a reading, and only the
// naming is table-based. Nothing here switches on deviceType, so a new meter
// model — or a firmware update that adds a field — is collected without a code
// change. `sensor-lens devices --raw` shows the untouched body when you need to
// know what the API really sent.
package metrics

import (
	"encoding/json"
	"math"
	"sort"
)

// Reading is one numeric sample of one metric.
type Reading struct {
	Metric string
	Value  float64
}

// canonical maps the API's field names onto stable metric names carrying their
// unit. Fields absent from this table keep their API name verbatim: guessing a
// unit for a field we have never seen would be worse than passing it through.
var canonical = map[string]string{
	"temperature":  "temperature_c",
	"humidity":     "humidity_pct",
	"CO2":          "co2_ppm",
	"battery":      "battery_pct",
	"lightLevel":   "light_level",
	"moveDetected": "move_detected",
}

// environmental is the subset that makes a device worth polling on a schedule.
// Battery alone does not: sensor-lens is not a battery monitor, and a device
// that only reports its own charge has nothing to chart.
var environmental = map[string]bool{
	"temperature_c": true,
	"humidity_pct":  true,
	"co2_ppm":       true,
	"light_level":   true,
}

// Name returns the metric name stored for an API field.
func Name(field string) string {
	if n, ok := canonical[field]; ok {
		return n
	}
	return field
}

// Extract pulls every scalar reading out of a status body, sorted by metric
// name so callers and tests see a stable order.
//
// Booleans become 0 or 1 — Hub 3's moveDetected is a real time series worth
// keeping. Strings (deviceId, deviceType, version, onlineStatus) and nested
// objects are skipped: they describe the device, not a measurement, and belong
// in the devices table.
func Extract(body map[string]any) []Reading {
	out := make([]Reading, 0, len(body))
	for field, raw := range body {
		v, ok := toFloat(raw)
		if !ok {
			continue
		}
		out = append(out, Reading{Metric: Name(field), Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metric < out[j].Metric })
	return out
}

// IsEnvironmental reports whether a metric is one of the ambient measurements
// that justify polling a device.
func IsEnvironmental(metric string) bool { return environmental[metric] }

// HasEnvironmental reports whether any reading is an ambient measurement.
func HasEnvironmental(readings []Reading) bool {
	for _, r := range readings {
		if IsEnvironmental(r.Metric) {
			return true
		}
	}
	return false
}

// toFloat converts a JSON scalar to a float64, reporting whether it is one.
// NaN and ±Inf are rejected: they cannot round-trip through SQLite's REAL
// comparisons and would poison min/max aggregates.
func toFloat(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, isFinite(v)
	case float32:
		return float64(v), isFinite(float64(v))
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil && isFinite(f)
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func isFinite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
