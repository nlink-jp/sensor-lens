package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state", "sensors.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenCreatesOwnerOnlyFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "sensors.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	// The database is a minute-by-minute record of whether anyone is home.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := fi.Mode().Perm(); got&0o077 != 0 {
		t.Errorf("db mode = %04o, want owner-only", got)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if got := di.Mode().Perm(); got&0o077 != 0 {
		t.Errorf("dir mode = %04o, want owner-only", got)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sensors.db")
	for range 2 {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		s.Close()
	}
}

func TestUpsertDevices(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0)

	in := []Device{
		{DeviceID: "AAA", Name: "Living Room", DeviceType: "MeterPro(CO2)", HubDeviceID: "HUB1", Version: "V4.2", Enabled: true},
		{DeviceID: "BBB", Name: "Bedroom", DeviceType: "Meter", HubDeviceID: "HUB1", Enabled: true},
	}
	if err := s.UpsertDevices(ctx, in, t0); err != nil {
		t.Fatalf("UpsertDevices() error = %v", err)
	}

	got, err := s.Devices(ctx)
	if err != nil {
		t.Fatalf("Devices() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Devices() = %d rows, want 2", len(got))
	}
	// Ordered by name: Bedroom before Living Room.
	if got[0].DeviceID != "BBB" || got[1].DeviceID != "AAA" {
		t.Errorf("Devices() order = %s,%s, want BBB,AAA", got[0].DeviceID, got[1].DeviceID)
	}
	if got[1].Version != "V4.2" {
		t.Errorf("version = %q, want V4.2", got[1].Version)
	}
}

func TestUpsertDevicesKeepsFirstSeenAndEnabled(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0)

	if err := s.UpsertDevices(ctx, []Device{{DeviceID: "AAA", Name: "Old", Enabled: true}}, t0); err != nil {
		t.Fatalf("UpsertDevices() error = %v", err)
	}
	// The user takes a device out of the collect set.
	if err := s.SetEnabled(ctx, "AAA", false); err != nil {
		t.Fatalf("SetEnabled() error = %v", err)
	}
	// A later refresh must not silently put it back.
	t1 := t0.Add(24 * time.Hour)
	if err := s.UpsertDevices(ctx, []Device{{DeviceID: "AAA", Name: "Renamed", Enabled: true}}, t1); err != nil {
		t.Fatalf("UpsertDevices() error = %v", err)
	}

	devices, err := s.Devices(ctx)
	if err != nil {
		t.Fatalf("Devices() error = %v", err)
	}
	d := devices[0]
	if d.Enabled {
		t.Error("a re-seen device was silently re-enabled; the collect set is the user's choice")
	}
	if d.Name != "Renamed" {
		t.Errorf("name = %q, want the refreshed Renamed", d.Name)
	}
	if d.FirstSeen != t0.Unix() {
		t.Errorf("FirstSeen = %d, want it preserved at %d", d.FirstSeen, t0.Unix())
	}
	if d.LastSeen != t1.Unix() {
		t.Errorf("LastSeen = %d, want %d", d.LastSeen, t1.Unix())
	}
}

func TestUpsertDevicesKeepsVersionWhenAbsent(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0)

	if err := s.UpsertDevices(ctx, []Device{{DeviceID: "AAA", Version: "V4.2"}}, t0); err != nil {
		t.Fatalf("UpsertDevices() error = %v", err)
	}
	// The device list endpoint does not carry a version; only status does. A
	// refresh from the list must not wipe what a poll learned.
	if err := s.UpsertDevices(ctx, []Device{{DeviceID: "AAA"}}, t0); err != nil {
		t.Fatalf("UpsertDevices() error = %v", err)
	}

	devices, _ := s.Devices(ctx)
	if devices[0].Version != "V4.2" {
		t.Errorf("version = %q, want it kept at V4.2", devices[0].Version)
	}
}

