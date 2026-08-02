//go:build !unix

package platform

import "os"

// Windows has no flock. The daemon is a macOS/Linux affair — scheduling is
// launchd — so rather than half-implement an exclusion primitive that would be
// trusted more than it deserves, this platform simply does not enforce one.
// Running two collectors there double-spends the API quota; that is documented
// rather than silently guarded.
func lockFile(*os.File) error { return nil }

func unlockFile(*os.File) error { return nil }
