package sdk

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestRelayServiceCID_Deterministic(t *testing.T) {
	c1 := RelayServiceCID("")
	c2 := RelayServiceCID("")
	if c1 != c2 {
		t.Errorf("CID should be deterministic: %s != %s", c1, c2)
	}
}

func TestRelayServiceCID_NamespaceIsolation(t *testing.T) {
	global := RelayServiceCID("")
	private := RelayServiceCID("mynet")
	if global == private {
		t.Error("different namespaces should produce different CIDs")
	}

	// Two different namespaces should also differ
	net1 := RelayServiceCID("net1")
	net2 := RelayServiceCID("net2")
	if net1 == net2 {
		t.Error("net1 and net2 should produce different CIDs")
	}
}

func TestStaticRelaySource(t *testing.T) {
	s := &StaticRelaySource{Addrs: []string{"/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"}}
	addrs := s.RelayAddrs()
	if len(addrs) != 1 {
		t.Fatalf("expected 1 addr, got %d", len(addrs))
	}
}

func TestStaticRelaySource_Empty(t *testing.T) {
	s := &StaticRelaySource{}
	addrs := s.RelayAddrs()
	if len(addrs) != 0 {
		t.Errorf("expected 0 addrs, got %d", len(addrs))
	}
}

func TestRelayDiscovery_NilDHT(t *testing.T) {
	rd := NewRelayDiscovery(nil, "", nil)

	// Discover with nil DHT should return nil
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	peers := rd.Discover(ctx, 5)
	if peers != nil {
		t.Errorf("expected nil from Discover with nil DHT, got %d peers", len(peers))
	}

	// AllRelays with no statics and no DHT
	all := rd.AllRelays()
	if len(all) != 0 {
		t.Errorf("expected 0 relays, got %d", len(all))
	}

	// RelayAddrs with no relays
	addrs := rd.RelayAddrs()
	if len(addrs) != 0 {
		t.Errorf("expected 0 relay addrs, got %d", len(addrs))
	}
}

func TestRelayDiscovery_StaticRelaysFirst(t *testing.T) {
	// Create two static relay AddrInfos
	static1, err := peer.AddrInfoFromString("/ip4/203.0.113.1/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if err != nil {
		t.Fatalf("parse static1: %v", err)
	}
	static2, err := peer.AddrInfoFromString("/ip4/203.0.113.2/tcp/7777/p2p/12D3KooWQYhTNQdmr3ArTeUHRYzFg94BKyTkoWBDWez9kSCVe4Xo")
	if err != nil {
		t.Fatalf("parse static2: %v", err)
	}

	rd := NewRelayDiscovery([]peer.AddrInfo{*static1, *static2}, "", nil)

	all := rd.AllRelays()
	if len(all) != 2 {
		t.Fatalf("expected 2 relays, got %d", len(all))
	}
	if all[0].ID != static1.ID {
		t.Error("first relay should be static1")
	}
	if all[1].ID != static2.ID {
		t.Error("second relay should be static2")
	}
}

func TestRelayDiscovery_RelayAddrsFormat(t *testing.T) {
	ai, err := peer.AddrInfoFromString("/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	rd := NewRelayDiscovery([]peer.AddrInfo{*ai}, "", nil)
	addrs := rd.RelayAddrs()
	if len(addrs) != 1 {
		t.Fatalf("expected 1 addr, got %d", len(addrs))
	}
	// Should contain both the multiaddr and the peer ID
	expected := "/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
	if addrs[0] != expected {
		t.Errorf("unexpected addr format:\n  got:  %s\n  want: %s", addrs[0], expected)
	}
}

func TestRelayDiscovery_PeerSourceClosesChannel(t *testing.T) {
	ai, err := peer.AddrInfoFromString("/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	rd := NewRelayDiscovery([]peer.AddrInfo{*ai}, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := rd.PeerSource(ctx, 10)

	// Should get exactly 1 peer then channel closes
	var count int
	for range ch {
		count++
	}
	if count != 1 {
		t.Errorf("expected 1 peer from PeerSource, got %d", count)
	}
}

func TestRelayDiscovery_SetHostSetDHT(t *testing.T) {
	h, err := libp2p.New(
		libp2p.NoSecurity,
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	)
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer h.Close()

	rd := NewRelayDiscovery(nil, "", nil)

	// SetHost and SetDHT should not panic with valid/nil values
	rd.SetHost(h)
	rd.SetDHT(nil) // nil DHT is valid

	// Discover should still return nil with nil DHT
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	peers := rd.Discover(ctx, 5)
	if peers != nil {
		t.Errorf("expected nil, got %d peers", len(peers))
	}
}

func TestRelayDiscovery_NilMetricsSafety(t *testing.T) {
	rd := NewRelayDiscovery(nil, "test", nil)
	// None of these should panic with nil metrics
	rd.AllRelays()
	rd.RelayAddrs()
}

func TestRelayDiscovery_AdvertiseNilDHT(t *testing.T) {
	rd := NewRelayDiscovery(nil, "", nil)

	// Advertise with nil DHT should return immediately
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		rd.Advertise(ctx, time.Hour)
		close(done)
	}()

	select {
	case <-done:
		// Good - returned immediately because no DHT
	case <-time.After(2 * time.Second):
		t.Error("Advertise with nil DHT should return immediately")
	}
}