func TestInsertReadingsIsIdempotent(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	readings := []Reading{
		{DeviceID: "AAA", Metric: "co2_ppm", TS: 1000, Value: 620},
		{DeviceID: "AAA", Metric: "temperature_c", TS: 1000, Value: 22.4},
	}

	n, err := s.InsertReadings(ctx, readings, false)
	if err != nil {
		t.Fatalf("InsertReadings() error = %v", err)
	}
	if n != 2 {
		t.Fatalf("first insert = %d rows, want 2", n)
	}

	// Re-importing an overlapping export must add nothing.
	n, err = s.InsertReadings(ctx, readings, false)
	if err != nil {
		t.Fatalf("InsertReadings() error = %v", err)
	}
	if n != 0 {
		t.Errorf("re-insert = %d new rows, want 0 (backfill must be idempotent)", n)
	}

	total, err := s.CountReadings(ctx)
	if err != nil {
		t.Fatalf("CountReadings() error = %v", err)
	}
	if total != 2 {
		t.Errorf("stored %d rows, want 2", total)
	}
}

func TestInsertReadingsOnlyFillsGaps(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	if _, err := s.InsertReadings(ctx, []Reading{
		{DeviceID: "AAA", Metric: "co2_ppm", TS: 1000, Value: 620},
		{DeviceID: "AAA", Metric: "co2_ppm", TS: 3000, Value: 700},
	}, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A CSV covering the whole window, overlapping what we already hold.
	n, err := s.InsertReadings(ctx, []Reading{
		{DeviceID: "AAA", Metric: "co2_ppm", TS: 1000, Value: 620},
		{DeviceID: "AAA", Metric: "co2_ppm", TS: 2000, Value: 655},
		{DeviceID: "AAA", Metric: "co2_ppm", TS: 3000, Value: 700},
	}, false)
	if err != nil {
		t.Fatalf("InsertReadings() error = %v", err)
	}
	if n != 1 {
		t.Errorf("backfill inserted %d rows, want only the 1 missing sample", n)
	}
}

func TestInsertReadingsDryRunWritesNothing(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	n, err := s.InsertReadings(ctx, []Reading{
		{DeviceID: "AAA", Metric: "co2_ppm", TS: 1000, Value: 620},
		{DeviceID: "AAA", Metric: "co2_ppm", TS: 2000, Value: 650},
	}, true)
	if err != nil {
		t.Fatalf("InsertReadings(dryRun) error = %v", err)
	}
	if n != 2 {
		t.Errorf("dry run reported %d new rows, want 2", n)
	}

	total, _ := s.CountReadings(ctx)
	if total != 0 {
		t.Errorf("dry run persisted %d rows, want 0", total)
	}
}

func TestLatestAndHistory(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	if _, err := s.InsertReadings(ctx, []Reading{
		{DeviceID: "AAA", Metric: "co2_ppm", TS: 1000, Value: 600},
		{DeviceID: "AAA", Metric: "co2_ppm", TS: 2000, Value: 700},
		{DeviceID: "AAA", Metric: "temperature_c", TS: 2000, Value: 22.5},
		{DeviceID: "BBB", Metric: "temperature_c", TS: 1500, Value: 19},
	}, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	latest, err := s.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	want := []Reading{
		{DeviceID: "AAA", Metric: "co2_ppm", TS: 2000, Value: 700},
		{DeviceID: "AAA", Metric: "temperature_c", TS: 2000, Value: 22.5},
		{DeviceID: "BBB", Metric: "temperature_c", TS: 1500, Value: 19},
	}
	if !reflect.DeepEqual(latest, want) {
		t.Errorf("Latest() = %+v\nwant %+v", latest, want)
	}

	hist, err := s.History(ctx, "AAA", "co2_ppm", 0, 1500)
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if len(hist) != 1 || hist[0].TS != 1000 {
		t.Errorf("History(0..1500) = %+v, want just ts=1000", hist)
	}

	metrics, err := s.Metrics(ctx, "AAA")
	if err != nil {
		t.Fatalf("Metrics() error = %v", err)
	}
	if !reflect.DeepEqual(metrics, []string{"co2_ppm", "temperature_c"}) {
		t.Errorf("Metrics() = %v", metrics)
	}

	last, err := s.LastReadingTime(ctx)
	if err != nil {
		t.Fatalf("LastReadingTime() error = %v", err)
	}
	if last != 2000 {
		t.Errorf("LastReadingTime() = %d, want 2000", last)
	}
}

func TestLastReadingTimeEmpty(t *testing.T) {
	s := openTest(t)
	got, err := s.LastReadingTime(context.Background())
	if err != nil {
		t.Fatalf("LastReadingTime() error = %v", err)
	}
	if got != 0 {
		t.Errorf("LastReadingTime() on an empty DB = %d, want 0", got)
	}
}

func TestPrune(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	if _, err := s.InsertReadings(ctx, []Reading{
		{DeviceID: "AAA", Metric: "co2_ppm", TS: 1000, Value: 600},
		{DeviceID: "AAA", Metric: "co2_ppm", TS: 2000, Value: 700},
		{DeviceID: "AAA", Metric: "co2_ppm", TS: 3000, Value: 800},
	}, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	n, err := s.Prune(ctx, 2000)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if n != 1 {
		t.Errorf("Prune(before 2000) removed %d rows, want 1", n)
	}
	total, _ := s.CountReadings(ctx)
	if total != 2 {
		t.Errorf("%d rows left, want 2", total)
	}
}

func TestPruneDevice(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	if _, err := s.InsertReadings(ctx, []Reading{
		{DeviceID: "HUM", Metric: "humidity_pct", TS: 1000, Value: 0},
		{DeviceID: "HUM", Metric: "mode", TS: 1000, Value: 0},
		{DeviceID: "AAA", Metric: "temperature_c", TS: 1000, Value: 27},
	}, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if n, err := s.CountDeviceReadings(ctx, "HUM"); err != nil || n != 2 {
		t.Fatalf("CountDeviceReadings() = %d, %v; want 2, nil", n, err)
	}

	n, err := s.PruneDevice(ctx, "HUM")
	if err != nil {
		t.Fatalf("PruneDevice() error = %v", err)
	}
	if n != 2 {
		t.Errorf("PruneDevice() removed %d rows, want 2", n)
	}
	// Only the named device goes.
	if total, _ := s.CountReadings(ctx); total != 1 {
		t.Errorf("%d readings left, want the other device's 1", total)
	}
}

func TestAPICallAccounting(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	if n, err := s.APICalls(ctx, "2026-08-02"); err != nil || n != 0 {
		t.Fatalf("APICalls(unseen day) = %d, %v; want 0, nil", n, err)
	}

	if n, err := s.AddAPICalls(ctx, "2026-08-02", 7); err != nil || n != 7 {
		t.Fatalf("AddAPICalls() = %d, %v; want 7, nil", n, err)
	}
	// The count must survive across calls: a daemon restart mid-day must not
	// forget the quota already spent.
	if n, err := s.AddAPICalls(ctx, "2026-08-02", 3); err != nil || n != 10 {
		t.Fatalf("AddAPICalls() = %d, %v; want 10, nil", n, err)
	}
	// A new day starts from zero.
	if n, err := s.APICalls(ctx, "2026-08-03"); err != nil || n != 0 {
		t.Fatalf("APICalls(next day) = %d, %v; want 0, nil", n, err)
	}
}

func TestAPICallCountSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sensors.db")
	ctx := context.Background()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := s.AddAPICalls(ctx, "2026-08-02", 42); err != nil {
		t.Fatalf("AddAPICalls() error = %v", err)
	}
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer s2.Close()

	n, err := s2.APICalls(ctx, "2026-08-02")
	if err != nil {
		t.Fatalf("APICalls() error = %v", err)
	}
	if n != 42 {
		t.Errorf("APICalls() after reopen = %d, want 42", n)
	}
}
