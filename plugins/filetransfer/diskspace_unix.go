//go:build !windows

package filetransfer

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// checkDiskSpace verifies that the receive directory has enough free space.
func (ts *TransferService) checkDiskSpace(needed int64) error {
	return checkDiskSpaceAt(ts.receiveDir, needed)
}

// checkDiskSpaceAt verifies that the given directory has enough free space.
func checkDiskSpaceAt(dir string, needed int64) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return fmt.Errorf("statfs: %w", err)
	}
	available := int64(stat.Bavail) * int64(stat.Bsize)
	// Require at least 10% headroom above needed.
	required := needed + needed/10
	if available < required {
		return fmt.Errorf("insufficient disk space: need %d bytes, have %d", required, available)
	}
	return nil
}
