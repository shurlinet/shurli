//go:build windows

package platform

// RestrictiveUmask is a no-op on Windows (no umask concept).
func RestrictiveUmask() func() {
	return func() {}
}
