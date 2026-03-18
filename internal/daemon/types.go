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
	Relays            []RelayStatus  `json:"relays,omitempty"`
	MOTDs             []MOTDInfo     `json:"motds,omitempty"`
	ConfigReload      *ConfigReloadState `json:"config_reload,omitempty"`
	PluginStatus      map[string]map[string]any `json:"plugin_status,omitempty"`
}

// RelayStatus describes a configured relay's connection state.
type RelayStatus struct {
	Address      string `json:"address"`
	PeerID       string `json:"peer_id"`
	ShortID      string `json:"short_id"`
	Connected    bool   `json:"connected"`
	RelayName    string `json:"relay_name,omitempty"`
	AgentVersion string `json:"agent_version,omitempty"`
}

// MOTDInfo describes a MOTD or goodbye message from a relay.
type MOTDInfo struct {
	RelayPeerID string `json:"relay_peer_id"`
	RelayName   string `json:"relay_name,omitempty"`
	Message     string `json:"message"`
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
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

// BandwidthStats is returned by GET /v1/bandwidth.
type BandwidthStats struct {
	TotalIn  int64                    `json:"total_in"`
	TotalOut int64                    `json:"total_out"`
	RateIn   float64                  `json:"rate_in"`
	RateOut  float64                  `json:"rate_out"`
	ByPeer   map[string]BandwidthPeer `json:"by_peer,omitempty"`
}

// BandwidthPeer holds per-peer bandwidth stats.
type BandwidthPeer struct {
	TotalIn  int64   `json:"total_in"`
	TotalOut int64   `json:"total_out"`
	RateIn   float64 `json:"rate_in"`
	RateOut  float64 `json:"rate_out"`
}

// RelayHealthResponse is returned by GET /v1/relay-health.
type RelayHealthResponse struct {
	Relays []RelayHealthEntry `json:"relays"`
}

// RelayHealthEntry is one relay's health status.
type RelayHealthEntry struct {
	PeerID      string  `json:"peer_id"`
	Score       float64 `json:"score"`
	RTTMs       float64 `json:"rtt_ms"`
	SuccessRate float64 `json:"success_rate"`
	ProbeCount  int     `json:"probe_count"`
	IsStatic    bool    `json:"is_static"`
}

// InviteCreateRequest is the body for POST /v1/invite.
type InviteCreateRequest struct {
	Name       string `json:"name,omitempty"`
	Count      int    `json:"count,omitempty"`        // default 1
	TTLSeconds int    `json:"ttl_seconds,omitempty"`  // default 86400 (24h)
}

// InviteCreateResponse is returned by POST /v1/invite.
type InviteCreateResponse struct {
	InviteID  string   `json:"invite_id"`
	Codes     []string `json:"codes"`
	GroupID   string   `json:"group_id"`
	TTL       int      `json:"ttl_seconds"`
	ExpiresAt string   `json:"expires_at"`
}

// InviteWaitResponse is returned by GET /v1/invite/{id}/wait.
type InviteWaitResponse struct {
	Status string `json:"status"` // "complete", "partial", "expired", "cancelled"
	Used   int    `json:"used"`
	Total  int    `json:"total"`
}

// RemoteServiceRequest is sent to POST /v1/services/remote.
type RemoteServiceRequest struct {
	Peer string `json:"peer"` // peer name or ID
}

// RemoteServiceResponse is returned by POST /v1/services/remote.
type RemoteServiceResponse struct {
	Services []p2pnet.RemoteServiceInfo `json:"services"`
}


// ErrorResponse is returned on failure.
type ErrorResponse struct {
	Error string `json:"error"`
}

// DataResponse wraps a successful response.
type DataResponse struct {
	Data any `json:"data"`
}

// PluginInfoResponse is returned by GET /v1/plugins and GET /v1/plugins/{name}.
type PluginInfoResponse struct {
	Name       string   `json:"name"`
	Version    string   `json:"version"`
	Type       string   `json:"type"`        // "built-in" or "installed"
	State      string   `json:"state"`       // "loading", "ready", "active", "draining", "stopped"
	Enabled    bool     `json:"enabled"`
	Commands   []string `json:"commands"`
	Routes     []string `json:"routes"`
	Protocols  []string `json:"protocols"`
	ConfigKey  string   `json:"config_key"`
	CrashCount int      `json:"crash_count"`
}

// PluginDisableAllResponse is returned by POST /v1/plugins/disable-all.
type PluginDisableAllResponse struct {
	Disabled int    `json:"disabled"`
	Error    string `json:"error,omitempty"`
}
