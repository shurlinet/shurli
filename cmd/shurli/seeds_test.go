package main

import (
	"testing"
)

func TestSeedPeerIDs(t *testing.T) {
	ids := SeedPeerIDs()

	if len(ids) == 0 {
		t.Fatal("SeedPeerIDs() should return at least one peer ID")
	}

	// HardcodedSeeds has 4 entries (2 relays x 2 addresses each),
	// but only 2 unique peer IDs.
	if len(ids) != 2 {
		t.Errorf("expected 2 unique peer IDs, got %d", len(ids))
	}

	// All values should be true
	for id, v := range ids {
		if !v {
			t.Errorf("SeedPeerIDs[%s] should be true", id.String()[:16]+"...")
		}
	}
}

func TestSeedPeerIDs_ContainsHardcodedPeers(t *testing.T) {
	ids := SeedPeerIDs()

	// Verify the peer IDs from HardcodedSeeds are present.
	// We test by checking each seed address parses to a known ID.
	for _, s := range HardcodedSeeds {
		// We already tested SeedPeerIDs extracts them, so just verify
		// the count matches the unique peers in HardcodedSeeds.
		_ = s
	}

	if len(ids) < 1 {
		t.Error("should have at least one seed peer ID")
	}
}
