// Package store is the durable record of what the sensors reported.
//
// Readings are kept in long format — one row per (device, metric, timestamp) —
// rather than a column per measurement. That is what lets a new device type or
// a firmware field appear without a migration, and it is what makes backfill
// idempotent: the primary key is the identity of a reading, so re-importing an
// overlapping CSV inserts only what is genuinely missing.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so the CLI cross-compiles

	"github.com/nlink-jp/sensor-lens/core/platform"
)

// schemaVersion is written to PRAGMA user_version so a future migration can
// tell what it is looking at.
const schemaVersion = 1

// Device is a physical device sensor-lens knows about.
type Device struct {
	DeviceID    string `json:"device_id"`
	Name        string `json:"name"`
	DeviceType  string `json:"device_type"`
	HubDeviceID string `json:"hub_device_id,omitempty"`
	Version     string `json:"version,omitempty"`
	// Enabled is whether the daemon polls this device (the collect set).
	Enabled   bool  `json:"enabled"`
	FirstSeen int64 `json:"first_seen"`
	LastSeen  int64 `json:"last_seen"`
}

// Reading is one stored measurement.
type Reading struct {
	DeviceID string  `json:"device_id"`
	Metric   string  `json:"metric"`
	TS       int64   `json:"ts"`
	Value    float64 `json:"value"`
}

