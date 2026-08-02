# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-08-02

First release.

### Added

- SwitchBot Open API v1.1 client: HMAC-SHA256 request signing, device listing
  and device status, with structured errors that distinguish a transient
  device/hub outage from a configuration problem. Read-only — sending commands
  is deliberately not implemented, so the tool cannot actuate a lock, a curtain
  or a plug even though the token would allow it.
- Shape-driven reading extraction: every numeric or boolean field in a status
  body becomes a metric, with a canonical-name table for the well-known ones
  (`temperature_c`, `humidity_pct`, `co2_ppm`, `battery_pct`, `light_level`).
  New device models and firmware fields are collected without a code change.
- Local SQLite store in long format — one row per (device, metric, timestamp) —
  with owner-only file modes.
- Resident polling daemon with durable daily-call accounting, a configurable
  budget below the account's 10,000/day limit, and exponential backoff.
  `doctor` and `install` refuse a schedule that would exceed the budget, because
  going over surfaces as an authentication failure rather than a quota error.
- Collection ownership: `daemon` takes an exclusive lock per database, and
  `now --if-stale` lets a front end collect on its own timer while costing
  nothing when a daemon is already doing it. `status` reports `collecting` from
  data freshness rather than from whether the LaunchAgent is loaded, and says
  when the daemon points at a binary that no longer exists.
- `import`: merges a CSV exported from the SwitchBot app, which is the only way
  to recover history the API cannot serve. Idempotent — re-importing an
  overlapping window adds only genuinely missing readings.
- `gaps`: reports windows with no readings, so it is clear what to export from
  the app and import back.
- Commands: `devices`, `now`, `daemon`, `history`, `report`, `gaps`, `import`,
  `export`, `status`, `doctor`, `install`, `uninstall`, `prune`, `version`.
- launchd LaunchAgent integration for login-time collection on macOS.
- Collect set (`[polling] devices`) kept separate from what any front end
  displays, so a whole house can be collected while two readings are shown.
- Configuration from `config.toml`, searched in both `~/.config` and macOS's
  Application Support; `doctor` prints which file it read and where it looked.

[0.1.0]: https://github.com/nlink-jp/sensor-lens/releases/tag/v0.1.0
