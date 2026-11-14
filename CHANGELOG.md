# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- `make vuln` — and so `make package` — scans every platform in `PLATFORMS`,
  with the environment `build-all` builds each with. It scanned the host's
  build alone, so an advisory in a Linux- or Windows-only package could not
  stop a release.

## [0.1.1] - 2026-11-14

### Security

- **Rebuilt with Go 1.27.4, which `go.mod` now requires** (it was `go 1.26`).
  No code of the tool changed. v0.1.0 was built with Go 1.26.5; `govulncheck
  -mode binary` finds 15 standard-library advisories in its darwin-arm64
  binary, in `crypto/tls`, `crypto/x509`, `encoding/asn1`, `net/http`,
  `net/textproto` and `net/url`: GO-2026-5026, GO-2026-5972, GO-2026-6090,
  GO-2026-6218, GO-2026-6603, GO-2026-6605, GO-2026-6607, GO-2026-6608,
  GO-2026-6610, GO-2026-6611, GO-2026-6612, GO-2026-6613, GO-2026-6617,
  GO-2026-6809, GO-2026-6859. All are fixed by Go 1.27.4. Binary mode shows
  that an affected function is linked into the binary, not that the tool calls
  it; each advisory is described at `https://pkg.go.dev/vuln/<ID>`.

### Changed

- `make package` runs `govulncheck ./...` first (`make vuln`) and stops on a
  finding, so a release cannot be built past one.

### Fixed

- **`make verify-release` now fails closed.** Its last block chained unzip, the
  packaged binary's `--version` and `spctl` with `&&` and ended the whole chain
  in `|| true`, so a zip that did not unpack or a binary that did not run exited
  0 and the upload proceeded. Each step is now judged on its own, the packaged
  binary's `--version` must contain the tag being released, and only the
  informational `spctl` line may be ignored. Matches the org template
  (CONVENTIONS.md §Code Signing → Verifying a release).
- **The Linux archives no longer carry macOS file metadata.** macOS `tar` wrote
  each bundled file's extended attributes (`com.apple.provenance`, and a Dropbox
  attribute where the tree is synced) into the `.tar.gz` twice: as AppleDouble
  `._` members, which GNU tar extracts as stray `._<name>` files beside the real
  ones, and as `LIBARCHIVE.xattr.*` / `SCHILY.xattr.*` pax headers, which it
  reports as unknown keywords. `make package` now archives with
  `COPYFILE_DISABLE=1 tar --no-xattrs`; each setting stops one of the two.
  Archives already published still carry them; the files themselves are
  unaffected.

### Internal

- `make verify-release` also judges each Linux archive: no AppleDouble or other
  macOS metadata members — listed with `--options 'tar:!mac-ext'`, because a
  plain macOS listing folds `._` members away — no extended attributes as pax
  headers, and exactly the canonical binary, `README.md` and `LICENSE`, compared
  in the C locale.
- The Linux-archive check in `make verify-release` reads each archive's pax
  headers with Python's `tarfile` instead of grepping the decompressed stream,
  which also matched file text that names the keywords (a bundled CHANGELOG,
  for one).

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