// Store owns the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path. The file and its
// directory are owner-only: the readings say when someone is home.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := platform.EnsureDir(dir); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// One writer at a time; the daemon and a foreground command can both hold
	// the DB open, and WAL plus a busy timeout keeps them out of each other's way.
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	// Chmod after creation: SQLite makes the file with the process umask.
	if err := os.Chmod(path, platform.FileMode); err != nil && !os.IsNotExist(err) {
		db.Close()
		return nil, fmt.Errorf("tighten %s: %w", path, err)
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS devices(
	device_id     TEXT PRIMARY KEY,
	name          TEXT NOT NULL DEFAULT '',
	device_type   TEXT NOT NULL DEFAULT '',
	hub_device_id TEXT NOT NULL DEFAULT '',
	version       TEXT NOT NULL DEFAULT '',
	enabled       INTEGER NOT NULL DEFAULT 1,
	first_seen    INTEGER NOT NULL,
	last_seen     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS readings(
	device_id TEXT NOT NULL,
	metric    TEXT NOT NULL,
	ts        INTEGER NOT NULL,
	value     REAL NOT NULL,
	PRIMARY KEY(device_id, metric, ts)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS readings_ts ON readings(ts);

CREATE TABLE IF NOT EXISTS api_calls(
	day   TEXT PRIMARY KEY,
	count INTEGER NOT NULL
);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

// UpsertDevices records the devices seen at time now. Known devices keep their
// first_seen and their enabled flag; unseen devices are left alone rather than
// deleted, so history for a meter that is temporarily off the mesh survives.
func (s *Store) UpsertDevices(ctx context.Context, devices []Device, now time.Time) error {
	if len(devices) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO devices(device_id, name, device_type, hub_device_id, version, enabled, first_seen, last_seen)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(device_id) DO UPDATE SET
	name          = excluded.name,
	device_type   = excluded.device_type,
	hub_device_id = excluded.hub_device_id,
	version       = CASE WHEN excluded.version != '' THEN excluded.version ELSE devices.version END,
	last_seen     = excluded.last_seen`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	ts := now.Unix()
	for _, d := range devices {
		if _, err := stmt.ExecContext(ctx, d.DeviceID, d.Name, d.DeviceType, d.HubDeviceID,
			d.Version, boolToInt(d.Enabled), ts, ts); err != nil {
			return fmt.Errorf("upsert device %s: %w", d.DeviceID, err)
		}
	}
	return tx.Commit()
}

// SetEnabled updates the collect set: whether the daemon polls a device.
func (s *Store) SetEnabled(ctx context.Context, deviceID string, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET enabled = ? WHERE device_id = ?`,
		boolToInt(enabled), deviceID)
	return err
}

// Devices returns every known device, ordered by name for stable output.
func (s *Store) Devices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT device_id, name, device_type, hub_device_id, version, enabled, first_seen, last_seen
FROM devices ORDER BY name, device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Device
	for rows.Next() {
		var d Device
		var enabled int
		if err := rows.Scan(&d.DeviceID, &d.Name, &d.DeviceType, &d.HubDeviceID,
			&d.Version, &enabled, &d.FirstSeen, &d.LastSeen); err != nil {
			return nil, err
		}
		d.Enabled = enabled != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

// InsertReadings stores readings, skipping any whose (device, metric, ts) is
// already present, and returns how many rows were genuinely new.
//
// With dryRun the work is done and then rolled back, so the count is exact
// rather than estimated — that is what `import --dry-run` reports.
func (s *Store) InsertReadings(ctx context.Context, readings []Reading, dryRun bool) (int, error) {
	if len(readings) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO readings(device_id, metric, ts, value) VALUES(?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	for _, r := range readings {
		res, err := stmt.ExecContext(ctx, r.DeviceID, r.Metric, r.TS, r.Value)
		if err != nil {
			return inserted, fmt.Errorf("insert reading %s/%s@%d: %w", r.DeviceID, r.Metric, r.TS, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return inserted, err
		}
		inserted += int(n)
	}
	if dryRun {
		return inserted, nil // deferred Rollback discards the writes
	}
	return inserted, tx.Commit()
}

// Latest returns the most recent reading of every (device, metric) pair.
func (s *Store) Latest(ctx context.Context) ([]Reading, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT r.device_id, r.metric, r.ts, r.value
FROM readings r
JOIN (SELECT device_id, metric, MAX(ts) AS ts FROM readings GROUP BY device_id, metric) m
  ON m.device_id = r.device_id AND m.metric = r.metric AND m.ts = r.ts
ORDER BY r.device_id, r.metric`)
	if err != nil {
		return nil, err
	}
	return scanReadings(rows)
}

// History returns one metric's readings for a device over [since, until].
func (s *Store) History(ctx context.Context, deviceID, metric string, since, until int64) ([]Reading, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT device_id, metric, ts, value FROM readings
WHERE device_id = ? AND metric = ? AND ts >= ? AND ts <= ?
ORDER BY ts`, deviceID, metric, since, until)
	if err != nil {
		return nil, err
	}
	return scanReadings(rows)
}

// Range returns every reading in [since, until], ordered for aggregation.
func (s *Store) Range(ctx context.Context, since, until int64) ([]Reading, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT device_id, metric, ts, value FROM readings
WHERE ts >= ? AND ts <= ?
ORDER BY device_id, metric, ts`, since, until)
	if err != nil {
		return nil, err
	}
	return scanReadings(rows)
}

// Metrics returns the distinct metric names recorded for a device.
func (s *Store) Metrics(ctx context.Context, deviceID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT metric FROM readings WHERE device_id = ? ORDER BY metric`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// LastReadingTime returns the newest reading timestamp, or 0 when the database
// is empty.
func (s *Store) LastReadingTime(ctx context.Context) (int64, error) {
	var ts sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(ts) FROM readings`).Scan(&ts); err != nil {
		return 0, err
	}
	return ts.Int64, nil
}

// CountReadings returns the total number of stored readings.
func (s *Store) CountReadings(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM readings`).Scan(&n)
	return n, err
}

// Prune deletes readings older than before, returning how many went.
func (s *Store) Prune(ctx context.Context, before int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM readings WHERE ts < ?`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// AddAPICalls adds n to the running call count for a local day (YYYY-MM-DD)
// and returns the new total.
//
// The count is persisted rather than held in memory because the quota is per
// account per day: a daemon restart at 18:00 must not forget the calls already
// spent, or a restart loop could walk straight through the limit.
func (s *Store) AddAPICalls(ctx context.Context, day string, n int) (int, error) {
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO api_calls(day, count) VALUES(?, ?)
ON CONFLICT(day) DO UPDATE SET count = count + excluded.count`, day, n); err != nil {
		return 0, err
	}
	return s.APICalls(ctx, day)
}

// APICalls returns how many calls have been recorded for a local day.
func (s *Store) APICalls(ctx context.Context, day string) (int, error) {
	var n sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT count FROM api_calls WHERE day = ?`, day).Scan(&n); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	return int(n.Int64), nil
}

func scanReadings(rows *sql.Rows) ([]Reading, error) {
	defer rows.Close()

	var out []Reading
	for rows.Next() {
		var r Reading
		if err := rows.Scan(&r.DeviceID, &r.Metric, &r.TS, &r.Value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
