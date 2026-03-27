package sdk

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// ResolveDNSSeeds queries _dnsaddr.<domain> TXT records for bootstrap peer
// multiaddrs. This follows the dnsaddr multiaddr convention used by IPFS
// bootstrap nodes.
//
// TXT record format: dnsaddr=/ip4/203.0.113.50/tcp/7777/p2p/12D3KooW...
//
// Returns parsed peer.AddrInfo slice. DNS failures are logged but not fatal;
// the caller falls through to the next bootstrap layer.
func ResolveDNSSeeds(ctx context.Context, domain string) []peer.AddrInfo {
	if domain == "" {
		return nil
	}

	lookupDomain := "_dnsaddr." + domain

	// Use a short timeout so DNS failures don't block startup
	resolveCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resolver := &net.Resolver{}
	records, err := resolver.LookupTXT(resolveCtx, lookupDomain)
	if err != nil {
		slog.Debug("dns seed lookup failed", "domain", lookupDomain, "error", err)
		return nil
	}

	return parseDNSAddrRecords(records)
}

// parseDNSAddrRecords extracts peer.AddrInfo from TXT records.
// Each record should have the format: dnsaddr=<multiaddr>
// Invalid records are logged and skipped.
func parseDNSAddrRecords(records []string) []peer.AddrInfo {
	seen := make(map[peer.ID]int) // peer ID -> index in result
	var result []peer.AddrInfo

	for _, record := range records {
		record = strings.TrimSpace(record)

		// Extract multiaddr from "dnsaddr=<multiaddr>" format
		if !strings.HasPrefix(record, "dnsaddr=") {
			slog.Debug("dns seed: skipping non-dnsaddr record", "record", record)
			continue
		}
		addrStr := strings.TrimPrefix(record, "dnsaddr=")

		maddr, err := ma.NewMultiaddr(addrStr)
		if err != nil {
			slog.Debug("dns seed: invalid multiaddr", "addr", addrStr, "error", err)
			continue
		}

		ai, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			slog.Debug("dns seed: cannot extract peer info", "addr", addrStr, "error", err)
			continue
		}

		// Merge addresses for same peer ID
		if idx, ok := seen[ai.ID]; ok {
			result[idx].Addrs = append(result[idx].Addrs, ai.Addrs...)
		} else {
			seen[ai.ID] = len(result)
			result = append(result, *ai)
		}
	}

	if len(result) > 0 {
		slog.Info("dns seed: resolved peers", "domain", fmt.Sprintf("_dnsaddr.%s", "..."), "count", len(result))
	}

	return result
}
