//go:build !darwin && !linux

package p2pnet

import "context"

// watchNetworkChanges falls back to polling on platforms without
// native event-driven network monitoring.
func watchNetworkChanges(ctx context.Context, ch chan<- struct{}) {
	pollNetworkChanges(ctx, ch)
}
