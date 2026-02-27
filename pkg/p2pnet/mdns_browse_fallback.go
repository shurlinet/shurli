//go:build !cgo || (!darwin && !linux)

package p2pnet

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/libp2p/zeroconf/v2"
)

// nativeBrowse falls back to zeroconf when native DNS-SD APIs are not
// available (Windows, CGo disabled, or non-macOS/Linux). Uses raw
// multicast sockets which may not work when a system mDNS daemon
// (mDNSResponder, avahi) is running.
func nativeBrowse(ctx context.Context, service, domain string, entries chan<- []string) error {
	slog.Debug("mdns: fallback browse via zeroconf", "service", service, "domain", domain)

	// zeroconf expects domain without trailing dot.
	domain = strings.TrimSuffix(domain, ".")

	zcEntries := make(chan *zeroconf.ServiceEntry, 100)

	// Convert zeroconf entries to plain TXT record slices.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for entry := range zcEntries {
			select {
			case entries <- entry.Text:
			case <-ctx.Done():
				// Drain to unblock zeroconf's sender.
				for range zcEntries {
				}
				return
			}
		}
	}()

	// zeroconf.Browse closes zcEntries when done.
	err := zeroconf.Browse(ctx, service, domain, zcEntries)
	wg.Wait()
	return err
}
