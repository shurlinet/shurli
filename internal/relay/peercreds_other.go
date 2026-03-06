//go:build !linux && !darwin

package relay

// getPeerCreds is a no-op on unsupported platforms.
func getPeerCreds(fd uintptr) *peerCreds {
	return nil
}
