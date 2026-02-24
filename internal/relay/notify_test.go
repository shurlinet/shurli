package relay

import (
	"bytes"
	"testing"
)

func TestEncodePeerNotify_RoundTrip(t *testing.T) {
	groupID := "ab12cd34"
	groupSize := byte(3)
	peers := []NotifyPeerInfo{
		{PeerID: "12D3KooWTestPeerAAAAAAAAAAAAAAAAAAAAAAAA", Name: "home-node"},
		{PeerID: "12D3KooWTestPeerBBBBBBBBBBBBBBBBBBBBBBBB", Name: "laptop"},
	}

	data := EncodePeerNotify(groupID, groupSize, peers)
	if len(data) == 0 {
		t.Fatal("EncodePeerNotify returned empty data")
	}

	// Decode.
	gotPeers, gotGroupID, gotGroupSize, err := ReadPeerNotify(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ReadPeerNotify error: %v", err)
	}

	if gotGroupID != groupID {
		t.Errorf("group ID = %q, want %q", gotGroupID, groupID)
	}
	if gotGroupSize != int(groupSize) {
		t.Errorf("group size = %d, want %d", gotGroupSize, groupSize)
	}
	if len(gotPeers) != len(peers) {
		t.Fatalf("peer count = %d, want %d", len(gotPeers), len(peers))
	}
	for i, p := range gotPeers {
		if p.PeerID != peers[i].PeerID {
			t.Errorf("peer[%d].PeerID = %q, want %q", i, p.PeerID, peers[i].PeerID)
		}
		if p.Name != peers[i].Name {
			t.Errorf("peer[%d].Name = %q, want %q", i, p.Name, peers[i].Name)
		}
	}
}

func TestEncodePeerNotify_EmptyPeers(t *testing.T) {
	data := EncodePeerNotify("deadbeef", 2, nil)

	gotPeers, gotGroupID, gotGroupSize, err := ReadPeerNotify(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ReadPeerNotify error: %v", err)
	}
	if gotGroupID != "deadbeef" {
		t.Errorf("group ID = %q, want %q", gotGroupID, "deadbeef")
	}
	if gotGroupSize != 2 {
		t.Errorf("group size = %d, want 2", gotGroupSize)
	}
	if len(gotPeers) != 0 {
		t.Errorf("peer count = %d, want 0", len(gotPeers))
	}
}

func TestEncodePeerNotify_EmptyName(t *testing.T) {
	peers := []NotifyPeerInfo{
		{PeerID: "12D3KooWTestPeerCCCCCCCCCCCCCCCCCCCCCCCC", Name: ""},
	}

	data := EncodePeerNotify("aabbccdd", 2, peers)

	gotPeers, _, _, err := ReadPeerNotify(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ReadPeerNotify error: %v", err)
	}
	if len(gotPeers) != 1 {
		t.Fatalf("peer count = %d, want 1", len(gotPeers))
	}
	if gotPeers[0].Name != "" {
		t.Errorf("name = %q, want empty", gotPeers[0].Name)
	}
}

func TestReadPeerNotify_BadVersion(t *testing.T) {
	data := []byte{0x99, 0x08, 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 2, 0}

	_, _, _, err := ReadPeerNotify(bytes.NewReader(data))
	if err == nil {
		t.Fatal("expected error for bad version")
	}
}

func TestReadPeerNotify_Truncated(t *testing.T) {
	// Just a version byte, nothing else.
	_, _, _, err := ReadPeerNotify(bytes.NewReader([]byte{0x01}))
	if err == nil {
		t.Fatal("expected error for truncated data")
	}
}

func TestEncodePeerNotify_WireFormat(t *testing.T) {
	// Verify the exact wire layout for one peer.
	peers := []NotifyPeerInfo{
		{PeerID: "AB", Name: "x"},
	}
	data := EncodePeerNotify("g1", 2, peers)

	// Expected:
	// [0] version = 0x01
	// [1] group ID len = 2
	// [2-3] group ID "g1"
	// [4] group size = 2
	// [5] peer count = 1
	// [6-7] peer ID len (BE) = 0x00, 0x02
	// [8-9] peer ID "AB"
	// [10] name len = 1
	// [11] name "x"
	// [12-43] HMAC proof (32 bytes of zeros, no proof set)
	expected := []byte{
		0x01,       // version
		0x02,       // group ID len
		'g', '1',   // group ID
		0x02,       // group size
		0x01,       // peer count
		0x00, 0x02, // peer ID len (BE)
		'A', 'B',   // peer ID
		0x01,       // name len
		'x',        // name
		// 32-byte HMAC proof (zeros)
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	}

	if !bytes.Equal(data, expected) {
		t.Errorf("wire format mismatch:\ngot:  %v\nwant: %v", data, expected)
	}
}

func TestEncodePeerNotify_WithHMAC(t *testing.T) {
	proof := make([]byte, HMACProofSize)
	for i := range proof {
		proof[i] = byte(i + 1)
	}
	peers := []NotifyPeerInfo{
		{PeerID: "TESTPEER", Name: "node-a", HMACProof: proof},
	}

	data := EncodePeerNotify("aabb1122", 2, peers)
	gotPeers, _, _, err := ReadPeerNotify(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ReadPeerNotify error: %v", err)
	}
	if len(gotPeers) != 1 {
		t.Fatalf("peer count = %d, want 1", len(gotPeers))
	}
	if !bytes.Equal(gotPeers[0].HMACProof, proof) {
		t.Errorf("HMAC proof mismatch:\ngot:  %v\nwant: %v", gotPeers[0].HMACProof, proof)
	}
}

func TestReadPeerNotify_TruncatedPeerData(t *testing.T) {
	// Valid header but claims 1 peer with no peer data following.
	data := []byte{
		0x01,       // version
		0x02,       // group ID len
		'g', '1',   // group ID
		0x02,       // group size
		0x01,       // peer count = 1 but no peer data
	}

	_, _, _, err := ReadPeerNotify(bytes.NewReader(data))
	if err == nil {
		t.Fatal("expected error for truncated peer data")
	}
}
