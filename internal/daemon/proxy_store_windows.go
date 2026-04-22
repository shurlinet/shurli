//go:build windows

package daemon

// openFlagNoFollow is a no-op on Windows; O_NOFOLLOW does not exist.
// Windows symlink handling differs from POSIX and does not use this flag.
const openFlagNoFollow = 0

// isELOOP always returns false on Windows (no ELOOP errno).
func isELOOP(_ error) bool {
	return false
}
