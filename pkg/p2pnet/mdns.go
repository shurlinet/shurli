package p2pnet

import (
	"context"
	"log/slog"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pmdns "github.com/libp2p/go-libp2p/p2p/discovery/mdns"
)

// MDNSServiceName is the DNS-SD service type used for LAN discovery.
// Fixed for all Shurli nodes. Network isolation is handled by the
// ConnectionGater (authorized_keys), not by mDNS service names.
const MDNSServiceName = "_shurli._udp"

// MDNSDiscovery wraps libp2p mDNS for local network peer discovery.
// Discovered peers have their addresses added to the peerstore;
// connection attempts go through the normal ConnectionGater.
type MDNSDiscovery struct {
	host    host.Host
	service libp2pmdns.Service
	metrics *Metrics
}

// NewMDNSDiscovery creates an mDNS discovery service.
// Metrics is optional (nil-safe).
func NewMDNSDiscovery(h host.Host, m *Metrics) *MDNSDiscovery {
	md := &MDNSDiscovery{
		host:    h,
		metrics: m,
	}
	md.service = libp2pmdns.NewMdnsService(h, MDNSServiceName, md)
	return md
}

// Start begins mDNS advertising and browsing on the local network.
func (md *MDNSDiscovery) Start() error {
	return md.service.Start()
}

// Close stops the mDNS service.
func (md *MDNSDiscovery) Close() error {
	return md.service.Close()
}

// HandlePeerFound implements the libp2p mdns.Notifee interface.
// Called when a peer is discovered via mDNS on the local network.
func (md *MDNSDiscovery) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == md.host.ID() {
		return
	}

	short := pi.ID.String()
	if len(short) > 16 {
		short = short[:16] + "..."
	}

	slog.Info("mdns: peer discovered on LAN", "peer", short, "addrs", len(pi.Addrs))

	if md.metrics != nil && md.metrics.MDNSDiscoveredTotal != nil {
		md.metrics.MDNSDiscoveredTotal.WithLabelValues("discovered").Inc()
	}

	// Add addresses with a 10-minute TTL. LAN addresses are ephemeral;
	// they refresh on the next mDNS cycle.
	md.host.Peerstore().AddAddrs(pi.ID, pi.Addrs, 10*time.Minute)

	// Attempt connection in a goroutine. Goes through ConnectionGater;
	// unauthorized peers are rejected at the crypto handshake.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := md.host.Connect(ctx, pi); err != nil {
			slog.Debug("mdns: connect failed", "peer", short, "error", err)
			return
		}

		slog.Info("mdns: connected to LAN peer", "peer", short)
		if md.metrics != nil && md.metrics.MDNSDiscoveredTotal != nil {
			md.metrics.MDNSDiscoveredTotal.WithLabelValues("connected").Inc()
		}
	}()
}
