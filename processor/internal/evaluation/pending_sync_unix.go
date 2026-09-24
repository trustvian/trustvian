//go:build !windows

package evaluation

import (
	"fmt"
	"os"
)

// syncDirectory makes a directory's own entries durable.
//
// fsync on a file commits that file's contents; the entry naming it lives in
// the parent directory and is committed separately. Without this, a rename
// or an unlink can be lost by a host crash that the file's own fsync did
// nothing to protect against — which for this journal means a record in the
// run with no note that it was ever in flight.
func syncDirectory(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("opening %s to sync it: %w", dir, err)
	}
	if err := handle.Sync(); err != nil {
		handle.Close()
		return fmt.Errorf("syncing %s: %w", dir, err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("closing %s after syncing it: %w", dir, err)
	}
	return nil
}
