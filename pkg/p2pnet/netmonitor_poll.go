package p2pnet

import (
	"context"
	"time"
)

// pollNetworkChanges is the fallback for platforms without event-driven
// network monitoring. It polls every 30 seconds - adequate since network
// changes (WiFi switch, tethering) are infrequent.
func pollNetworkChanges(ctx context.Context, ch chan<- struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}
