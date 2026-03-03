package p2pnet

import (
	"testing"
)

func TestParseDNSAddrRecords_Valid(t *testing.T) {
	records := []string{
		"dnsaddr=/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
	}

	result := parseDNSAddrRecords(records)
	if len(result) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(result))
	}
	if result[0].ID.String() != "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN" {
		t.Errorf("unexpected peer ID: %s", result[0].ID)
	}
	if len(result[0].Addrs) != 1 {
		t.Errorf("expected 1 addr, got %d", len(result[0].Addrs))
	}
}

func TestParseDNSAddrRecords_MultipleSamePeer(t *testing.T) {
	records := []string{
		"dnsaddr=/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
		"dnsaddr=/ip6/2001:db8::1/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
	}

	result := parseDNSAddrRecords(records)
	if len(result) != 1 {
		t.Fatalf("expected 1 peer (merged), got %d", len(result))
	}
	if len(result[0].Addrs) != 2 {
		t.Errorf("expected 2 merged addrs, got %d", len(result[0].Addrs))
	}
}

func TestParseDNSAddrRecords_Malformed(t *testing.T) {
	records := []string{
		"not-a-dnsaddr-record",
		"dnsaddr=invalid-multiaddr",
		"dnsaddr=/ip4/203.0.113.50/tcp/7777", // missing /p2p/<id>
		"",
	}

	result := parseDNSAddrRecords(records)
	if len(result) != 0 {
		t.Errorf("expected 0 peers from malformed records, got %d", len(result))
	}
}

func TestParseDNSAddrRecords_Empty(t *testing.T) {
	result := parseDNSAddrRecords(nil)
	if len(result) != 0 {
		t.Errorf("expected 0 peers from nil, got %d", len(result))
	}

	result = parseDNSAddrRecords([]string{})
	if len(result) != 0 {
		t.Errorf("expected 0 peers from empty, got %d", len(result))
	}
}

func TestParseDNSAddrRecords_WhitespaceHandling(t *testing.T) {
	records := []string{
		"  dnsaddr=/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN  ",
	}

	result := parseDNSAddrRecords(records)
	if len(result) != 1 {
		t.Fatalf("expected 1 peer with whitespace trimming, got %d", len(result))
	}
}
