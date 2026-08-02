# RFP: sensor-lens

> Generated: 2026-08-02
> Status: Approved, Phase 1 implemented

## 1. Problem Statement

I want to know the temperature, humidity and CO2 in my rooms at a glance,
without opening the SwitchBot app — and I want the history kept, so "is it
worse than yesterday?" is answerable. The SwitchBot app shows current values
well but is a phone app, and no menu-bar tool for it appears to exist.

The target user is the developer themselves (personal use). Readings are stored
locally; nothing is sent anywhere except the authenticated calls to SwitchBot's
own API.

## 2. Functional Specification

### Commands / API Surface

A Go CLI (`sensor-lens`) does the collecting, storing and aggregating; a
Swift/SwiftUI menu-bar GUI is a thin front end that shells out to `--json` — the
same two-layer split as `active-lens` / `active-lens-gui`.

| Command | Role |
|---------|------|
| `sensor-lens daemon` | Resident polling; appends readings to SQLite |
| `sensor-lens now [--json] [--devices ids]` | Read the sensors right now |
| `sensor-lens devices [--json] [--raw] [--refresh]` | Device list and collect-set membership |
| `sensor-lens history --device D --metric M [flags]` | One metric over time (the GUI's chart source) |
| `sensor-lens report [--since --until]` | min / avg / max per metric |
| `sensor-lens gaps [--since --until]` | Windows with no readings |
| `sensor-lens import --file F [--dry-run]` | Merge a CSV exported from the app |
| `sensor-lens export --format csv\|json` | Write stored readings out |
| `sensor-lens install` / `uninstall` | Register/remove the launchd LaunchAgent |
| `sensor-lens status [--json]` | Daemon state, DB path, calls spent today |
| `sensor-lens doctor` | Diagnose config, credentials, quota, connectivity |
| `sensor-lens prune --keep-days N` | Delete old readings |

### Data model

Readings are stored in long format: one row per `(device_id, metric, ts)`. Two
consequences follow, and both are the point:

1. A device type the code has never seen is collected anyway, because extraction
   is shape-driven — every numeric or boolean scalar in the status body becomes
   a metric, and only the naming is table-driven.
2. Backfill is idempotent, because the primary key *is* the identity of a
   reading. `INSERT OR IGNORE` merges an overlapping export and adds only what
   is missing.

### Collect set vs display set

Three layers, with different owners:

| Layer | Contents | Owner |
|-------|----------|-------|
| collect set | which devices are polled and stored | CLI `config.toml` `[polling] devices` — the daemon needs it |
| popover | every collected device, shown as a card | fixed, no setting |
| menu-bar set | the ordered device × metric chips on the bar | GUI `UserDefaults` — the CLI never reads it |

The menu-bar set is a subset of the collect set, chosen per *device × metric* so
one sensor can contribute only its CO2. Collecting eight sensors and displaying
two is the expected configuration. The CLI's equivalent of the display choice is
`now --devices`.

### Configuration

`config.toml` in the OS config dir, mode 0600 (it holds the credentials):

- `[switchbot] token` / `secret` — overridable by `SWITCHBOT_TOKEN` /
  `SWITCHBOT_SECRET` for interactive use
- `[polling] interval_seconds` (default 300), `devices` (the collect set)
- `[api] daily_budget` (default 8000), `timeout_seconds`
- `[storage] db_path`, `retention_days` (default 400)

### External Dependencies

The SwitchBot Open API v1.1 over HTTPS, and a SwitchBot hub on the account.
Nothing else; no other network access.

## 3. Design Decisions

- **CLI = Go, no cgo.** SQLite is pure Go (`modernc.org/sqlite`), so the whole
  release matrix cross-compiles from a Mac. Only launchd scheduling is
  platform-specific, isolated behind `_darwin.go` / `_other.go`.
- **GUI = Swift/SwiftUI**, riding the same signing/notarization pipeline as
  `active-lens-gui`.
- **Resident daemon, not a per-tick `StartInterval` job.** The poller holds
  state a per-tick process would have to reload and re-derive every time: the
  running daily call count and the current backoff.
- **Read-only.** `POST /commands` is deliberately not implemented, so the tool
  cannot actuate a lock, a curtain or a plug. This is structural, not a policy.
- **Credentials live only in the 0600 config file.** A LaunchAgent plist is
  world-readable by convention, so putting the token in `EnvironmentVariables`
  would be strictly worse than the file.
- **Testability.** `metrics`, `aggregate`, `importer`, `switchbot/sign` and the
  CLI's formatting are pure functions; the API client takes an injected `Doer`
  and the poller an injected clock, so the resident loop, the budget and the
  backoff are all testable without a network or a wait.

### Out of scope (explicit)

1. Sending commands to devices (permanently out of scope)
2. Receiving SwitchBot webhooks — it needs a public HTTPS endpoint, which a
   local resident tool does not have. (A future option: point them at the
   existing `webhook-relay`. Note that webhook `deviceType` strings are a
   different namespace from status ones.)
3. Reading meters directly over BLE, bypassing the hub
4. Cloud sync / multi-device history merge (only "put the DB in a synced folder",
   single writer)
5. Vendors other than SwitchBot — the name allows for it, v0.1.0 does not

## 4. Development Plan

### Phase 0: Probe

Confirm against real hardware and real exports what the documentation does not
say. Completed for the CSV side (five device exports); the live API probe waits
on a token.

### Phase 1: CLI engine

Independently reviewable. Signed request client, shape-driven extraction, SQLite
store, resident poller with budget accounting, CSV importer, gap detection, and
the command surface above. Complete when `make test` is green and the pipeline
is verified end-to-end on real data.

### Phase 2: GUI

Menu-bar residency showing the chosen device × metric chips; a popover with all
collected devices (current value, trend against an hour ago, battery, a stale
badge); a Swift Charts analysis window; a CO2 threshold alert (1000 / 1500 ppm)
that recolours the bar and can notify; settings for the collect set, the
menu-bar set, the interval and the credentials; gaps hatched into the history
chart with the export→import route offered as the fix.

**Collection while the app runs.** The GUI ticks `now --if-stale` on its own
timer rather than requiring the LaunchAgent, which roughly halves the API spend
by collecting only while the user is present. A "collect in the background"
toggle installs the daemon for continuous history; when it is on, the GUI's tick
finds fresh data and costs nothing, so the two never double-spend and no
coordination protocol is needed. The recording indicator reads `collecting` from
`status --json`, which is judged by data freshness — so it is green for a daemon
started by hand as well.

Two traps are already known and must be handled: **App Nap freezes the timer**
of an `LSUIElement` app, so `ProcessInfo.beginActivity` is required and its
token held for the app's lifetime (`claude-usage-lens-gui` shipped this bug);
and the settings window must be a plain `Window` opened with `openWindow(id:)`,
not a `Settings` scene, which an `LSUIElement` app cannot focus.

### Phase 3: Release

Docs, signing, notarization, submodule registration, org profile, `check-org.sh`.

## 5. Required API Scopes / Permissions

A SwitchBot open token and secret, issued in the app (Profile → Preferences →
tap App Version ten times → Developer Options). The API has no scope system: the
token grants full access to the account, including *control* of every device.
That is precisely why this tool implements no command sending — the credential
is far more powerful than the use requires, so the restraint has to live in the
client.

No OS permissions are needed. macOS auto-start uses a per-user LaunchAgent.

## 6. Series Placement

Series: util-series.

A local collect/aggregate/visualize utility running the same "Go CLI engine +
Swift GUI, Developer ID signed and notarized" operations as `active-lens` and
`claude-usage-lens`, and a sibling in the `-lens` family.

## 7. External Platform Constraints

- **10,000 API calls per account per day.** Exceeding it does not fail loudly:
  the API begins answering "Unauthorized", the same HTTP 401 an invalid token
  produces. The daily spend is therefore counted in the database (a restart must
  not forget it), the budget defaults below the limit, and `doctor` / `install`
  refuse an unaffordable schedule.
- **No history endpoint.** Nine endpoints exist — devices, status, commands,
  scenes ×2, webhook ×4 — and status returns the current values only. History
  can only be recovered through the app's CSV export.
- **A hub is required.** Meters speak Bluetooth; the cloud API sees them only
  through a hub with Cloud Services enabled.
- **Device types are not a stable interface.** The documentation lacks status
  examples for most models, Japanese product names do not match API type strings
  (デイリーステーション is `Home Climate Panel`), and webhook type strings are a
  separate namespace. Hence the refusal to switch on `deviceType`.
- **`Meter`'s battery is quantized to four steps** while other models report a
  real percentage — a documented device behaviour, visible as a staircase in any
  battery chart.
- **Export timestamps carry no time zone**, so an import assumes local time.
- Placing the SQLite file in a synced folder assumes a single writer.

---

## Discussion Log

- The initial requirement was "temperature, humidity and CO2 in the menu bar,
  collected periodically". Research established the auth scheme, the endpoints
  and the daily quota before any code was written.
- The user asked that the collected set and the displayed set be chosen
  separately — collect many, show a few — which produced the three-layer model
  above and the device × metric granularity of the display choice.
- The user also asked that missing history be filled in automatically where the
  source allows it. Investigation showed the API cannot serve history at all;
  the app's CSV export can, so `import` was designed as an idempotent merge and
  `gaps` was added to say what to export. This was reported back before the plan
  was approved rather than promised and quietly dropped.
- Cloning the official API documentation resolved the remaining unknowns (the
  `CO2` field name, `MeterPro(CO2)`, Celsius-always, the quantized `Meter`
  battery, the separate webhook type namespace).
- Real exports of five devices then fixed the CSV format, and the whole import
  path was verified against them: 15,519 readings merged, re-import inserted 0.
