package aggregate

import (
	"reflect"
	"testing"
	"time"

	"github.com/nlink-jp/sensor-lens/core/store"
)

func r(device, metric string, ts int64, v float64) store.Reading {
	return store.Reading{DeviceID: device, Metric: metric, TS: ts, Value: v}
}

func TestSummarize(t *testing.T) {
	// Deliberately out of order: the store returns sorted rows, but a caller
	// stitching several queries together may not.
	got := Summarize([]store.Reading{
		r("AAA", "co2_ppm", 3000, 900),
		r("AAA", "co2_ppm", 1000, 600),
		r("AAA", "co2_ppm", 2000, 750),
		r("BBB", "temperature_c", 1000, 21),
	})

	want := []Summary{
		{DeviceID: "AAA", Metric: "co2_ppm", Count: 3, Min: 600, Max: 900, Avg: 750,
			First: 600, Last: 900, FirstTS: 1000, LastTS: 3000},
		{DeviceID: "BBB", Metric: "temperature_c", Count: 1, Min: 21, Max: 21, Avg: 21,
			First: 21, Last: 21, FirstTS: 1000, LastTS: 1000},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Summarize() = %+v\nwant %+v", got, want)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	if got := Summarize(nil); len(got) != 0 {
		t.Errorf("Summarize(nil) = %+v, want empty", got)
	}
}

func TestGapsBetweenReadings(t *testing.T) {
	// 300 s interval; the 1000 -> 3000 jump is over 2.5 intervals.
	got := Gaps([]store.Reading{
		r("AAA", "co2_ppm", 1000, 600),
		r("AAA", "co2_ppm", 1300, 610),
		r("AAA", "co2_ppm", 3000, 700),
		r("AAA", "co2_ppm", 3300, 710),
	}, GapOptions{Expected: 300 * time.Second})

	if len(got) != 1 {
		t.Fatalf("Gaps() = %+v, want exactly 1", got)
	}
	g := got[0]
	if g.Start != 1300 || g.End != 3000 {
		t.Errorf("gap = %d..%d, want 1300..3000", g.Start, g.End)
	}
	// (3000-1300)/300 - 1 = 4 samples that never landed.
	if g.Missing != 4 {
		t.Errorf("Missing = %d, want 4", g.Missing)
	}
	if g.Duration() != 1700*time.Second {
		t.Errorf("Duration() = %v, want 1700s", g.Duration())
	}
}

func TestGapsIgnoreJitter(t *testing.T) {
	// A single late sample (2 intervals) is normal scheduling jitter, not an
	// outage — reporting it would drown the real gaps.
	got := Gaps([]store.Reading{
		r("AAA", "co2_ppm", 1000, 600),
		r("AAA", "co2_ppm", 1600, 610),
	}, GapOptions{Expected: 300 * time.Second})

	if len(got) != 0 {
		t.Errorf("Gaps() = %+v, want none for a 2-interval delay", got)
	}
}

func TestGapsAtWindowEdges(t *testing.T) {
	// The daemon was stopped before the window opened and never came back:
	// both edges are exactly what an outage looks like.
	got := Gaps([]store.Reading{
		r("AAA", "co2_ppm", 5000, 600),
		r("AAA", "co2_ppm", 5300, 610),
	}, GapOptions{Expected: 300 * time.Second, Since: 1000, Until: 9000})

	if len(got) != 2 {
		t.Fatalf("Gaps() = %+v, want a leading and a trailing gap", got)
	}
	if got[0].Start != 1000 || got[0].End != 5000 {
		t.Errorf("leading gap = %d..%d, want 1000..5000", got[0].Start, got[0].End)
	}
	if got[1].Start != 5300 || got[1].End != 9000 {
		t.Errorf("trailing gap = %d..%d, want 5300..9000", got[1].Start, got[1].End)
	}
}

func TestGapsPerSeries(t *testing.T) {
	// One device's meter dropped off while the other kept reporting; the gap
	// must be attributed to the series that actually lost data.
	got := Gaps([]store.Reading{
		r("AAA", "co2_ppm", 1000, 600),
		r("AAA", "co2_ppm", 5000, 700),
		r("BBB", "temperature_c", 1000, 21),
		r("BBB", "temperature_c", 1300, 21),
		r("BBB", "temperature_c", 1600, 21),
	}, GapOptions{Expected: 300 * time.Second})

	if len(got) != 1 {
		t.Fatalf("Gaps() = %+v, want 1", got)
	}
	if got[0].DeviceID != "AAA" || got[0].Metric != "co2_ppm" {
		t.Errorf("gap attributed to %s/%s, want AAA/co2_ppm", got[0].DeviceID, got[0].Metric)
	}
}

func TestGapsNoExpectedInterval(t *testing.T) {
	if got := Gaps([]store.Reading{r("AAA", "co2_ppm", 1000, 600)}, GapOptions{}); got != nil {
		t.Errorf("Gaps() without an expected interval = %+v, want nil", got)
	}
}

func TestIsStale(t *testing.T) {
	const interval = 300 * time.Second

	tests := []struct {
		name   string
		lastTS int64
		now    int64
		want   bool
	}{
		{"just polled", 10000, 10010, false},
		{"one interval late", 10000, 10300, false},
		{"three intervals late", 10000, 10900, true},
		{"never polled", 0, 10000, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsStale(tc.lastTS, tc.now, interval, 0); got != tc.want {
				t.Errorf("IsStale() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The menu bar's "stale" badge and `gaps` must agree, or the UI says data is
// flowing while the report says it stopped.
func TestIsStaleAgreesWithGaps(t *testing.T) {
	const interval = 300 * time.Second
	const last, now = int64(10000), int64(11000)

	stale := IsStale(last, now, interval, 0)
	gaps := Gaps([]store.Reading{r("AAA", "co2_ppm", last, 1)},
		GapOptions{Expected: interval, Until: now})

	if stale != (len(gaps) > 0) {
		t.Errorf("IsStale() = %v but Gaps() found %d gaps", stale, len(gaps))
	}
}

func TestDownsample(t *testing.T) {
	got := Downsample([]store.Reading{
		r("AAA", "co2_ppm", 1000, 600),
		r("AAA", "co2_ppm", 1100, 800),
		r("AAA", "co2_ppm", 4000, 500),
	}, 1000)

	want := []Bucket{
		{Start: 1000, Count: 2, Min: 600, Max: 800, Avg: 700},
		{Start: 4000, Count: 1, Min: 500, Max: 500, Avg: 500},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Downsample() = %+v\nwant %+v", got, want)
	}
}

func TestDownsampleKeepsExtremes(t *testing.T) {
	// A CO2 spike inside one bucket must survive as Max; averaging it away
	// would hide exactly the event worth seeing.
	got := Downsample([]store.Reading{
		r("AAA", "co2_ppm", 0, 500),
		r("AAA", "co2_ppm", 100, 2400),
		r("AAA", "co2_ppm", 200, 520),
	}, 1000)

	if len(got) != 1 {
		t.Fatalf("Downsample() = %+v, want 1 bucket", got)
	}
	if got[0].Max != 2400 {
		t.Errorf("Max = %v, want the 2400 spike preserved", got[0].Max)
	}
}

func TestDownsampleDegenerateInputs(t *testing.T) {
	if got := Downsample(nil, 1000); got != nil {
		t.Errorf("Downsample(nil) = %+v, want nil", got)
	}
	if got := Downsample([]store.Reading{r("AAA", "x", 1, 1)}, 0); got != nil {
		t.Errorf("Downsample(bucket 0) = %+v, want nil", got)
	}
}
