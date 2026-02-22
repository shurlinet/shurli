package p2pnet

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// NATType describes the type of NAT based on STUN probing results.
type NATType string

const (
	NATNone              NATType = "none"               // No NAT (public IP matches local)
	NATFullCone          NATType = "full-cone"           // Endpoint-independent mapping
	NATAddressRestricted NATType = "address-restricted"  // Same mapping for all destinations
	NATPortRestricted    NATType = "port-restricted"     // Port differs per destination
	NATSymmetric         NATType = "symmetric"           // Different mapping per destination
	NATUnknown           NATType = "unknown"             // Could not determine
)

// HolePunchable returns true if this NAT type is amenable to hole punching.
func (n NATType) HolePunchable() bool {
	switch n {
	case NATNone, NATFullCone, NATAddressRestricted:
		return true
	case NATPortRestricted:
		return true // possible but less reliable
	default:
		return false
	}
}

// ProbeResult is the outcome of a single STUN server probe.
type ProbeResult struct {
	ServerAddr   string        `json:"server_addr"`
	ExternalAddr string        `json:"external_addr,omitempty"`
	ExternalIP   string        `json:"external_ip,omitempty"`
	ExternalPort int           `json:"external_port,omitempty"`
	Latency      time.Duration `json:"latency_ms"`
	Error        string        `json:"error,omitempty"`
}

// STUNResult is the aggregate result of probing multiple STUN servers.
type STUNResult struct {
	Probes        []ProbeResult `json:"probes"`
	NATType       NATType       `json:"nat_type"`
	ExternalAddrs []string      `json:"external_addrs"`
	ProbedAt      time.Time     `json:"probed_at"`
}

// DefaultSTUNServers are well-known public STUN servers.
var DefaultSTUNServers = []string{
	"stun.l.google.com:19302",
	"stun.cloudflare.com:3478",
}

// STUNProber discovers external address mappings via STUN (RFC 5389 Binding Request).
// It probes multiple servers to determine the external IP:port and NAT type.
type STUNProber struct {
	servers []string
	metrics *Metrics // nil-safe

	mu     sync.RWMutex
	result *STUNResult
}

// NewSTUNProber creates a STUNProber. If servers is empty, defaults are used.
// Metrics is optional (nil-safe).
func NewSTUNProber(servers []string, m *Metrics) *STUNProber {
	if len(servers) == 0 {
		servers = DefaultSTUNServers
	}
	return &STUNProber{
		servers: servers,
		metrics: m,
	}
}

// Probe sends STUN Binding Requests to all configured servers concurrently
// and determines NAT type from the results.
func (sp *STUNProber) Probe(ctx context.Context) (*STUNResult, error) {
	results := make([]ProbeResult, len(sp.servers))
	var wg sync.WaitGroup

	for i, server := range sp.servers {
		wg.Add(1)
		go func(idx int, srv string) {
			defer wg.Done()
			results[idx] = stunBindingRequest(ctx, srv)
		}(i, server)
	}
	wg.Wait()

	// Build aggregate result
	result := &STUNResult{
		Probes:   results,
		ProbedAt: time.Now(),
	}

	// Collect unique external addresses
	seen := make(map[string]bool)
	var successful int
	for _, r := range results {
		if r.Error == "" {
			successful++
			if !seen[r.ExternalAddr] {
				seen[r.ExternalAddr] = true
				result.ExternalAddrs = append(result.ExternalAddrs, r.ExternalAddr)
			}
		}
	}

	// Determine NAT type
	result.NATType = classifyNAT(results)

	// Store result
	sp.mu.Lock()
	sp.result = result
	sp.mu.Unlock()

	// Record metrics
	if sp.metrics != nil && sp.metrics.STUNProbeTotal != nil {
		for _, r := range results {
			if r.Error == "" {
				sp.metrics.STUNProbeTotal.WithLabelValues("success").Inc()
			} else {
				sp.metrics.STUNProbeTotal.WithLabelValues("failure").Inc()
			}
		}
	}

	slog.Info("stun: probe complete",
		"servers", len(sp.servers),
		"successful", successful,
		"nat_type", string(result.NATType),
		"external_addrs", len(result.ExternalAddrs),
	)

	if successful == 0 {
		return result, fmt.Errorf("all STUN probes failed")
	}

	return result, nil
}

// Result returns the most recent probe result (thread-safe).
func (sp *STUNProber) Result() *STUNResult {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	return sp.result
}

