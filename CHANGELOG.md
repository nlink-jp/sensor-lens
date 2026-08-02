# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- SwitchBot Open API v1.1 client: HMAC-SHA256 request signing, device listing
  and device status, with structured errors that distinguish a transient
  device/hub outage from a configuration problem.
- Shape-driven reading extraction: every numeric or boolean field in a status
  body becomes a metric, with a canonical-name table for the well-known ones
  (`temperature_c`, `humidity_pct`, `co2_ppm`, `battery_pct`, `light_level`).
  New device models and firmware fields are collected without a code change.
- Local SQLite store in long format — one row per (device, metric, timestamp) —
  with owner-only file modes.
- Resident polling daemon with durable daily-call accounting, a configurable
  budget below the account's 10,000/day limit, and exponential backoff.
  `doctor` and `install` refuse a schedule that would exceed the budget.
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
