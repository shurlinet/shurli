//go:build !darwin && !linux

package sdk

// defaultGateway is a no-op on unsupported platforms. Gateway-based
// network change detection is disabled; the monitor still detects
// changes via global IP and tunnel interface diffs.
func defaultGateway() string {
	return ""
}
