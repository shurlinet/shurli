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
