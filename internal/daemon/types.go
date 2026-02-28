package daemon

import "github.com/shurlinet/shurli/pkg/p2pnet"

// StatusResponse is returned by GET /v1/status.
type StatusResponse struct {
	PeerID         string   `json:"peer_id"`
	Version        string   `json:"version"`
	UptimeSeconds  int      `json:"uptime_seconds"`
	ConnectedPeers int      `json:"connected_peers"`
	ListenAddrs    []string `json:"listen_addresses"`
	RelayAddrs     []string `json:"relay_addresses"`
	ServicesCount  int      `json:"services_count"`
	HasGlobalIPv6     bool     `json:"has_global_ipv6"`
	HasGlobalIPv4     bool     `json:"has_global_ipv4"`
	NATType           string   `json:"nat_type,omitempty"`
	STUNExternalAddrs []string `json:"stun_external_addrs,omitempty"`
	IsRelaying        bool     `json:"is_relaying"`
	Reachability      *p2pnet.ReachabilityGrade `json:"reachability,omitempty"`
}

// ServiceInfo is returned by GET /v1/services.
type ServiceInfo struct {
	Name         string `json:"name"`
	Protocol     string `json:"protocol"`
	LocalAddress string `json:"local_address"`
	Enabled      bool   `json:"enabled"`
}

// PeerInfo is returned by GET /v1/peers.
type PeerInfo struct {
	ID           string   `json:"id"`
	Addresses    []string `json:"addresses"`
	AgentVersion string   `json:"agent_version,omitempty"`
}

// PathInfo is returned by GET /v1/paths. Mirrors p2pnet.PeerPathInfo JSON tags.
type PathInfo struct {
	PeerID      string `json:"peer_id"`
	PathType    string `json:"path_type"`
	Address     string `json:"address"`
	ConnectedAt string `json:"connected_at"`
	Transport   string `json:"transport"`
	IPVersion   string `json:"ip_version"`
	LastRTTMs   float64 `json:"last_rtt_ms,omitempty"`
}

// AuthEntry is returned by GET /v1/auth.
type AuthEntry struct {
	PeerID    string `json:"peer_id"`
	Comment   string `json:"comment,omitempty"`
	Verified  string `json:"verified,omitempty"`   // e.g. "sha256:a1b2c3d4"
	ExpiresAt string `json:"expires_at,omitempty"` // RFC3339, empty = never
	Group     string `json:"group,omitempty"`      // pairing group ID
	Role      string `json:"role"`                 // "admin" or "member"
}

// AuthAddRequest is the body for POST /v1/auth.
type AuthAddRequest struct {
	PeerID  string `json:"peer_id"`
	Comment string `json:"comment,omitempty"`
	Role    string `json:"role,omitempty"` // "admin" or "member" (default: "member")
}

// PingRequest is the body for POST /v1/ping.
type PingRequest struct {
	Peer       string `json:"peer"`
	Count      int    `json:"count,omitempty"`       // 0 = continuous
	IntervalMs int    `json:"interval_ms,omitempty"` // default 1000
}

// PingResponse wraps ping results for non-streaming responses.
type PingResponse struct {
	Results []p2pnet.PingResult `json:"results"`
	Stats   p2pnet.PingStats   `json:"stats"`
}

// TraceRequest is the body for POST /v1/traceroute.
type TraceRequest struct {
	Peer string `json:"peer"`
}

// ResolveRequest is the body for POST /v1/resolve.
type ResolveRequest struct {
	Name string `json:"name"`
}

// ResolveResponse is returned by POST /v1/resolve.
type ResolveResponse struct {
	Name   string `json:"name"`
	PeerID string `json:"peer_id"`
	Source string `json:"source"` // "local_config", "peer_id" (direct parse)
}

// ConnectRequest is the body for POST /v1/connect.
type ConnectRequest struct {
	Peer    string `json:"peer"`
	Service string `json:"service"`
	Listen  string `json:"listen"`
}

// ConnectResponse is returned by POST /v1/connect.
type ConnectResponse struct {
	ID            string `json:"id"`
	ListenAddress string `json:"listen_address"`
	PathType      string `json:"path_type,omitempty"`
	Address       string `json:"address,omitempty"`
}

// ExposeRequest is the body for POST /v1/expose.
type ExposeRequest struct {
	Name         string `json:"name"`
	LocalAddress string `json:"local_address"`
}

// ErrorResponse is returned on failure.
type ErrorResponse struct {
	Error string `json:"error"`
}

// DataResponse wraps a successful response.
type DataResponse struct {
	Data any `json:"data"`
}
