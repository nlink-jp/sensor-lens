//go:build unix

package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCollectorLockIsExclusive(t *testing.T) {
	db := filepath.Join(t.TempDir(), "sensors.db")

	lock, err := AcquireCollectorLock(db)
	if err != nil {
		t.Fatalf("AcquireCollectorLock() error = %v", err)
	}
	t.Cleanup(func() { lock.Release() })

	// flock is per-open-file-description, so a second Acquire in this process
	// exercises the same path a second process would take.
	second, err := AcquireCollectorLock(db)
	if !errors.Is(err, ErrLocked) {
		second.Release()
		t.Fatalf("second AcquireCollectorLock() error = %v, want ErrLocked", err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	// Once released the claim is available again.
	third, err := AcquireCollectorLock(db)
	if err != nil {
		t.Fatalf("AcquireCollectorLock() after release error = %v", err)
	}
	third.Release()
}

func TestCollectorLockIsPerDatabase(t *testing.T) {
	dir := t.TempDir()

	// Two daemons on two databases is a legitimate configuration.
	a, err := AcquireCollectorLock(filepath.Join(dir, "a.db"))
	if err != nil {
		t.Fatalf("lock a: %v", err)
	}
	defer a.Release()

	b, err := AcquireCollectorLock(filepath.Join(dir, "b.db"))
	if err != nil {
		t.Fatalf("lock b: %v", err)
	}
	defer b.Release()
}

func TestCollectorLockSurvivesAHolderThatDies(t *testing.T) {
	// flock is released by the kernel when the holder exits, so a crashed
	// daemon must not leave the next one locked out. A PID file would.
	db := filepath.Join(t.TempDir(), "sensors.db")

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperHoldsLock")
	cmd.Env = append(os.Environ(), "SENSOR_LENS_LOCK_HELPER="+db)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper failed: %v: %s", err, out)
	}

	lock, err := AcquireCollectorLock(db)
	if err != nil {
		t.Fatalf("AcquireCollectorLock() after the holder exited = %v, want success", err)
	}
	lock.Release()
}

// TestHelperHoldsLock is not a test: it is the child process above, taking the
// lock and exiting without releasing it.
func TestHelperHoldsLock(t *testing.T) {
	db := os.Getenv("SENSOR_LENS_LOCK_HELPER")
	if db == "" {
		t.Skip("helper process only")
	}
	if _, err := AcquireCollectorLock(db); err != nil {
		t.Fatalf("helper could not take the lock: %v", err)
	}
	// Deliberately no Release: the process exiting is the point.
}
