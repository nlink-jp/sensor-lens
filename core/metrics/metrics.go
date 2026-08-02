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

// ambient is the set of metrics that mean "this device measures the room",
// which is what makes it worth polling on a schedule.
//
// Only temperature and CO2 qualify, and the omissions are the interesting part.
// Humidity is not enough on its own: a humidifier reports a humidity field too
// (0 when it is not sensing) alongside mode and childLock, and it is an
// appliance describing itself, not a sensor describing the room. Illuminance
// and battery are likewise things a device can report without measuring the
// ambient conditions anyone wants charted.
//
// Anything excluded here can still be collected — name it in the config's
// collect set and it is polled without argument. This governs only what is
// picked up automatically.
var ambient = map[string]bool{
	"temperature_c": true,
	"co2_ppm":       true,
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

// IsAmbient reports whether a metric measures the room rather than the device.
func IsAmbient(metric string) bool { return ambient[metric] }

// HasAmbient reports whether any reading measures the room, i.e. whether this
// device is a sensor worth collecting without being asked.
func HasAmbient(readings []Reading) bool {
	for _, r := range readings {
		if IsAmbient(r.Metric) {
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
