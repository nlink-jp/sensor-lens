package metrics

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

// decode mirrors what the client hands us: a status body unmarshalled into a
// map, so numbers arrive as float64 exactly as they will in production.
func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

func TestExtractPerDeviceType(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []Reading
	}{
		{
			// The only device type that reports CO2.
			name: "MeterPro(CO2)",
			body: `{"deviceId":"AAA","deviceType":"MeterPro(CO2)","hubDeviceId":"HUB1",
			        "battery":100,"version":"V4.2","temperature":22.5,"humidity":31,"CO2":1203}`,
			want: []Reading{
				{"battery_pct", 100},
				{"co2_ppm", 1203},
				{"humidity_pct", 31},
				{"temperature_c", 22.5},
			},
		},
		{
			name: "Meter",
			body: `{"deviceId":"BBB","deviceType":"Meter","hubDeviceId":"HUB1",
			        "temperature":26.1,"version":"V2.9","battery":60,"humidity":52}`,
			want: []Reading{
				{"battery_pct", 60},
				{"humidity_pct", 52},
				{"temperature_c", 26.1},
			},
		},
		{
			// Hub 2 has no CO2 and no battery, but does report illuminance.
			name: "Hub 2",
			body: `{"deviceId":"HUB1","deviceType":"Hub 2","hubDeviceId":"HUB1",
			        "temperature":13,"lightLevel":19,"version":"V4.2","humidity":18}`,
			want: []Reading{
				{"humidity_pct", 18},
				{"light_level", 19},
				{"temperature_c", 13},
			},
		},
		{
			name: "WoIOSensor",
			body: `{"deviceId":"CCC","deviceType":"WoIOSensor","hubDeviceId":"HUB1",
			        "temperature":8.4,"humidity":77,"battery":95,"version":"V1.3"}`,
			want: []Reading{
				{"battery_pct", 95},
				{"humidity_pct", 77},
				{"temperature_c", 8.4},
			},
		},
		{
			// Hub 3 adds a boolean and a string status field.
			name: "Hub 3",
			body: `{"deviceId":"HUB3","deviceType":"Hub 3","hubDeviceId":"HUB3",
			        "temperature":30.3,"humidity":45,"lightLevel":10,
			        "moveDetected":true,"onlineStatus":"online","version":"V1.0"}`,
			want: []Reading{
				{"humidity_pct", 45},
				{"light_level", 10},
				{"move_detected", 1},
				{"temperature_c", 30.3},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Extract(decode(t, tc.body))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Extract() = %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// A field nobody has seen before must still be collected — that is the whole
// point of not switching on deviceType.
func TestExtractKeepsUnknownNumericFields(t *testing.T) {
	got := Extract(decode(t, `{"deviceType":"FutureMeter","temperature":21,"pm25":13,"vocIndex":4.5}`))

	want := []Reading{
		{"pm25", 13},
		{"temperature_c", 21},
		{"vocIndex", 4.5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Extract() = %+v\nwant %+v", got, want)
	}
}

func TestExtractSkipsNonScalars(t *testing.T) {
	got := Extract(decode(t, `{"temperature":21,"nested":{"a":1},"list":[1,2],"name":"x","nothing":null}`))

	want := []Reading{{"temperature_c", 21}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Extract() = %+v, want %+v", got, want)
	}
}

func TestExtractRejectsNonFiniteValues(t *testing.T) {
	// NaN/Inf cannot come from JSON, but can from a hand-built map (CSV import).
	got := Extract(map[string]any{
		"temperature": math.NaN(),
		"humidity":    math.Inf(1),
		"CO2":         800,
	})

	want := []Reading{{"co2_ppm", 800}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Extract() = %+v, want %+v", got, want)
	}
}

func TestExtractEmptyBody(t *testing.T) {
	if got := Extract(map[string]any{}); len(got) != 0 {
		t.Errorf("Extract() = %+v, want empty", got)
	}
}

func TestHasEnvironmental(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"co2 meter", `{"temperature":22,"CO2":800}`, true},
		{"hub with ambient sensors", `{"temperature":13,"lightLevel":19}`, true},
		// A device that only reports its own charge has nothing to chart, so it
		// must not be pulled into the polling schedule and spend quota.
		{"battery only", `{"battery":80}`, false},
		{"no scalars", `{"deviceType":"Bot","power":"on"}`, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasEnvironmental(Extract(decode(t, tc.body))); got != tc.want {
				t.Errorf("HasEnvironmental() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestName(t *testing.T) {
	if got := Name("CO2"); got != "co2_ppm" {
		t.Errorf("Name(CO2) = %q, want co2_ppm", got)
	}
	if got := Name("unheardOf"); got != "unheardOf" {
		t.Errorf("Name(unheardOf) = %q, want it passed through verbatim", got)
	}
}
