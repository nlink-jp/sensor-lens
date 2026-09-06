# AGENTS.md — sensor-lens

## What it is

A CLI that polls SwitchBot sensors through the Open API v1.1 and keeps their
temperature / humidity / CO2 readings in a local SQLite database. Read-only: it
lists devices and reads status, and deliberately implements no command sending.
A menu-bar GUI (Swift, separate repo) is a thin front over `--json`.

## Build / test / run

```sh
make build     # -> dist/sensor-lens  (NEVER `go build` directly)
make test      # go test ./...
make test-linux # same suite on Linux (container)
make vet       # darwin + linux + windows (covers the build-tagged platform files)
make package   # zip/tar.gz + notarize the darwin arm64 asset
make verify-release  # gate: .notarized marker + freshness (run before upload)
```

Version is injected from `git describe` via `-ldflags -X main.version`.
**No cgo** — SQLite is pure Go, so every platform cross-compiles from a Mac.

## Layout

```
main.go                 version wiring -> cmd.Execute
cmd/
  cmd.go                Execute(): dispatch + usage
  env.go                config + store + client wiring shared by commands
  commands.go           devices/now/daemon/history/report/gaps/import/export/
                        status/doctor/install/uninstall/prune
  format.go             PURE: value/duration rendering, grouping, ParseTime
core/
  switchbot/            API client
    sign.go             PURE HMAC-SHA256 signature (verified against a fixed vector)
    client.go           Doer-injected HTTP; status bodies stay map[string]any
    errors.go           statusCode -> APIError with Transient()/RateLimited()
  metrics/              PURE: status body -> []Reading; the canonical-name table
  store/                SQLite (modernc.org/sqlite): devices, readings, api_calls
  aggregate/            PURE: Summarize, Gaps, IsStale, Downsample
  poller/               resident loop; budget accounting and backoff
  importer/             the app's CSV export -> []Reading
  config/               hand-rolled TOML (no external dep) + env overrides
  platform/             config/data paths + launchd LaunchAgent
```

## Design invariants / gotchas

- **Never switch on `deviceType`.** Readings are extracted by shape: every
  numeric (or boolean) scalar in the status body becomes a metric, and only the
  naming is table-driven in `core/metrics`. This is what makes a new model or a
  new firmware field work without a code change. `devices --raw` is the escape
  hatch when you need the truth.

- **Long format is what makes backfill idempotent.** The primary key is
  `(device_id, metric, ts)`, so `INSERT OR IGNORE` merges an overlapping export
  and adds only what is missing. Do not add a column-per-metric table.

- **The API has no history endpoint.** Nine endpoints exist (devices, status,
  commands, scenes ×2, webhook ×4) and status is current-only. Recovering a gap
  is only possible through the app's CSV export → `sensor-lens import`. Do not
  go looking for a history API; it is not there.

- **Going over quota looks like a bad token.** Above 10,000 calls a day the API
  answers "Unauthorized" — the same HTTP 401 an invalid token produces. So
  `APIError.RateLimited()` is true for 401 as well as 429, the spent-call count
  lives in the database (a restart must not forget it), and `doctor`/`install`
  refuse a schedule that would cross `api.daily_budget`.

- **Credentials never go in the LaunchAgent plist.** It is world-readable by
  convention; the 0600 config file is the daemon's only source. The
  `SWITCHBOT_TOKEN` / `SWITCHBOT_SECRET` environment variables are for
  interactive use only — launchd does not carry the user's shell environment.

- **`UpsertDevices` never re-enables a device.** The collect set is the user's
  choice, so a routine device-list refresh must not silently switch collection
  back on. An explicit `[polling] devices` list is applied afterwards with
  `SetEnabled`, which is why `RefreshDevices` does both.

- **Collection has one owner at a time, enforced not agreed.** `daemon` takes a
  `flock` beside the database; a second one refuses to start. Two collectors
  would not corrupt anything — SQLite handles it — but would silently spend
  twice the daily quota. flock is used rather than a PID file because the kernel
  releases it when the holder dies.

