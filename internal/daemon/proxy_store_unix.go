//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

// openFlagNoFollow prevents following symlinks on open (SEC-4).
const openFlagNoFollow = syscall.O_NOFOLLOW

// isELOOP checks for syscall.ELOOP in the error chain (O_NOFOLLOW on a symlink).
func isELOOP(err error) bool {
	for err != nil {
		if pe, ok := err.(*os.PathError); ok {
			if pe.Err == syscall.ELOOP {
				return true
			}
			err = pe.Err
			continue
		}
		break
	}
	return false
}
