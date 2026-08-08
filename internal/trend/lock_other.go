//go:build !unix

package trend

import (
	"errors"
	"os"
)

// lockExclusive has no implementation on platforms without flock(2).
//
// It refuses rather than returning a no-op lock. A no-op would let two
// concurrent writers interleave their bytes and produce a line that parses as
// neither run — silent corruption of the exact history this store exists to
// keep, on the exact platform nobody tested. R-CLI-16 requires the lock, so a
// build that cannot take one says so. TortureU's released binaries are
// linux and darwin (.goreleaser.yaml), both of which use lock_unix.go.
func lockExclusive(_ *os.File) (func(), error) {
	return nil, errors.New("no advisory file lock on this platform; " +
		"appending without one could interleave two runs into one unreadable line (R-CLI-16)")
}
