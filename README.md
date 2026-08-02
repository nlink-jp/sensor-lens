# sensor-lens

Collect temperature, humidity and CO2 from your SwitchBot sensors, keep the
history locally, and read it back without opening the app.

`sensor-lens` is the CLI engine: it authenticates against the SwitchBot Open API
v1.1, polls your devices on a schedule, and stores every reading in a local
SQLite database. A menu-bar GUI (`sensor-lens-gui`, separate repository) is a
thin front end over `--json`.

It is **read-only**. It never sends a command to a device, so it cannot turn
anything on, off, open or locked.

## Requirements

- A **SwitchBot Hub** (Hub Mini / Hub 2 / Hub 3). Meters talk Bluetooth; the
  cloud API only sees them through a hub, with Cloud Services enabled in the app.
- A token and secret from the app: Profile → Preferences → tap **App Version**
  ten times → Developer Options → Get Token (app v6.14 or later).
- macOS or Linux. Daemon auto-start is wired for macOS (launchd); elsewhere run
  `sensor-lens daemon` under your own supervisor.

## Install

```bash
make build      # -> dist/sensor-lens
```

Then create `config.toml` (see `config.example.toml`) in:

- macOS: `~/Library/Application Support/sensor-lens/`
- other: `~/.config/sensor-lens/`

```toml
[switchbot]
token  = "..."
secret = "..."
```

Keep it mode 0600 — `sensor-lens doctor` will tell you if it is not.

```bash
sensor-lens doctor        # check credentials, quota, connectivity
sensor-lens devices       # discover what is on the account
sensor-lens now           # read the sensors right now
sensor-lens install       # start collecting at login (macOS)
```

## What it collects

The status body is read field by field rather than per device model: every
numeric field becomes a metric, and only the naming is table-driven. A new
SwitchBot model, or a firmware update that adds a field, is collected without a
code change.

| API field    | Stored as       | Devices |
|--------------|-----------------|---------|
| `temperature`| `temperature_c` | all meters, Hub 2 / Hub 3, Daily Station |
| `humidity`   | `humidity_pct`  | same |
| `CO2`        | `co2_ppm`       | **Meter Pro CO2 only** |
| `battery`    | `battery_pct`   | battery-powered meters |
| `lightLevel` | `light_level`   | Hub 2 (1–20), Hub 3 (1–10) |
| anything else numeric | its API name | whatever reports it |

A device is collected automatically only if it reports **temperature or CO2** —
the measurements that describe the room rather than the device. Humidity alone
is not enough: a humidifier reports a humidity field too, next to its own mode
and child lock, and collecting it would spend quota on noise. Anything left out
can still be collected by naming it in `[polling] devices`.

`sensor-lens devices --raw` prints the untouched API response when you want to
see exactly what a device sent, and `devices --reclassify` re-decides what is
collected (one call per device).

## Commands

| Command | What it does |
|---------|--------------|
| `devices [--json] [--raw] [--refresh] [--reclassify]` | List the devices and which are collected |
| `now [--json] [--devices ids] [--stored]` | Read the sensors now (or show the last stored values) |
| `daemon` | Run the resident collector |
| `history --device D --metric M [--since --until --bucket 5m] [--json]` | One metric over time |
| `report [--since --until] [--json]` | min / avg / max per metric |
| `gaps [--since --until --factor N] [--json]` | Windows with no readings |
| `import --file F [--device D] [--dry-run] [--all-columns] [--tz Z]` | Merge a CSV exported from the app |
| `export [--format csv\|json] [--since --until]` | Write stored readings out |
| `status [--json]` | Daemon state, DB path, calls spent today |
| `doctor` | Diagnose config, credentials, quota, connectivity |
| `install` / `uninstall` | Register / remove the launchd LaunchAgent |
| `prune [--keep-days N \| --device D] [--dry-run]` | Delete old readings, or one device's |
| `version` | Print the version |

Time flags accept `2026-08-01`, `2026-08-01 15:04`, or an offset: `-3h`, `-7d`.

## Collect broadly, display narrowly

