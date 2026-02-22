package p2pnet

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// --- STUN wire protocol tests ---

func TestBuildSTUNBindingRequest(t *testing.T) {
	txID := [12]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	req := BuildSTUNBindingRequest(txID)

	if len(req) != stunHeaderSize {
		t.Fatalf("expected %d bytes, got %d", stunHeaderSize, len(req))
	}

	// Message type = 0x0001 (Binding Request)
	msgType := binary.BigEndian.Uint16(req[0:2])
	if msgType != stunBindingReq {
		t.Errorf("message type = 0x%04x, want 0x%04x", msgType, stunBindingReq)
	}

	// Message length = 0 (no attributes)
	msgLen := binary.BigEndian.Uint16(req[2:4])
	if msgLen != 0 {
		t.Errorf("message length = %d, want 0", msgLen)
	}

	// Magic cookie
	cookie := binary.BigEndian.Uint32(req[4:8])
	if cookie != stunMagicCookie {
		t.Errorf("magic cookie = 0x%08x, want 0x%08x", cookie, stunMagicCookie)
	}

	// Transaction ID
	for i := 0; i < 12; i++ {
		if req[8+i] != txID[i] {
			t.Errorf("txID[%d] = %d, want %d", i, req[8+i], txID[i])
		}
	}
}

func TestBuildSTUNBindingResponse(t *testing.T) {
	txID := [12]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	ip := net.ParseIP("203.0.113.50")
	port := 12345

	resp := BuildSTUNBindingResponse(txID, ip, port)
	if resp == nil {
		t.Fatal("BuildSTUNBindingResponse returned nil")
	}

	// Parse it back
	if len(resp) < stunHeaderSize {
		t.Fatalf("response too short: %d", len(resp))
	}

	respType := binary.BigEndian.Uint16(resp[0:2])
	if respType != stunBindingResp {
		t.Errorf("response type = 0x%04x, want 0x%04x", respType, stunBindingResp)
	}

	// Parse attributes
	attrLen := int(binary.BigEndian.Uint16(resp[2:4]))
	parsedIP, parsedPort, err := parseSTUNAttributes(resp[stunHeaderSize:stunHeaderSize+attrLen], txID[:])
	if err != nil {
		t.Fatalf("parseSTUNAttributes: %v", err)
	}
	if parsedIP.String() != "203.0.113.50" {
		t.Errorf("IP = %s, want 203.0.113.50", parsedIP)
	}
	if parsedPort != port {
		t.Errorf("port = %d, want %d", parsedPort, port)
	}
}

func TestParseXorMappedAddress_IPv4(t *testing.T) {
	txID := [12]byte{0}
	ip := net.ParseIP("192.0.2.1").To4()
	port := 8080

	// Build XOR-MAPPED-ADDRESS value (8 bytes for IPv4)
	data := make([]byte, 8)
	data[0] = 0    // reserved
	data[1] = 0x01 // IPv4
	xPort := uint16(port) ^ uint16(stunMagicCookie>>16)
	binary.BigEndian.PutUint16(data[2:4], xPort)
	rawIP := binary.BigEndian.Uint32(ip)
	xAddr := rawIP ^ stunMagicCookie
	binary.BigEndian.PutUint32(data[4:8], xAddr)

	parsedIP, parsedPort, err := parseXorMappedAddress(data, txID[:])
	if err != nil {
		t.Fatalf("parseXorMappedAddress: %v", err)
	}
	if parsedIP.String() != "192.0.2.1" {
		t.Errorf("IP = %s, want 192.0.2.1", parsedIP)
	}
	if parsedPort != port {
		t.Errorf("port = %d, want %d", parsedPort, port)
	}
}

func TestParseXorMappedAddress_TooShort(t *testing.T) {
	_, _, err := parseXorMappedAddress([]byte{0, 1, 2}, nil)
	if err == nil {
		t.Error("expected error for short data")
	}
}

func TestParseMappedAddress_IPv4(t *testing.T) {
	data := make([]byte, 8)
	data[0] = 0    // reserved
	data[1] = 0x01 // IPv4
	binary.BigEndian.PutUint16(data[2:4], 9999)
	data[4] = 198
	data[5] = 51
	data[6] = 100
	data[7] = 10

	ip, port, err := parseMappedAddress(data)
	if err != nil {
		t.Fatalf("parseMappedAddress: %v", err)
	}
	if ip.String() != "198.51.100.10" {
		t.Errorf("IP = %s, want 198.51.100.10", ip)
	}
	if port != 9999 {
		t.Errorf("port = %d, want 9999", port)
	}
}

func TestParseMappedAddress_TooShort(t *testing.T) {
	_, _, err := parseMappedAddress([]byte{0, 1, 2})
	if err == nil {
		t.Error("expected error for short data")
	}
}

func TestParseMappedAddress_UnknownFamily(t *testing.T) {
	data := make([]byte, 8)
	data[1] = 0x99 // unknown family
	_, _, err := parseMappedAddress(data)
	if err == nil {
		t.Error("expected error for unknown family")
	}
}

