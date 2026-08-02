// Package aggregate turns stored readings into the shapes the CLI and the GUI
// display. Everything here is a pure function of its inputs: the store owns
// persistence, this package owns interpretation, so thresholds can change and
// history be re-read under the new ones without touching what was recorded.
package aggregate

import (
	"math"
	"sort"
	"time"

	"github.com/nlink-jp/sensor-lens/core/store"
)

// DefaultGapFactor is how many nominal intervals may pass before the silence
// counts as a gap rather than jitter. Two consecutive misses is a real outage;
// one late sample is not.
const DefaultGapFactor = 2.5

// Summary describes one (device, metric) series over a time range.
type Summary struct {
	DeviceID string  `json:"device_id"`
	Metric   string  `json:"metric"`
	Count    int     `json:"count"`
	Min      float64 `json:"min"`
	Max      float64 `json:"max"`
	Avg      float64 `json:"avg"`
	First    float64 `json:"first"`
	Last     float64 `json:"last"`
	FirstTS  int64   `json:"first_ts"`
	LastTS   int64   `json:"last_ts"`
}

// Summarize reduces readings to one Summary per (device, metric), ordered by
// device then metric. Input order does not matter.
func Summarize(readings []store.Reading) []Summary {
	type key struct{ device, metric string }
	index := map[key]*Summary{}

	for _, r := range readings {
		k := key{r.DeviceID, r.Metric}
		s, ok := index[k]
		if !ok {
			index[k] = &Summary{
				DeviceID: r.DeviceID, Metric: r.Metric, Count: 1,
				Min: r.Value, Max: r.Value, Avg: r.Value,
				First: r.Value, Last: r.Value,
				FirstTS: r.TS, LastTS: r.TS,
			}
			continue
		}
		s.Count++
		s.Avg += r.Value // running total; divided once at the end
		s.Min = math.Min(s.Min, r.Value)
		s.Max = math.Max(s.Max, r.Value)
		if r.TS < s.FirstTS {
			s.FirstTS, s.First = r.TS, r.Value
		}
		if r.TS > s.LastTS {
			s.LastTS, s.Last = r.TS, r.Value
		}
	}

	out := make([]Summary, 0, len(index))
	for _, s := range index {
		// Avg carried the running total (seeded by the first value).
		s.Avg /= float64(s.Count)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DeviceID != out[j].DeviceID {
			return out[i].DeviceID < out[j].DeviceID
		}
		return out[i].Metric < out[j].Metric
	})
	return out
}

// Gap is a stretch of time with no readings for one (device, metric).
type Gap struct {
	DeviceID string `json:"device_id"`
	Metric   string `json:"metric"`
	Start    int64  `json:"start"`
	End      int64  `json:"end"`
	// Missing estimates how many samples the gap swallowed, at the nominal
	// interval. It is what you would recover by importing that window.
	Missing int `json:"missing"`
}

// Duration returns how long the gap lasted.
func (g Gap) Duration() time.Duration { return time.Duration(g.End-g.Start) * time.Second }

// GapOptions configures gap detection.
type GapOptions struct {
	// Expected is the nominal polling interval.
	Expected time.Duration
	// Factor scales Expected into the threshold; zero means DefaultGapFactor.
	Factor float64
	// Since and Until bound the window. A series that starts after Since, or
	// ends before Until, yields a leading or trailing gap — those edges are the
	// ones a daemon outage produces, so they must not be silently dropped.
	Since int64
	Until int64
}

// Gaps finds the stretches with no readings, per (device, metric).
//
// This is the "what should I import?" query: each gap names a window the
// SwitchBot app can export and `sensor-lens import` can merge back in.
func Gaps(readings []store.Reading, opt GapOptions) []Gap {
	if opt.Expected <= 0 {
		return nil
	}
	factor := opt.Factor
	if factor <= 0 {
		factor = DefaultGapFactor
	}
	threshold := int64(float64(opt.Expected/time.Second) * factor)
	if threshold < 1 {
		threshold = 1
	}

	series := groupSeries(readings)
	var out []Gap
	for _, s := range series {
		out = append(out, seriesGaps(s, threshold, opt)...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		if out[i].DeviceID != out[j].DeviceID {
			return out[i].DeviceID < out[j].DeviceID
		}
		return out[i].Metric < out[j].Metric
	})
	return out
}