`[polling] devices` is the **collect set** — what gets polled and stored. Leave
it empty to collect every device that reports an ambient measurement; a plug
that only reports voltage is left out, and so is a bot.

What a menu bar shows is a separate, smaller choice, and it belongs to the GUI.
The CLI's equivalent is `now --devices` — collect eight sensors, show two.

## Who does the collecting

Readings only exist while something is polling. Three arrangements work, and
they are mutually exclusive by construction rather than by convention:

| Arrangement | How | Trade-off |
|---|---|---|
| **Daemon** | `sensor-lens install` | Collects whether or not you are at the machine. Spends the full daily budget. |
| **Front end** | a menu-bar app ticking `sensor-lens now --if-stale` | Collects only while the app runs, so roughly halves the spend. Gaps whenever it is closed. |
| **Neither** | run `now` by hand | Nothing accumulates. |

`now --if-stale` is what makes this safe to mix: it polls only if the newest
stored reading has aged past the interval. If a daemon is already collecting,
the readings are fresh and the tick costs **zero API calls**; if nothing is
running, that tick becomes the collector. No coordination protocol, no
configuration to keep in sync.

Two daemons on one database would silently double the API spend, so `daemon`
takes an exclusive lock and the second one refuses to start. The lock is a
`flock`, released by the kernel, so a crash cannot leave a stale one behind.

`status` reports `collecting` from how recent the newest reading is, never from
whether the LaunchAgent is loaded — an indicator that only believed in launchd
would call a menu-bar app or a hand-started daemon "not collecting" while data
was visibly arriving.

## Backfilling history

**The SwitchBot API has no history endpoint.** It returns the current status and
nothing else, so a stretch when the daemon was not running cannot be recovered
from the API at any price.

The data does exist: the meters hold weeks of it and the app holds years. The
route back in is the app's export.

```bash
sensor-lens gaps                     # what is missing, and how much
# SwitchBot app -> the device -> Export Data -> pick the window -> share the CSV
sensor-lens import --file "CO2センサー (Room 1)_data.csv" --dry-run
sensor-lens import --file "CO2センサー (Room 1)_data.csv"
```

Importing is **idempotent**: a reading is identified by (device, metric,
timestamp), so overlapping exports add only what is genuinely missing. Re-import
the same file as often as you like.

Two things to know about exports:

- The app samples about once a minute, far denser than the default 5-minute
  polling. Imported windows are simply more detailed.
- The timestamps carry no time zone. They are read as this machine's local time;
  use `--tz` if the export came from elsewhere.
- The app also computes dew point, VPD and absolute humidity. These are **not**
  imported by default: the API never reports them, so they would produce series
  that exist only inside imported windows. `--all-columns` imports them anyway.

## Staying inside the quota

The account allows **10,000 API calls per day**, and exceeding it does not fail
loudly — the API starts answering "Unauthorized", exactly like a bad token.

sensor-lens counts what it spends, in the database, so a restart does not forget:

- The default 5-minute cadence costs `devices × 288` calls a day. Six sensors is
  about 1,700 — well inside the budget.
- `doctor` and `install` refuse a schedule that would exceed `api.daily_budget`
  (default 8000) and tell you the interval that would fit.
- The daemon stops polling for the rest of the day rather than crossing the
  budget, and backs off exponentially if the API pushes back.

## Storage

One row per (device, metric, timestamp), about 65 bytes each. Five sensors at
the default cadence come to roughly 100 MB a year; `prune` and
`storage.retention_days` (default 400) keep it bounded.

The database and config file are owner-only (0600): a minute-by-minute
temperature log is a record of when someone is home.

## Development

```bash
make build      # -> dist/ (never `go build` directly)
make test       # go test ./...
make vet        # darwin + linux + windows
```

No cgo: SQLite is pure Go (`modernc.org/sqlite`), so the whole matrix
cross-compiles from a Mac. See `AGENTS.md` for the layout and the gotchas, and
`docs/en/sensor-lens-rfp.md` for the design rationale.

## License

MIT — see `LICENSE`.
