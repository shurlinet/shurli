package sdk

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	ma "github.com/multiformats/go-multiaddr"
)

// bulkDialMu protects per-peer peerstore snapshot-restore in dialTCPLAN.
// Prevents R11-F1/F12 races between concurrent OpenBulkStream calls and
// mDNS dialWithBackoffClear on the same peer.
var bulkDialMu sync.Map // peer.ID -> *sync.Mutex

func peerDialLock(pid peer.ID) *sync.Mutex {
	v, _ := bulkDialMu.LoadOrStore(pid, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// OpenBulkStream opens a stream preferring a TCP LAN connection when
// network.lan_transport is "tcp" and the peer has a verified LAN connection.
// Falls back to HedgedOpenStream if TCP is unavailable or dial fails.
func OpenBulkStream(ctx context.Context, n *Network, peerID peer.ID, serviceName string) (network.Stream, error) {
	// R11-F14: read config on every call (hot-reload safe).
	if n.config != nil && n.config.Network.LANTransport == "tcp" && n.HasVerifiedLANConn(peerID) {
		// R11-F4: skip if peer has no live connections (just disconnected).
		conns := n.host.Network().ConnsToPeer(peerID)
		if len(conns) > 0 {
			// R15-F7: check if a TCP LAN conn already exists.
			for _, c := range conns {
				if IsTCPConn(c) && !c.IsClosed() {
					s, err := n.OpenPluginStreamOnConn(ctx, peerID, serviceName, c)
					if err == nil {
						slog.Debug("bulk-stream: reusing existing TCP LAN connection",
							"peer", peerID.String()[:16])
						return s, nil
					}
					// Existing TCP conn failed (half-closed?), fall through to dial.
				}
			}

			// No usable TCP conn exists. Dial one.
			tcpConn, dialErr := dialTCPLAN(ctx, n, peerID)
			if dialErr == nil {
				s, err := n.OpenPluginStreamOnConn(ctx, peerID, serviceName, tcpConn)
				if err == nil {
					slog.Info("bulk-stream: opened stream on new TCP LAN connection",
						"peer", peerID.String()[:16])
					return s, nil
				}
				slog.Debug("bulk-stream: stream open on TCP conn failed, falling back",
					"peer", peerID.String()[:16], "error", err)
			} else {
				slog.Debug("bulk-stream: TCP LAN dial failed, falling back",
					"peer", peerID.String()[:16], "error", dialErr)
			}
		}
	}

	// Default/fallback: hedged open across all connection groups.
	return HedgedOpenStream(ctx, n, peerID, serviceName)
}

// BulkStreamOpener returns a stream opener function compatible with SendOptions.StreamOpener.
// It captures the TCP conn from the first successful OpenBulkStream call and pins
// subsequent streams to that conn. Falls back to HedgedOpenStream on conn death (R15-F3).
func BulkStreamOpener(ctx context.Context, n *Network, peerID peer.ID, serviceName string) func() (network.Stream, error) {
	var mu sync.Mutex
	var pinnedConn network.Conn

	return func() (network.Stream, error) {
		mu.Lock()
		conn := pinnedConn
		mu.Unlock()

		// R11-F5 + R15-F3: check if captured conn is still alive.
		if conn != nil {
			if conn.IsClosed() {
				// Conn dead. Reset so next call tries fresh TCP dial.
				mu.Lock()
				pinnedConn = nil
				mu.Unlock()
			} else {
				s, err := n.OpenPluginStreamOnConn(ctx, peerID, serviceName, conn)
				if err == nil {
					return s, nil
				}
				// Stream open failed on live conn (rcmgr, policy, etc.).
				// Re-check: if conn died during stream open, reset for fresh dial.
				if conn.IsClosed() {
					mu.Lock()
					pinnedConn = nil
					mu.Unlock()
				}
				// If conn is still alive, fall through to OpenBulkStream which
				// will find this TCP conn in ConnsToPeer and reuse it (R15-F7).
			}
		}

		// Full OpenBulkStream path (may dial TCP or fall back to hedged).
		s, err := OpenBulkStream(ctx, n, peerID, serviceName)
		if err != nil {
			return nil, err
		}

		// Capture the conn for pinning subsequent worker streams.
		mu.Lock()
		pinnedConn = s.Conn()
		mu.Unlock()

		return s, nil
	}
}

// dialTCPLAN dials a TCP connection to a verified-LAN peer using peerstore
// snapshot-restore. Same proven pattern as mDNS dialWithBackoffClear.
func dialTCPLAN(ctx context.Context, n *Network, peerID peer.ID) (network.Conn, error) {
	// Filter peerstore addrs to TCP LAN only.
	allAddrs := n.host.Peerstore().Addrs(peerID)
	tcpLAN := FilterTCPAddrs(FilterLANAddrs(allAddrs))
	if len(tcpLAN) == 0 {
		return nil, fmt.Errorf("no TCP LAN addresses for peer")
	}

	// R11-F1/F12: per-peer mutex prevents ClearAddrs races.
	mu := peerDialLock(peerID)
	mu.Lock()
	defer mu.Unlock()

	// R14-I2: clear swarm backoff (previous TCP failures may be cached).
	if sw, ok := n.host.Network().(*swarm.Swarm); ok {
		sw.Backoff().Clear(peerID)
	}

	// Snapshot and restrict peerstore to TCP LAN addrs only.
	savedAddrs := n.host.Peerstore().Addrs(peerID)
	n.host.Peerstore().ClearAddrs(peerID)
	n.host.Peerstore().AddAddrs(peerID, tcpLAN, peerstore.TempAddrTTL)

	// R11-F6: short timeout for LAN TCP dial (3s).
	dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	// R14-I1: ForceDirectDial prevents short-circuit to existing relay.
	dialCtx = network.WithForceDirectDial(dialCtx, "tcp-for-lan")

	conn, dialErr := n.host.Network().DialPeer(dialCtx, peerID)
	cancel()

	if dialErr != nil {
		// R11-F10: restore with RecentlyConnectedAddrTTL on failure (conservative).
		n.host.Peerstore().AddAddrs(peerID, savedAddrs, peerstore.RecentlyConnectedAddrTTL)
	} else {
		// R11-F10: restore with ConnectedAddrTTL on success (peer IS connected).
		n.host.Peerstore().AddAddrs(peerID, savedAddrs, peerstore.ConnectedAddrTTL)
	}

	return conn, dialErr
}

// IsTCPConn returns true if the connection uses TCP transport (not QUIC).
// TCP multiaddrs have /tcp/ without /udp/. QUIC has /udp/.../quic-v1.
// Uses multiaddr component walking for correctness (R15-F8).
// Exported for use by the file-transfer plugin's transport detection.
func IsTCPConn(conn network.Conn) bool {
	hasTCP := false
	hasUDP := false
	ma.ForEach(conn.RemoteMultiaddr(), func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_TCP:
			hasTCP = true
		case ma.P_UDP:
			hasUDP = true
		}
		return true
	})
	return hasTCP && !hasUDP
}