// classifyNAT determines NAT type based on probe results from multiple servers.
// With two servers, we can distinguish:
//   - Same IP:port from both: endpoint-independent mapping (full-cone or address-restricted)
//   - Same IP, different ports: port-dependent mapping (port-restricted)
//   - Different IPs: symmetric NAT
//
// Distinguishing full-cone from address-restricted requires CHANGE-REQUEST support
// in the STUN server (RFC 5389 extension), which most public servers lack.
// We conservatively classify endpoint-independent as address-restricted.
func classifyNAT(results []ProbeResult) NATType {
	var successful []ProbeResult
	for _, r := range results {
		if r.Error == "" {
			successful = append(successful, r)
		}
	}

	if len(successful) == 0 {
		return NATUnknown
	}

	if len(successful) == 1 {
		// One result - can't classify NAT type definitively
		return NATUnknown
	}

	// Compare external addresses from different servers
	firstIP := successful[0].ExternalIP
	firstPort := successful[0].ExternalPort

	sameIP := true
	samePort := true

	for _, r := range successful[1:] {
		if r.ExternalIP != firstIP {
			sameIP = false
		}
		if r.ExternalPort != firstPort {
			samePort = false
		}
	}

	switch {
	case sameIP && samePort:
		// Endpoint-independent mapping. Conservative: address-restricted.
		return NATAddressRestricted
	case sameIP && !samePort:
		return NATPortRestricted
	case !sameIP:
		return NATSymmetric
	default:
		return NATUnknown
	}
}

// --- STUN RFC 5389 wire protocol ---

const (
	stunMagicCookie   uint32 = 0x2112A442
	stunBindingReq    uint16 = 0x0001
	stunBindingResp   uint16 = 0x0101
	stunHeaderSize           = 20
	stunAttrXorMapped uint16 = 0x0020
	stunAttrMapped    uint16 = 0x0001
)

// stunBindingRequest sends a single STUN Binding Request and parses the response.
func stunBindingRequest(ctx context.Context, server string) ProbeResult {
	result := ProbeResult{ServerAddr: server}

	start := time.Now()

	// Resolve server address
	addr, err := net.ResolveUDPAddr("udp4", server)
	if err != nil {
		result.Error = fmt.Sprintf("resolve: %v", err)
		return result
	}

	// Create UDP connection
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		result.Error = fmt.Sprintf("dial: %v", err)
		return result
	}
	defer conn.Close()

	// Generate transaction ID (12 bytes)
	var txID [12]byte
	if _, err := rand.Read(txID[:]); err != nil {
		result.Error = fmt.Sprintf("rand: %v", err)
		return result
	}

	// Build Binding Request (20-byte header, no attributes)
	req := make([]byte, stunHeaderSize)
	binary.BigEndian.PutUint16(req[0:2], stunBindingReq)
	binary.BigEndian.PutUint16(req[2:4], 0) // length = 0
	binary.BigEndian.PutUint32(req[4:8], stunMagicCookie)
	copy(req[8:20], txID[:])

	// Set deadline from context or 3s default
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(3 * time.Second)
	}
	conn.SetDeadline(deadline)

	// Send request
	if _, err := conn.Write(req); err != nil {
		result.Error = fmt.Sprintf("write: %v", err)
		return result
	}

	// Read response
	buf := make([]byte, 576) // STUN messages are typically small
	n, err := conn.Read(buf)
	if err != nil {
		result.Error = fmt.Sprintf("read: %v", err)
		return result
	}

	result.Latency = time.Since(start)

	// Parse response header
	if n < stunHeaderSize {
		result.Error = "response too short"
		return result
	}

	respType := binary.BigEndian.Uint16(buf[0:2])
	if respType != stunBindingResp {
		result.Error = fmt.Sprintf("unexpected response type: 0x%04x", respType)
		return result
	}

	// Verify magic cookie
	cookie := binary.BigEndian.Uint32(buf[4:8])
	if cookie != stunMagicCookie {
		result.Error = "invalid magic cookie"
		return result
	}

	// Verify transaction ID
	if !stunBytesEqual(buf[8:20], txID[:]) {
		result.Error = "transaction ID mismatch"
		return result
	}

	// Parse attributes
	attrLen := int(binary.BigEndian.Uint16(buf[2:4]))
	if stunHeaderSize+attrLen > n {
		result.Error = "attribute length exceeds packet"
		return result
	}

	ip, port, err := parseSTUNAttributes(buf[stunHeaderSize:stunHeaderSize+attrLen], txID[:])
	if err != nil {
		result.Error = err.Error()
		return result
	}

	result.ExternalIP = ip.String()
	result.ExternalPort = port
	result.ExternalAddr = fmt.Sprintf("%s:%d", ip.String(), port)

	return result
}

// parseSTUNAttributes extracts the external address from STUN response attributes.
// Prefers XOR-MAPPED-ADDRESS (0x0020) over MAPPED-ADDRESS (0x0001).
func parseSTUNAttributes(data []byte, txID []byte) (net.IP, int, error) {
	var mappedIP net.IP
	var mappedPort int
	var foundXor bool

	offset := 0
	for offset+4 <= len(data) {
		attrType := binary.BigEndian.Uint16(data[offset : offset+2])
		attrLen := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		offset += 4

		if offset+attrLen > len(data) {
			break
		}

		attrData := data[offset : offset+attrLen]

		switch attrType {
		case stunAttrXorMapped:
			ip, port, err := parseXorMappedAddress(attrData, txID)
			if err == nil {
				mappedIP = ip
				mappedPort = port
				foundXor = true
			}
		case stunAttrMapped:
			if !foundXor {
				ip, port, err := parseMappedAddress(attrData)
				if err == nil {
					mappedIP = ip
					mappedPort = port
				}
			}
		}

		// Attributes are padded to 4-byte boundaries
		offset += attrLen
		if attrLen%4 != 0 {
			offset += 4 - (attrLen % 4)
		}
	}

	if mappedIP == nil {
		return nil, 0, fmt.Errorf("no mapped address in response")
	}

	return mappedIP, mappedPort, nil
}

