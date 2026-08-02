// Package poller is the resident loop that reads the sensors on a schedule.
//
// Its job is as much about *not* calling the API as calling it: the account has
// a hard daily quota, and going over it makes every request fail with the same
// "Unauthorized" a bad token produces. So the poller tracks what it has spent
// in durable storage, refuses to start a round it cannot afford, and backs off
// when the API pushes back.
package poller

import (
	"context"
	"strings"
	"time"

	"github.com/nlink-jp/sensor-lens/core/config"
	"github.com/nlink-jp/sensor-lens/core/metrics"
	"github.com/nlink-jp/sensor-lens/core/store"
	"github.com/nlink-jp/sensor-lens/core/switchbot"
)

// MaxBackoff caps the wait after repeated rate-limit responses. Beyond this
// there is no point growing further: a spent daily quota clears at midnight,
// not sooner.
const MaxBackoff = 30 * time.Minute

// API is the slice of the SwitchBot client the poller needs.
type API interface {
	Devices(ctx context.Context) ([]switchbot.Device, error)
	DeviceStatus(ctx context.Context, deviceID string) (map[string]any, error)
}

// Recorder is the slice of the store the poller needs.
type Recorder interface {
	UpsertDevices(ctx context.Context, devices []store.Device, now time.Time) error
	SetEnabled(ctx context.Context, deviceID string, enabled bool) error
	Devices(ctx context.Context) ([]store.Device, error)
	InsertReadings(ctx context.Context, readings []store.Reading, dryRun bool) (int, error)
	AddAPICalls(ctx context.Context, day string, n int) (int, error)
	APICalls(ctx context.Context, day string) (int, error)
}

// Clock supplies the time and the waiting, so tests need not.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

// Poller reads devices on a schedule and records what they report.
type Poller struct {
	api   API
	store Recorder
	cfg   config.Config
	clock Clock
	logf  func(format string, args ...any)

	backoff time.Duration
}

// Option customizes a Poller.
type Option func(*Poller)

// WithClock injects the time source and the sleeper.
func WithClock(c Clock) Option { return func(p *Poller) { p.clock = c } }

// WithLogger injects the progress logger. The default discards output.
func WithLogger(logf func(format string, args ...any)) Option {
	return func(p *Poller) { p.logf = logf }
}

