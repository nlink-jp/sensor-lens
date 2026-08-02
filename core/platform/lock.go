package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// Lock is a held single-instance claim.
type Lock struct {
	f *os.File
}

// Release drops the claim.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unlockFile(l.f)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}

// ErrLocked is returned when another process already holds the claim.
var ErrLocked = fmt.Errorf("another sensor-lens collector is already running")

// AcquireCollectorLock claims the right to be the process polling the API.
//
// Two collectors would not corrupt anything — SQLite handles the concurrent
// writes — but they would quietly spend twice the API quota, and the account's
// answer to overspending is an "Unauthorized" that looks like a bad token. The
// lock makes that impossible rather than merely unlikely.
//
// The lock file sits beside the database, so one lock covers one database: two
// daemons on different databases are a legitimate configuration.
func AcquireCollectorLock(dbPath string) (*Lock, error) {
	path := dbPath + ".lock"
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := EnsureDir(dir); err != nil {
			return nil, err
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, FileMode)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	// Record the holder so a stale-looking lock can be explained.
	f.Truncate(0)
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return &Lock{f: f}, nil
}