// parseXorMappedAddress decodes an XOR-MAPPED-ADDRESS (RFC 5389 section 15.2).
func parseXorMappedAddress(data []byte, txID []byte) (net.IP, int, error) {
	if len(data) < 8 {
		return nil, 0, fmt.Errorf("XOR-MAPPED-ADDRESS too short")
	}

	family := data[1] // 0x01 = IPv4, 0x02 = IPv6
	xPort := binary.BigEndian.Uint16(data[2:4])
	port := int(xPort ^ uint16(stunMagicCookie>>16))

	switch family {
	case 0x01: // IPv4
		xAddr := binary.BigEndian.Uint32(data[4:8])
		addr := xAddr ^ stunMagicCookie
		ip := net.IPv4(byte(addr>>24), byte(addr>>16), byte(addr>>8), byte(addr))
		return ip, port, nil

	case 0x02: // IPv6
		if len(data) < 20 {
			return nil, 0, fmt.Errorf("IPv6 address too short")
		}
		// XOR with magic cookie (4 bytes) + transaction ID (12 bytes)
		xorKey := make([]byte, 16)
		binary.BigEndian.PutUint32(xorKey[0:4], stunMagicCookie)
		copy(xorKey[4:16], txID)

		ip := make(net.IP, 16)
		for i := 0; i < 16; i++ {
			ip[i] = data[4+i] ^ xorKey[i]
		}
		return ip, port, nil

	default:
		return nil, 0, fmt.Errorf("unknown address family: 0x%02x", family)
	}
}

// parseMappedAddress decodes a MAPPED-ADDRESS (RFC 5389 section 15.1).
func parseMappedAddress(data []byte) (net.IP, int, error) {
	if len(data) < 8 {
		return nil, 0, fmt.Errorf("MAPPED-ADDRESS too short")
	}

	family := data[1]
	port := int(binary.BigEndian.Uint16(data[2:4]))

	switch family {
	case 0x01: // IPv4
		ip := net.IPv4(data[4], data[5], data[6], data[7])
		return ip, port, nil
	case 0x02: // IPv6
		if len(data) < 20 {
			return nil, 0, fmt.Errorf("IPv6 address too short")
		}
		ip := make(net.IP, 16)
		copy(ip, data[4:20])
		return ip, port, nil
	default:
		return nil, 0, fmt.Errorf("unknown address family: 0x%02x", family)
	}
}

// stunBytesEqual compares two byte slices for equality.
func stunBytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// BuildSTUNBindingRequest creates a STUN Binding Request packet with the given
// transaction ID. Exported for testing.
func BuildSTUNBindingRequest(txID [12]byte) []byte {
	req := make([]byte, stunHeaderSize)
	binary.BigEndian.PutUint16(req[0:2], stunBindingReq)
	binary.BigEndian.PutUint16(req[2:4], 0)
	binary.BigEndian.PutUint32(req[4:8], stunMagicCookie)
	copy(req[8:20], txID[:])
	return req
}

// BuildSTUNBindingResponse creates a STUN Binding Response with an
// XOR-MAPPED-ADDRESS attribute. Exported for testing.
func BuildSTUNBindingResponse(txID [12]byte, ip net.IP, port int) []byte {
	ip4 := ip.To4()
	if ip4 == nil {
		return nil // only IPv4 for now
	}

	// XOR-MAPPED-ADDRESS attribute (type=0x0020, length=8)
	attr := make([]byte, 12) // 4-byte attr header + 8-byte value
	binary.BigEndian.PutUint16(attr[0:2], stunAttrXorMapped)
	binary.BigEndian.PutUint16(attr[2:4], 8) // value length
	attr[4] = 0                              // reserved
	attr[5] = 0x01                           // IPv4 family

	// XOR port
	xPort := uint16(port) ^ uint16(stunMagicCookie>>16)
	binary.BigEndian.PutUint16(attr[6:8], xPort)

	// XOR address
	rawIP := binary.BigEndian.Uint32(ip4)
	xAddr := rawIP ^ stunMagicCookie
	binary.BigEndian.PutUint32(attr[8:12], xAddr)

	// Build response header
	resp := make([]byte, stunHeaderSize+len(attr))
	binary.BigEndian.PutUint16(resp[0:2], stunBindingResp)
	binary.BigEndian.PutUint16(resp[2:4], uint16(len(attr)))
	binary.BigEndian.PutUint32(resp[4:8], stunMagicCookie)
	copy(resp[8:20], txID[:])
	copy(resp[stunHeaderSize:], attr)

	return resp
}
