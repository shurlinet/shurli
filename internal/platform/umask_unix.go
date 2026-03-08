//go:build !windows

package platform

import "syscall"

// RestrictiveUmask sets umask to 0077 and returns a function to restore the original.
func RestrictiveUmask() func() {
	old := syscall.Umask(0077)
	return func() { syscall.Umask(old) }
}
