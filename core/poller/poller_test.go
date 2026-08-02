package poller

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nlink-jp/sensor-lens/core/config"
	"github.com/nlink-jp/sensor-lens/core/store"
	"github.com/nlink-jp/sensor-lens/core/switchbot"
)

// fakeAPI answers from canned data and records what was asked.
type fakeAPI struct {
	devices    []switchbot.Device
	devicesErr error
	status     map[string]map[string]any
	statusErr  map[string]error
	statusCall map[string]int
}

func (f *fakeAPI) Devices(context.Context) ([]switchbot.Device, error) {
	return f.devices, f.devicesErr
}

func (f *fakeAPI) DeviceStatus(_ context.Context, id string) (map[string]any, error) {
	if f.statusCall == nil {
		f.statusCall = map[string]int{}
	}
	f.statusCall[id]++
	if err, ok := f.statusErr[id]; ok {
		return nil, err
	}
	body, ok := f.status[id]
	if !ok {
		return nil, &switchbot.APIError{StatusCode: switchbot.StatusDeviceNotFound, HTTPStatus: 200}
	}
	return body, nil
}

// fakeClock advances a fixed amount per sleep and cancels the run after a set
// number of sleeps, which is how the resident loop is bounded in tests.
type fakeClock struct {
	now        time.Time
	sleeps     []time.Duration
	maxSleeps  int
	cancelFunc context.CancelFunc
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	if c.maxSleeps > 0 && len(c.sleeps) >= c.maxSleeps && c.cancelFunc != nil {
		c.cancelFunc()
	}
	return ctx.Err()
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "sensors.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func baseConfig() config.Config {
	cfg := config.Defaults("/data")
	cfg.Token, cfg.Secret = "t", "s"
	return cfg
}

func TestRefreshDevicesProbesUnknownDevices(t *testing.T) {
	api := &fakeAPI{
		devices: []switchbot.Device{
			{DeviceID: "CO2", DeviceName: "Room 1", DeviceType: "MeterPro(CO2)"},
			{DeviceID: "PLUG", DeviceName: "Desk", DeviceType: "Plug Mini (JP)"},
		},
		status: map[string]map[string]any{
			"CO2": {"temperature": 27.2, "humidity": 47.0, "CO2": 1148.0},
			// A plug reports numbers, but none of them are ambient readings —
			// it must not be pulled into the polling schedule.
			"PLUG": {"voltage": 100.0, "weight": 12.0, "electricCurrent": 120.0},
		},
	}
	s := newStore(t)
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	p := New(api, s, baseConfig(), WithClock(clock))

	devices, err := p.RefreshDevices(context.Background())
	if err != nil {
		t.Fatalf("RefreshDevices() error = %v", err)
	}

	enabled := map[string]bool{}
	for _, d := range devices {
		enabled[d.DeviceID] = d.Enabled
	}
	if !enabled["CO2"] {
		t.Error("CO2 meter was not enabled")
	}
	if enabled["PLUG"] {
		t.Error("plug was enabled; only devices with ambient readings should be")
	}

	// A second refresh must not re-probe: the classification is durable, and
	// each probe costs a call against the daily quota.
	before := api.statusCall["CO2"]
	if _, err := p.RefreshDevices(context.Background()); err != nil {
		t.Fatalf("RefreshDevices() error = %v", err)
	}
	if api.statusCall["CO2"] != before {
		t.Errorf("known device re-probed: %d calls, want %d", api.statusCall["CO2"], before)
	}
}

func TestRefreshDevicesHonoursExplicitCollectSet(t *testing.T) {
	api := &fakeAPI{
		devices: []switchbot.Device{
			{DeviceID: "CO2", DeviceName: "Room 1", DeviceType: "MeterPro(CO2)"},
			{DeviceID: "HUB", DeviceName: "リビング", DeviceType: "Hub 2"},
		},
		status: map[string]map[string]any{
			"CO2": {"temperature": 27.2, "CO2": 1148.0},
			"HUB": {"temperature": 30.3, "humidity": 62.0},
		},
	}
	s := newStore(t)
	cfg := baseConfig()
	cfg.Devices = []string{"リビング"} // by name, as it reads in the app
	p := New(api, s, cfg, WithClock(&fakeClock{now: time.Unix(1_700_000_000, 0)}))

	devices, err := p.RefreshDevices(context.Background())
	if err != nil {
		t.Fatalf("RefreshDevices() error = %v", err)
	}

	for _, d := range devices {
		want := d.DeviceID == "HUB"
		if d.Enabled != want {
			t.Errorf("device %s enabled = %v, want %v", d.DeviceID, d.Enabled, want)
		}
	}
	// An explicit list must not spend calls probing.
	if len(api.statusCall) != 0 {
		t.Errorf("probed %v despite an explicit collect set", api.statusCall)
	}
}

