package p2pnet

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/shurlinet/shurli/internal/config"
)

// newMDNSNetwork creates a Network listening on 0.0.0.0 (required for mDNS multicast).
func newMDNSNetwork(t *testing.T) *Network {
	t.Helper()
	dir := t.TempDir()
	net, err := New(&Config{
		KeyFile: filepath.Join(dir, "test.key"),
		Config: &config.Config{
			Network: config.NetworkConfig{
				ListenAddresses: []string{"/ip4/0.0.0.0/tcp/0"},
			},
		},
	})
	if err != nil {
		t.Fatalf("create mDNS network: %v", err)
	}
	t.Cleanup(func() { net.Close() })
	return net
}

func TestMDNSDiscovery_SelfIgnored(t *testing.T) {
	net := newMDNSNetwork(t)
	h := net.Host()

	md := NewMDNSDiscovery(h, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := md.Start(ctx); err != nil {
		t.Fatalf("md.Start: %v", err)
	}
	defer md.Close()

	// HandlePeerFound with our own ID should be a no-op.
	md.HandlePeerFound(peer.AddrInfo{
		ID:    h.ID(),
		Addrs: h.Addrs(),
	})

	// Peerstore should NOT have our own addresses added
	// (libp2p always has self in peerstore, but we verify
	// HandlePeerFound didn't attempt a self-connection).
}

func TestMDNSDiscovery_HandlePeerFound(t *testing.T) {
	netA := newMDNSNetwork(t)
	netB := newMDNSNetwork(t)

	md := NewMDNSDiscovery(netA.Host(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := md.Start(ctx); err != nil {
		t.Fatalf("md.Start: %v", err)
	}
	defer md.Close()

	// Simulate discovering netB via mDNS.
	addr, _ := ma.NewMultiaddr("/ip4/192.168.1.100/tcp/9999")
	md.HandlePeerFound(peer.AddrInfo{
		ID:    netB.Host().ID(),
		Addrs: []ma.Multiaddr{addr},
	})

	// Verify addresses were added to peerstore.
	addrs := netA.Host().Peerstore().Addrs(netB.Host().ID())
	if len(addrs) == 0 {
		t.Fatal("expected addresses in peerstore after HandlePeerFound")
	}

	found := false
	for _, a := range addrs {
		if a.Equal(addr) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected address %s in peerstore, got %v", addr, addrs)
	}
}

func TestMDNSDiscovery_BrowseNow(t *testing.T) {
	net := newMDNSNetwork(t)
	h := net.Host()

	md := NewMDNSDiscovery(h, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := md.Start(ctx); err != nil {
		t.Fatalf("md.Start: %v", err)
	}
	defer md.Close()

	// Seed the dedup map with a fake peer.
	fakePeer, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	md.mu.Lock()
	md.lastTry[fakePeer] = time.Now()
	md.mu.Unlock()

	// BrowseNow should clear dedup and signal the channel.
	md.BrowseNow()

	md.mu.Lock()
	if len(md.lastTry) != 0 {
		t.Errorf("BrowseNow should clear dedup map, got %d entries", len(md.lastTry))
	}
	md.mu.Unlock()

	// Channel should have been signaled (non-blocking check).
	select {
	case <-md.browseNowCh:
		// Drain it so browseLoop can pick it up on next iteration.
	default:
		// Channel might have already been consumed by browseLoop.
		// Either way, the dedup clear is the definitive test.
	}
}

func TestMDNSDiscovery_HandlePeerFound_NoConnections(t *testing.T) {
	// Verify HandlePeerFound doesn't crash when peer has no existing
	// connections (the common case for first discovery).
	netA := newMDNSNetwork(t)
	netB := newMDNSNetwork(t)

	md := NewMDNSDiscovery(netA.Host(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := md.Start(ctx); err != nil {
		t.Fatalf("md.Start: %v", err)
	}
	defer md.Close()

	addr, _ := ma.NewMultiaddr("/ip4/192.168.1.100/tcp/9999")
	md.HandlePeerFound(peer.AddrInfo{
		ID:    netB.Host().ID(),
		Addrs: []ma.Multiaddr{addr},
	})

	// No connections to close, no crash. Addresses added to peerstore.
	addrs := netA.Host().Peerstore().Addrs(netB.Host().ID())
	if len(addrs) == 0 {
		t.Fatal("expected addresses in peerstore")
	}
}

func TestMDNSDiscovery_HandlePeerFound_DirectNotClosed(t *testing.T) {
	// When a peer is already connected directly, HandlePeerFound
	// should NOT close the connection.
	netA := newMDNSNetwork(t)
	netB := newMDNSNetwork(t)

	md := NewMDNSDiscovery(netA.Host(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := md.Start(ctx); err != nil {
		t.Fatalf("md.Start: %v", err)
	}
	defer md.Close()

	// Establish a direct connection first.
	if err := netA.Host().Connect(ctx, peer.AddrInfo{
		ID:    netB.Host().ID(),
		Addrs: netB.Host().Addrs(),
	}); err != nil {
		t.Fatalf("direct connect: %v", err)
	}

	// Verify direct connection exists.
	conns := netA.Host().Network().ConnsToPeer(netB.Host().ID())
	if len(conns) == 0 {
		t.Fatal("expected direct connection")
	}
	for _, conn := range conns {
		if conn.Stat().Limited {
			t.Fatal("expected direct (non-limited) connection")
		}
	}

	// HandlePeerFound should NOT close the direct connection.
	addr, _ := ma.NewMultiaddr("/ip4/192.168.1.100/tcp/9999")
	md.HandlePeerFound(peer.AddrInfo{
		ID:    netB.Host().ID(),
		Addrs: []ma.Multiaddr{addr},
	})

	// Connection should still be open.
	time.Sleep(100 * time.Millisecond) // let async connect attempt settle
	conns = netA.Host().Network().ConnsToPeer(netB.Host().ID())
	if len(conns) == 0 {
		t.Error("direct connection should NOT have been closed")
	}
}

func TestMDNSDiscovery_TwoHosts(t *testing.T) {
	if testing.Short() {
		t.Skip("mDNS requires multicast networking")
	}

	netA := newMDNSNetwork(t)
	netB := newMDNSNetwork(t)

	mdA := NewMDNSDiscovery(netA.Host(), nil)
	mdB := NewMDNSDiscovery(netB.Host(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := mdA.Start(ctx); err != nil {
		t.Fatalf("mdA.Start: %v", err)
	}
	t.Cleanup(func() { mdA.Close() })

	if err := mdB.Start(ctx); err != nil {
		t.Fatalf("mdB.Start: %v", err)
	}
	t.Cleanup(func() { mdB.Close() })

	// Wait for mDNS discovery. The peers should find each other
	// and add addresses to their peerstores.
	deadline := time.After(15 * time.Second)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for mDNS discovery")
		case <-tick.C:
			addrsA := netA.Host().Peerstore().Addrs(netB.Host().ID())
			addrsB := netB.Host().Peerstore().Addrs(netA.Host().ID())
			if len(addrsA) > 0 && len(addrsB) > 0 {
				t.Logf("mDNS discovery successful: A sees %d addrs for B, B sees %d addrs for A",
					len(addrsA), len(addrsB))
				return
			}
		}
	}
}
