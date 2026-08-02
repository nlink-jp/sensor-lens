# CLAUDE.md — sensor-lens

Organization rules: https://github.com/nlink-jp/.github/blob/main/CONVENTIONS.md
Workspace rules also apply (see the parent `nlink-jp/CLAUDE.md`).

## What this is

A CLI that collects temperature, humidity and CO2 from SwitchBot sensors via the
Open API v1.1 and keeps the history in local SQLite. Two-layer, like
`active-lens` / `active-lens-gui`:

- **CLI (`sensor-lens`, this repo)** — auth, polling, storage, aggregation.
- **GUI (menu bar, Swift, separate repo)** — thin front over `--json` (Phase 2).

## Build & test

- **`make build`** → `dist/sensor-lens` (never `go build` directly).
- `make test` / `go test ./...` — must pass before committing.
- `make vet` — darwin + linux + windows.
- **No cgo.** SQLite is pure Go; keep it that way so the matrix cross-compiles.

## Non-negotiable design points

- **Read-only.** Never implement `POST /commands`. The tool must not be able to
  actuate a lock, a curtain or a plug, and that is a structural guarantee, not a
  policy.
- **No `deviceType` switches.** Readings are extracted by shape in
  `core/metrics`; only naming is table-driven. New models must work untouched.
- **Long format storage.** `(device_id, metric, ts)` is the identity of a
  reading. It is what makes CSV backfill idempotent. Never denormalize into a
  column per metric.
- **Quota is a first-class concern.** Over 10,000 calls/day the API answers
  "Unauthorized", which reads exactly like a bad token. Spent calls are counted
  in the database, `doctor`/`install` refuse an unaffordable schedule, and the
  daemon parks rather than crossing the budget.
- **Credentials only in the 0600 config file.** Not in the LaunchAgent plist
  (world-readable), not in a command line, not in the database.
- **Pure core.** `metrics`, `aggregate`, `importer`, `switchbot/sign` and
  `cmd/format` are pure functions and must stay testable without a network, a
  clock or a database.

## Docs

`README.md` and `README.ja.md` are kept in sync in the same commit as any
behaviour change. `AGENTS.md` carries the layout and the hard-won API/CSV facts;
`docs/{en,ja}` carry the RFP and any ADRs.