func TestRefreshDevicesAppliesCollectSetChanges(t *testing.T) {
	api := &fakeAPI{devices: []switchbot.Device{
		{DeviceID: "A", DeviceName: "A"},
		{DeviceID: "B", DeviceName: "B"},
	}}
	s := newStore(t)
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}

	cfg := baseConfig()
	cfg.Devices = []string{"A"}
	if _, err := New(api, s, cfg, WithClock(clock)).RefreshDevices(context.Background()); err != nil {
		t.Fatalf("RefreshDevices() error = %v", err)
	}

	// The user edits the list: the change must take effect, even though
	// UpsertDevices deliberately never re-enables a device on its own.
	cfg.Devices = []string{"B"}
	devices, err := New(api, s, cfg, WithClock(clock)).RefreshDevices(context.Background())
	if err != nil {
		t.Fatalf("RefreshDevices() error = %v", err)
	}
	for _, d := range devices {
		want := d.DeviceID == "B"
		if d.Enabled != want {
			t.Errorf("device %s enabled = %v, want %v", d.DeviceID, d.Enabled, want)
		}
	}
}

func TestPollOnceStoresReadings(t *testing.T) {
	api := &fakeAPI{
		devices: []switchbot.Device{{DeviceID: "CO2", DeviceName: "Room 1", DeviceType: "MeterPro(CO2)"}},
		status: map[string]map[string]any{
			"CO2": {"temperature": 27.2, "humidity": 47.0, "CO2": 1148.0, "battery": 100.0, "version": "V4.2"},
		},
	}
	s := newStore(t)
	now := time.Unix(1_700_000_000, 0)
	p := New(api, s, baseConfig(), WithClock(&fakeClock{now: now}))
	ctx := context.Background()

	if _, err := p.RefreshDevices(ctx); err != nil {
		t.Fatalf("RefreshDevices() error = %v", err)
	}
	res, err := p.PollOnce(ctx)
	if err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	if res.Polled != 1 {
		t.Errorf("Polled = %d, want 1", res.Polled)
	}
	if res.Inserted != 4 {
		t.Errorf("Inserted = %d, want 4 (temp, humidity, co2, battery)", res.Inserted)
	}
	for _, r := range res.Readings {
		if r.TS != now.Unix() {
			t.Errorf("reading %s has ts %d, want %d", r.Metric, r.TS, now.Unix())
		}
	}

	// The firmware version only ever appears in status, so a poll is the only
	// chance to learn it.
	devices, _ := s.Devices(ctx)
	if devices[0].Version != "V4.2" {
		t.Errorf("version = %q, want V4.2 recorded from the status body", devices[0].Version)
	}
}

func TestPollOnceSurvivesOfflineDevice(t *testing.T) {
	api := &fakeAPI{
		devices: []switchbot.Device{
			{DeviceID: "OK", DeviceName: "Room 1"},
			{DeviceID: "GONE", DeviceName: "Shed"},
		},
		status: map[string]map[string]any{
			"OK":   {"temperature": 22.0},
			"GONE": {"temperature": 5.0},
		},
	}
	s := newStore(t)
	p := New(api, s, baseConfig(), WithClock(&fakeClock{now: time.Unix(1_700_000_000, 0)}))
	ctx := context.Background()
	if _, err := p.RefreshDevices(ctx); err != nil {
		t.Fatalf("RefreshDevices() error = %v", err)
	}

	// The outdoor meter drops off the mesh, as they do.
	api.statusErr = map[string]error{
		"GONE": &switchbot.APIError{StatusCode: switchbot.StatusHubOffline, HTTPStatus: 200, Message: "hub offline"},
	}

	res, err := p.PollOnce(ctx)
	if err != nil {
		t.Fatalf("PollOnce() error = %v; one offline device must not fail the round", err)
	}
	if res.Polled != 1 {
		t.Errorf("Polled = %d, want the other device still read", res.Polled)
	}
	if len(res.Errors) != 1 || !res.Errors[0].Transient {
		t.Errorf("Errors = %+v, want one transient failure", res.Errors)
	}
}