func TestStunBytesEqual(t *testing.T) {
	if !stunBytesEqual([]byte{1, 2, 3}, []byte{1, 2, 3}) {
		t.Error("equal slices should return true")
	}
	if stunBytesEqual([]byte{1, 2, 3}, []byte{1, 2, 4}) {
		t.Error("different slices should return false")
	}
	if stunBytesEqual([]byte{1, 2}, []byte{1, 2, 3}) {
		t.Error("different length slices should return false")
	}
}

// --- NAT classification tests ---

func TestClassifyNAT_NoResults(t *testing.T) {
	natType := classifyNAT(nil)
	if natType != NATUnknown {
		t.Errorf("NAT type = %s, want unknown", natType)
	}
}

func TestClassifyNAT_AllFailed(t *testing.T) {
	results := []ProbeResult{
		{ServerAddr: "stun1:3478", Error: "timeout"},
		{ServerAddr: "stun2:3478", Error: "timeout"},
	}
	natType := classifyNAT(results)
	if natType != NATUnknown {
		t.Errorf("NAT type = %s, want unknown", natType)
	}
}

func TestClassifyNAT_SingleResult(t *testing.T) {
	results := []ProbeResult{
		{ServerAddr: "stun1:3478", ExternalIP: "203.0.113.50", ExternalPort: 12345},
	}
	natType := classifyNAT(results)
	if natType != NATUnknown {
		t.Errorf("NAT type = %s, want unknown (single result)", natType)
	}
}

func TestClassifyNAT_EndpointIndependent(t *testing.T) {
	// Same IP:port from both servers = address-restricted (conservative)
	results := []ProbeResult{
		{ServerAddr: "stun1:3478", ExternalIP: "203.0.113.50", ExternalPort: 12345},
		{ServerAddr: "stun2:3478", ExternalIP: "203.0.113.50", ExternalPort: 12345},
	}
	natType := classifyNAT(results)
	if natType != NATAddressRestricted {
		t.Errorf("NAT type = %s, want address-restricted", natType)
	}
}

func TestClassifyNAT_PortRestricted(t *testing.T) {
	// Same IP, different ports
	results := []ProbeResult{
		{ServerAddr: "stun1:3478", ExternalIP: "203.0.113.50", ExternalPort: 12345},
		{ServerAddr: "stun2:3478", ExternalIP: "203.0.113.50", ExternalPort: 12400},
	}
	natType := classifyNAT(results)
	if natType != NATPortRestricted {
		t.Errorf("NAT type = %s, want port-restricted", natType)
	}
}

func TestClassifyNAT_Symmetric(t *testing.T) {
	// Different IPs
	results := []ProbeResult{
		{ServerAddr: "stun1:3478", ExternalIP: "203.0.113.50", ExternalPort: 12345},
		{ServerAddr: "stun2:3478", ExternalIP: "198.51.100.10", ExternalPort: 12400},
	}
	natType := classifyNAT(results)
	if natType != NATSymmetric {
		t.Errorf("NAT type = %s, want symmetric", natType)
	}
}

func TestClassifyNAT_MixedSuccessFailure(t *testing.T) {
	// One success + one failure = unknown (only one successful result)
	results := []ProbeResult{
		{ServerAddr: "stun1:3478", ExternalIP: "203.0.113.50", ExternalPort: 12345},
		{ServerAddr: "stun2:3478", Error: "timeout"},
	}
	natType := classifyNAT(results)
	if natType != NATUnknown {
		t.Errorf("NAT type = %s, want unknown (only one success)", natType)
	}
}

// --- NATType.HolePunchable tests ---

func TestNATType_HolePunchable(t *testing.T) {
	cases := []struct {
		natType  NATType
		expected bool
	}{
		{NATNone, true},
		{NATFullCone, true},
		{NATAddressRestricted, true},
		{NATPortRestricted, true},
		{NATSymmetric, false},
		{NATUnknown, false},
	}

	for _, tc := range cases {
		t.Run(string(tc.natType), func(t *testing.T) {
			if tc.natType.HolePunchable() != tc.expected {
				t.Errorf("NATType(%s).HolePunchable() = %v, want %v",
					tc.natType, tc.natType.HolePunchable(), tc.expected)
			}
		})
	}
}

// --- Mock STUN server tests ---

