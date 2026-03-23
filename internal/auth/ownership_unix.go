//go:build !windows

package auth

import (
	"os"
	"syscall"
)

// platformOwnership extracts UID/GID from file info on Unix systems.
func platformOwnership(info os.FileInfo) *fileOwnership {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return &fileOwnership{uid: int(stat.Uid), gid: int(stat.Gid)}
}