func TestPollOnceCountsCallsAgainstTheDailyQuota(t *testing.T) {
	api := &fakeAPI{
		devices: []switchbot.Device{
			{DeviceID: "A", DeviceName: "A"},
			{DeviceID: "B", DeviceName: "B"},
		},
		status: map[string]map[string]any{
			"A": {"temperature": 20.0},
			"B": {"temperature": 21.0},
		},
	}
	s := newStore(t)
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.Local)
	p := New(api, s, baseConfig(), WithClock(&fakeClock{now: now}))
	ctx := context.Background()

	// 1 device-list call + 2 probes.
	if _, err := p.RefreshDevices(ctx); err != nil {
		t.Fatalf("RefreshDevices() error = %v", err)
	}
	if _, err := p.PollOnce(ctx); err != nil { // + 2 status calls
		t.Fatalf("PollOnce() error = %v", err)
	}

	spent, err := s.APICalls(ctx, now.Format("2006-01-02"))
	if err != nil {
		t.Fatalf("APICalls() error = %v", err)
	}
	if spent != 5 {
		t.Errorf("recorded %d calls, want 5 (1 list + 2 probes + 2 status)", spent)
	}
}

func TestRunStopsPollingWhenTheBudgetIsSpent(t *testing.T) {
	api := &fakeAPI{
		devices: []switchbot.Device{{DeviceID: "A", DeviceName: "A"}},
		status:  map[string]map[string]any{"A": {"temperature": 20.0}},
	}
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.Local)
	clock := &fakeClock{now: now, maxSleeps: 1, cancelFunc: cancel}

	cfg := baseConfig()
	cfg.DailyBudget = 2 // the list call plus one probe already exhaust it

	p := New(api, s, cfg, WithClock(clock))
	if err := p.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}

	// It must have parked until tomorrow rather than polled on.
	if len(clock.sleeps) != 1 {
		t.Fatalf("sleeps = %v, want exactly 1", clock.sleeps)
	}
	if clock.sleeps[0] < 13*time.Hour {
		t.Errorf("slept %v, want the rest of the day (14h from 10:00)", clock.sleeps[0])
	}
	if api.statusCall["A"] > 1 {
		t.Errorf("device polled %d times after the budget was spent", api.statusCall["A"])
	}
}

func TestRunPollsOnScheduleWithinBudget(t *testing.T) {
	api := &fakeAPI{
		devices: []switchbot.Device{{DeviceID: "A", DeviceName: "A"}},
		status:  map[string]map[string]any{"A": {"temperature": 20.0}},
	}
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clock := &fakeClock{now: time.Date(2026, 8, 2, 10, 0, 0, 0, time.Local), maxSleeps: 3, cancelFunc: cancel}
	cfg := baseConfig()
	cfg.IntervalSeconds = 300

	p := New(api, s, cfg, WithClock(clock))
	if err := p.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}

	for i, d := range clock.sleeps {
		if d != 300*time.Second {
			t.Errorf("sleep %d = %v, want the 300s interval", i, d)
		}
	}
	if got, err := s.CountReadings(context.Background()); err != nil || got != 3 {
		t.Errorf("stored %d readings, want 3 (one per round), err = %v", got, err)
	}
}

func TestRunBacksOffWhenRateLimited(t *testing.T) {
	api := &fakeAPI{
		devices: []switchbot.Device{{DeviceID: "A", DeviceName: "A"}},
		status:  map[string]map[string]any{"A": {"temperature": 20.0}},
	}
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clock := &fakeClock{now: time.Date(2026, 8, 2, 10, 0, 0, 0, time.Local), maxSleeps: 3, cancelFunc: cancel}
	cfg := baseConfig()
	cfg.IntervalSeconds = 300

	p := New(api, s, cfg, WithClock(clock))
	if _, err := p.RefreshDevices(ctx); err != nil {
		t.Fatalf("RefreshDevices() error = %v", err)
	}
	// The account trips the quota: the API answers 401 "Unauthorized", which is
	// indistinguishable from a bad token, so the poller must slow down rather
	// than hammer on.
	api.statusErr = map[string]error{
		"A": &switchbot.APIError{HTTPStatus: 401, Message: "Unauthorized"},
	}

	if err := p.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}

	if len(clock.sleeps) < 2 {
		t.Fatalf("sleeps = %v, want at least 2", clock.sleeps)
	}
	if clock.sleeps[1] <= clock.sleeps[0] {
		t.Errorf("sleeps = %v, want the wait to grow after a rate limit", clock.sleeps)
	}
}

func TestBackoffIsCapped(t *testing.T) {
	p := New(nil, nil, baseConfig())
	for range 20 {
		p.growBackoff(5 * time.Minute)
	}
	if p.backoff != MaxBackoff {
		t.Errorf("backoff = %v, want it capped at %v", p.backoff, MaxBackoff)
	}
}

func TestUntilNextDay(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 2, 22, 30, 0, 0, time.Local)}
	p := New(nil, nil, baseConfig(), WithClock(clock))

	if got, want := p.untilNextDay(), 90*time.Minute; got != want {
		t.Errorf("untilNextDay() = %v, want %v", got, want)
	}
}