type series struct {
	deviceID string
	metric   string
	ts       []int64
}

func groupSeries(readings []store.Reading) []series {
	type key struct{ device, metric string }
	index := map[key][]int64{}
	for _, r := range readings {
		k := key{r.DeviceID, r.Metric}
		index[k] = append(index[k], r.TS)
	}

	out := make([]series, 0, len(index))
	for k, ts := range index {
		sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
		out = append(out, series{deviceID: k.device, metric: k.metric, ts: ts})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].deviceID != out[j].deviceID {
			return out[i].deviceID < out[j].deviceID
		}
		return out[i].metric < out[j].metric
	})
	return out
}

func seriesGaps(s series, threshold int64, opt GapOptions) []Gap {
	if len(s.ts) == 0 {
		return nil
	}
	interval := int64(opt.Expected / time.Second)

	mk := func(start, end int64) Gap {
		missing := 0
		if interval > 0 {
			if n := (end-start)/interval - 1; n > 0 {
				missing = int(n)
			}
		}
		return Gap{DeviceID: s.deviceID, Metric: s.metric, Start: start, End: end, Missing: missing}
	}

	var out []Gap
	if opt.Since > 0 && s.ts[0]-opt.Since > threshold {
		out = append(out, mk(opt.Since, s.ts[0]))
	}
	for i := 1; i < len(s.ts); i++ {
		if s.ts[i]-s.ts[i-1] > threshold {
			out = append(out, mk(s.ts[i-1], s.ts[i]))
		}
	}
	if opt.Until > 0 {
		if last := s.ts[len(s.ts)-1]; opt.Until-last > threshold {
			out = append(out, mk(last, opt.Until))
		}
	}
	return out
}

// IsStale reports whether a reading taken at lastTS is too old to be shown as
// current at now. A reading is stale once more than factor intervals have
// passed without a fresh one — the same threshold gap detection uses, so the
// menu bar and `gaps` never disagree about whether data is flowing.
func IsStale(lastTS, now int64, expected time.Duration, factor float64) bool {
	if lastTS <= 0 {
		return true
	}
	if expected <= 0 {
		return false
	}
	if factor <= 0 {
		factor = DefaultGapFactor
	}
	return float64(now-lastTS) > float64(expected/time.Second)*factor
}

// Bucket is one downsampled span of a series.
type Bucket struct {
	Start int64   `json:"start"`
	Count int     `json:"count"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Avg   float64 `json:"avg"`
}

// Downsample groups a single series into fixed-width buckets, keeping min and
// max alongside the mean so a chart does not hide a spike it averaged away.
// Empty buckets are omitted; the caller draws the gap.
func Downsample(readings []store.Reading, bucketSeconds int64) []Bucket {
	if bucketSeconds <= 0 || len(readings) == 0 {
		return nil
	}
	sorted := append([]store.Reading(nil), readings...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TS < sorted[j].TS })

	var out []Bucket
	var cur *Bucket
	var sum float64
	for _, r := range sorted {
		start := r.TS - r.TS%bucketSeconds
		if cur == nil || cur.Start != start {
			if cur != nil {
				cur.Avg = sum / float64(cur.Count)
				out = append(out, *cur)
			}
			cur = &Bucket{Start: start, Count: 0, Min: r.Value, Max: r.Value}
			sum = 0
		}
		cur.Count++
		sum += r.Value
		cur.Min = math.Min(cur.Min, r.Value)
		cur.Max = math.Max(cur.Max, r.Value)
	}
	if cur != nil {
		cur.Avg = sum / float64(cur.Count)
		out = append(out, *cur)
	}
	return out
}