// New builds a poller.
func New(api API, rec Recorder, cfg config.Config, opts ...Option) *Poller {
	p := &Poller{
		api:   api,
		store: rec,
		cfg:   cfg,
		clock: realClock{},
		logf:  func(string, ...any) {},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// DeviceError records one device failing a round without taking the round down
// with it. A meter dropping off the mesh is normal; the daemon must survive it.
type DeviceError struct {
	DeviceID string `json:"device_id"`
	Name     string `json:"name,omitempty"`
	Err      string `json:"error"`
	// Transient marks failures that may resolve on their own (device or hub
	// offline) as opposed to ones needing the user to act.
	Transient bool `json:"transient"`
	// RateLimited marks the failures that mean "stop calling for a while".
	RateLimited bool `json:"rate_limited,omitempty"`
}

// Result is the outcome of one polling round.
type Result struct {
	At       time.Time       `json:"at"`
	Readings []store.Reading `json:"readings"`
	Polled   int             `json:"polled"`
	Inserted int             `json:"inserted"`
	Calls    int             `json:"calls"`
	Errors   []DeviceError   `json:"errors,omitempty"`
}

// RefreshDevices re-reads the account's device list and reconciles it with the
// collect set.
//
// With an explicit collect set in the config, membership is exactly that list.
// Without one, a device the poller has never seen is probed once and enabled
// only if its status carries an ambient measurement — that one-off call is how
// a CO2 meter is told apart from a plug without hardcoding device types.
func (p *Poller) RefreshDevices(ctx context.Context) ([]store.Device, error) {
	remote, err := p.api.Devices(ctx)
	p.spend(ctx, 1)
	if err != nil {
		return nil, err
	}

	known, err := p.store.Devices(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(known))
	for _, d := range known {
		seen[d.DeviceID] = true
	}

	explicit := len(p.cfg.Devices) > 0
	now := p.clock.Now()

	devices := make([]store.Device, 0, len(remote))
	for _, d := range remote {
		rec := store.Device{
			DeviceID:    d.DeviceID,
			Name:        d.DeviceName,
			DeviceType:  d.DeviceType,
			HubDeviceID: d.HubDeviceID,
		}
		switch {
		case explicit:
			rec.Enabled = p.inCollectSet(d)
		case seen[d.DeviceID]:
			// Already classified; UpsertDevices preserves the stored flag.
			rec.Enabled = true
		default:
			rec.Enabled = p.probeHasSensors(ctx, d)
		}
		devices = append(devices, rec)
	}

	if err := p.store.UpsertDevices(ctx, devices, now); err != nil {
		return nil, err
	}
	// UpsertDevices deliberately never re-enables a known device, so an
	// explicit collect set has to be applied on top of it.
	if explicit {
		for _, d := range devices {
			if err := p.store.SetEnabled(ctx, d.DeviceID, d.Enabled); err != nil {
				return nil, err
			}
		}
	}
	return p.store.Devices(ctx)
}

// inCollectSet matches a device against the configured list by ID or by name,
// case-insensitively, so `devices = ["Living Room"]` works as written on the app.
func (p *Poller) inCollectSet(d switchbot.Device) bool {
	for _, want := range p.cfg.Devices {
		if strings.EqualFold(want, d.DeviceID) || strings.EqualFold(want, d.DeviceName) {
			return true
		}
	}
	return false
}

// probeHasSensors spends one call to find out whether a device reports anything
// worth charting. Failures answer "no" for now; the device is re-probed on the
// next refresh because it stays unknown to the store.
func (p *Poller) probeHasSensors(ctx context.Context, d switchbot.Device) bool {
	body, err := p.api.DeviceStatus(ctx, d.DeviceID)
	p.spend(ctx, 1)
	if err != nil {
		p.logf("probe %s (%s): %v", d.DeviceName, d.DeviceID, err)
		return false
	}
	return metrics.HasEnvironmental(metrics.Extract(body))
}

// PollOnce reads every enabled device once and stores the readings.
func (p *Poller) PollOnce(ctx context.Context) (Result, error) {
	devices, err := p.store.Devices(ctx)
	if err != nil {
		return Result{}, err
	}

	now := p.clock.Now()
	res := Result{At: now}
	ts := now.Unix()

	for _, d := range devices {
		if !d.Enabled {
			continue
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}

		body, err := p.api.DeviceStatus(ctx, d.DeviceID)
		p.spend(ctx, 1)
		res.Calls++
		if err != nil {
			de := DeviceError{DeviceID: d.DeviceID, Name: d.Name, Err: err.Error()}
			if apiErr, ok := switchbot.AsAPIError(err); ok {
				de.Transient = apiErr.Transient()
				de.RateLimited = apiErr.RateLimited()
			}
			res.Errors = append(res.Errors, de)
			continue
		}

		res.Polled++
		for _, m := range metrics.Extract(body) {
			res.Readings = append(res.Readings, store.Reading{
				DeviceID: d.DeviceID, Metric: m.Metric, TS: ts, Value: m.Value,
			})
		}
		// The firmware version only appears in status, never in the device list.
		if v, ok := body["version"].(string); ok && v != "" {
			d.Version = v
			if err := p.store.UpsertDevices(ctx, []store.Device{d}, now); err != nil {
				return res, err
			}
		}
	}

	if len(res.Readings) > 0 {
		inserted, err := p.store.InsertReadings(ctx, res.Readings, false)
		if err != nil {
			return res, err
		}
		res.Inserted = inserted
	}
	return res, nil
}

// Run polls until the context is cancelled.
func (p *Poller) Run(ctx context.Context) error {
	interval := time.Duration(p.cfg.IntervalSeconds) * time.Second
	var lastRefresh time.Time

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		// The device list changes rarely; once a day is enough, and it keeps
		// the refresh off the per-round budget.
		if p.clock.Now().Sub(lastRefresh) >= 24*time.Hour {
			if _, err := p.RefreshDevices(ctx); err != nil {
				p.logf("refresh devices: %v", err)
				if p.handleErr(err) {
					if err := p.wait(ctx); err != nil {
						return err
					}
					continue
				}
			} else {
				lastRefresh = p.clock.Now()
			}
		}

		affordable, spent, cost, err := p.affordRound(ctx)
		if err != nil {
			return err
		}
		if !affordable {
			p.logf("daily budget reached (%d of %d calls spent; this round needs %d) — pausing until tomorrow",
				spent, p.cfg.DailyBudget, cost)
			if err := p.clock.Sleep(ctx, p.untilNextDay()); err != nil {
				return err
			}
			continue
		}

		res, err := p.PollOnce(ctx)
		if err != nil {
			return err
		}
		p.logf("polled %d device(s), stored %d reading(s), %d error(s)",
			res.Polled, res.Inserted, len(res.Errors))

		for _, e := range res.Errors {
			if !e.Transient {
				p.logf("device %s: %s", e.DeviceID, e.Err)
			}
		}
		// Only a round that was not rate-limited clears the backoff. Resetting
		// unconditionally would pin the wait at one interval forever, so
		// repeated refusals would never actually slow anything down.
		if p.anyRateLimited(res) {
			p.growBackoff(interval)
		} else {
			p.backoff = 0
		}

		if err := p.wait(ctx); err != nil {
			return err
		}
	}
}

// wait sleeps for the polling interval, or for the current backoff when one is
// in force.
func (p *Poller) wait(ctx context.Context) error {
	d := time.Duration(p.cfg.IntervalSeconds) * time.Second
	if p.backoff > d {
		d = p.backoff
	}
	return p.clock.Sleep(ctx, d)
}

// handleErr grows the backoff for a rate-limited failure and reports whether
// the loop should simply wait and retry.
func (p *Poller) handleErr(err error) bool {
	apiErr, ok := switchbot.AsAPIError(err)
	if !ok {
		return false
	}
	if apiErr.RateLimited() {
		p.growBackoff(time.Duration(p.cfg.IntervalSeconds) * time.Second)
	}
	return apiErr.Transient() || apiErr.RateLimited()
}

func (p *Poller) anyRateLimited(res Result) bool {
	for _, e := range res.Errors {
		if e.RateLimited {
			return true
		}
	}
	return false
}

func (p *Poller) growBackoff(base time.Duration) {
	if p.backoff == 0 {
		p.backoff = base
	} else {
		p.backoff *= 2
	}
	if p.backoff > MaxBackoff {
		p.backoff = MaxBackoff
	}
}

// affordRound reports whether the next round fits inside the daily budget.
func (p *Poller) affordRound(ctx context.Context) (ok bool, spent, cost int, err error) {
	devices, err := p.store.Devices(ctx)
	if err != nil {
		return false, 0, 0, err
	}
	for _, d := range devices {
		if d.Enabled {
			cost++
		}
	}
	spent, err = p.store.APICalls(ctx, p.day())
	if err != nil {
		return false, 0, cost, err
	}
	return spent+cost <= p.cfg.DailyBudget, spent, cost, nil
}

// spend records calls against today's quota. A failure to record is logged but
// never fatal: losing the count is better than taking the daemon down, and the
// next round re-reads it.
func (p *Poller) spend(ctx context.Context, n int) {
	if _, err := p.store.AddAPICalls(ctx, p.day(), n); err != nil {
		p.logf("record api call: %v", err)
	}
}

// day is the local calendar day the quota is counted against. SwitchBot resets
// on its own clock, which we cannot observe; the local day is the closest
// approximation and errs toward pausing early.
func (p *Poller) day() string { return p.clock.Now().Format("2006-01-02") }

func (p *Poller) untilNextDay() time.Duration {
	now := p.clock.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(24 * time.Hour)
	if d := next.Sub(now); d > 0 {
		return d
	}
	return time.Minute
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
