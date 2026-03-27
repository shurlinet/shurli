//go:build !darwin && !linux

package sdk

import "context"

// watchNetworkChanges falls back to polling on platforms without
// native event-driven network monitoring.
func watchNetworkChanges(ctx context.Context, ch chan<- struct{}) {
	pollNetworkChanges(ctx, ch)
}