- **`now --if-stale` is the front end's tick.** It polls only when the newest
  reading has aged past the interval, so a menu-bar app can collect on its own
  timer without knowing whether a daemon exists: if one is running, the data is
  fresh and the tick costs zero calls. Do not replace this with a
  "is the daemon loaded?" check — that is the coordination protocol it exists to
  avoid.

- **`collecting` is judged by data freshness, never by `daemon_loaded`.** The
  collector may be launchd, a menu-bar app, or a daemon started by hand. This is
  the same trick `active-lens-gui` uses for its recording indicator.

- **The backoff resets only on a clean round.** Resetting it unconditionally
  after every poll pins the wait at one interval, so repeated rate limits never
  actually slow anything down. (Caught by `TestRunBacksOffWhenRateLimited`.)

- **`EnsureDir` does not fail on a directory it cannot tighten.** A database
  pointed at a synced or shared folder is the user's call; what protects the
  readings is the 0600 on the file, which is always ours to set.

## Facts about the API worth not re-deriving

Confirmed against the official docs (OpenWonderLabs/SwitchBotAPI) and real
hardware:

- CO2 is the field `CO2` (integer ppm, 0–9999) and only `MeterPro(CO2)` has it.
  Hub 2 / Hub 3 do **not** report CO2.
- `temperature` in a status body is **always Celsius**. The `scale` field that
  says otherwise exists only on webhook events, never on status.
- **`Meter`'s `battery` is quantized to four steps** (`<10→10`, `10-20→20`,
  `20-60→60`, `≥60→100`) while other models report a real percentage. A battery
  chart mixing them looks like a staircase; that is the device, not a bug.
- Webhook `deviceType` strings are a different namespace from status ones
  (`Meter`→`WoMeter`, `Hub 2`→`WoHub2`). Do not treat them as interchangeable.
- Japanese product names do not match API device types. Observed on real
  hardware: デイリーステーション reports as **`WeatherStation`**
  (temperature/humidity/battery). Do not guess these from the product name —
  `Home Climate Panel` is a *different* device that also exists and reports
  `brightness` 1–100, itself a different scale from Hub 2's `lightLevel` 1–20,
  which is why they are kept as separate metrics rather than merged.
- **Auto-classification cannot key on humidity.** A `Humidifier2` returns
  `{"humidity":0,"mode":0,"childLock":0,"drying":0}` — an appliance describing
  itself, not a sensor describing the room — and enabling it spends quota on
  noise. Hence `metrics.ambient` is temperature and CO2 only. Verified on real
  hardware: 30 devices, 15 correctly auto-collected.
- The classification is **durable**, so correcting the rule does not reach
  devices already in the database. `devices --reclassify` re-probes them; that
  is the migration path for any future change to `ambient`.

## Facts about the app's CSV export

Determined from real exports of five devices:

- Header: `Timestamp`, `Temperature_Celsius(°C)`, `Relative_Humidity(%)`,
  `CO2(ppm)` (CO2 models only), then the app-computed
  `Absolute_Humidity(g/m³)`, `DPT_Celsius(°C)`, `VPD(kPa)`. Every cell quoted.
- Timestamps are `2006-01-02 15:04:05` with **no zone** — read as local time.
- Cadence is roughly 60 s with jitter (60–90 s observed), far denser than
  polling. Gap detection uses a multiple of the expected interval for exactly
  this reason.
- The file carries **no device ID**, only the name in the filename
  (`<name>_data.csv`), which is why `import` resolves a device by name.
- Derived columns are skipped unless `--all-columns`: the API never produces
  them, so importing them makes series that exist only inside imported windows.
- A UTF-8 BOM must be stripped **before** the CSV reader sees it, or the first
  header cell fails to parse and takes the whole file with it.

## Status

Phase 1 (CLI) implemented and verified on real hardware: `make build` /
`make test` / `make vet` green; 30 devices discovered and 15 correctly
auto-collected; live polling, `report` and `gaps` exercised; five real app
exports imported (15,519 readings) with a re-import inserting 0.

Phase 2 is the Swift menu-bar GUI. See `docs/en/sensor-lens-rfp.md`.
