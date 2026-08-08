//go:build unix

package trend

import (
	"os"
	"syscall"
)

// lockExclusive takes an advisory exclusive lock on the open store and
// returns the release (R-CLI-16). flock(2) is what makes two CI jobs
// appending at the same instant produce two whole lines: the lock is held
// across the single write, so neither can land inside the other's bytes.
//
// It is deliberately blocking (no LOCK_NB): a second writer waiting a few
// microseconds is correct, whereas failing to record a run because another
// job was recording one would lose real history for no reason.
//
// The lock is advisory, so it only orders writers that take it — which is
// every writer in this codebase, because Append is the only way in.
func lockExclusive(f *os.File) (func(), error) {
	fd := int(f.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		return nil, err
	}
	return func() { _ = syscall.Flock(fd, syscall.LOCK_UN) }, nil
}