func TestSTUNProber_WithMockServer(t *testing.T) {
	// Start a mock STUN server on localhost
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()

	serverAddr := pc.LocalAddr().String()

	// Mock server goroutine: reads Binding Request, sends Binding Response
	go func() {
		buf := make([]byte, 576)
		for {
			n, raddr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < stunHeaderSize {
				continue
			}

			// Parse request
			msgType := binary.BigEndian.Uint16(buf[0:2])
			if msgType != stunBindingReq {
				continue
			}

			var txID [12]byte
			copy(txID[:], buf[8:20])

			// Build response with the client's observed address
			udpAddr := raddr.(*net.UDPAddr)
			resp := BuildSTUNBindingResponse(txID, udpAddr.IP, udpAddr.Port)
			if resp != nil {
				pc.WriteTo(resp, raddr)
			}
		}
	}()

	// Probe using the mock server
	sp := NewSTUNProber([]string{serverAddr}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := sp.Probe(ctx)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	if len(result.Probes) != 1 {
		t.Fatalf("expected 1 probe, got %d", len(result.Probes))
	}

	probe := result.Probes[0]
	if probe.Error != "" {
		t.Fatalf("probe error: %s", probe.Error)
	}
	if probe.ExternalIP == "" {
		t.Error("ExternalIP should not be empty")
	}
	if probe.ExternalPort == 0 {
		t.Error("ExternalPort should not be 0")
	}
	if probe.Latency <= 0 {
		t.Error("Latency should be > 0")
	}

	// Result should be stored
	stored := sp.Result()
	if stored == nil {
		t.Fatal("Result() returned nil after successful probe")
	}
	if stored.ProbedAt.IsZero() {
		t.Error("ProbedAt should not be zero")
	}
}

func TestSTUNProber_TwoMockServers(t *testing.T) {
	// Start two mock STUN servers that return the same external IP:port
	// (simulating endpoint-independent NAT)
	servers := make([]string, 2)
	pcs := make([]net.PacketConn, 2)

	for i := 0; i < 2; i++ {
		pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen %d: %v", i, err)
		}
		defer pc.Close()
		pcs[i] = pc
		servers[i] = pc.LocalAddr().String()

		go func(pc net.PacketConn) {
			buf := make([]byte, 576)
			for {
				n, raddr, err := pc.ReadFrom(buf)
				if err != nil {
					return
				}
				if n < stunHeaderSize {
					continue
				}
				msgType := binary.BigEndian.Uint16(buf[0:2])
				if msgType != stunBindingReq {
					continue
				}
				var txID [12]byte
				copy(txID[:], buf[8:20])
				udpAddr := raddr.(*net.UDPAddr)
				resp := BuildSTUNBindingResponse(txID, udpAddr.IP, udpAddr.Port)
				if resp != nil {
					pc.WriteTo(resp, raddr)
				}
			}
		}(pc)
	}

	sp := NewSTUNProber(servers, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := sp.Probe(ctx)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	if len(result.Probes) != 2 {
		t.Fatalf("expected 2 probes, got %d", len(result.Probes))
	}

	for i, probe := range result.Probes {
		if probe.Error != "" {
			t.Errorf("probe[%d] error: %s", i, probe.Error)
		}
	}

	// Both probes go from the same UDP socket per server, but each probe
	// creates its own socket, so ports will differ. NAT type depends on
	// whether the OS assigns the same or different source ports.
	// Just verify we got a valid classification (not panic/error)
	validTypes := map[NATType]bool{
		NATAddressRestricted: true,
		NATPortRestricted:    true,
		NATSymmetric:         true,
		NATUnknown:           true,
	}
	if !validTypes[result.NATType] {
		t.Errorf("unexpected NAT type: %s", result.NATType)
	}

	if len(result.ExternalAddrs) == 0 {
		t.Error("expected at least one external address")
	}
}

func TestSTUNProber_AllFail(t *testing.T) {
	// Point at a non-existent server
	sp := NewSTUNProber([]string{"127.0.0.1:1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := sp.Probe(ctx)
	if err == nil {
		t.Fatal("expected error when all probes fail")
	}
	if result == nil {
		t.Fatal("result should not be nil even on failure")
	}
	if result.NATType != NATUnknown {
		t.Errorf("NAT type = %s, want unknown", result.NATType)
	}
}

func TestSTUNProber_WithMetrics(t *testing.T) {
	// Start a mock STUN server
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()

	go func() {
		buf := make([]byte, 576)
		for {
			n, raddr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < stunHeaderSize {
				continue
			}
			var txID [12]byte
			copy(txID[:], buf[8:20])
			udpAddr := raddr.(*net.UDPAddr)
			resp := BuildSTUNBindingResponse(txID, udpAddr.IP, udpAddr.Port)
			if resp != nil {
				pc.WriteTo(resp, raddr)
			}
		}
	}()

	m := NewMetrics("test", "go1.26")
	sp := NewSTUNProber([]string{pc.LocalAddr().String()}, m)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = sp.Probe(ctx)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	// Verify metrics recorded
	// The STUNProbeTotal counter should have been incremented
	// (We can't easily read counter values, but we verify no panic)
}

func TestSTUNProber_DefaultServers(t *testing.T) {
	sp := NewSTUNProber(nil, nil)
	if len(sp.servers) != len(DefaultSTUNServers) {
		t.Errorf("expected %d default servers, got %d", len(DefaultSTUNServers), len(sp.servers))
	}
}

func TestSTUNProber_ResultBeforeProbe(t *testing.T) {
	sp := NewSTUNProber(nil, nil)
	if sp.Result() != nil {
		t.Error("Result() should be nil before first probe")
	}
}
