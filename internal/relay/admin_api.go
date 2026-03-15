package relay

// Compile-time interface satisfaction checks.
var (
	_ RelayAdminAPI = (*AdminClient)(nil)
	_ RelayAdminAPI = (*RemoteAdminClient)(nil)
)

// RelayAdminAPI defines the common interface for relay administration.
// Both AdminClient (local Unix socket) and RemoteAdminClient (P2P stream)
// implement this interface, allowing CLI commands to work with either
// transport transparently.
type RelayAdminAPI interface {
	Unseal(password, totpCode string, yubikeyResponse []byte) error
	Seal() error
	SealStatus() (*SealStatusResponse, error)
	// InitVault removed from interface: seed material must never travel over
	// the network. Use AdminClient.InitVault() directly (local-only).
	CreateInvite(caveats []string, ttlSec int) (map[string]string, error)
	ListInvites() ([]map[string]any, error)
	RevokeInvite(id string) error
	ModifyInvite(id string, addCaveats []string) error
	CreateGroup(count, ttlSec, expiresSec int, namespace string) (*PairResponse, error)
	ListGroups() ([]GroupInfo, error)
	RevokeGroup(id string) error
	ListPeers() ([]AuthorizedPeerInfo, error)
	ListConnectedPeers() ([]ConnectedPeerInfo, error)
	AuthorizePeer(peerID, comment string) error
	DeauthorizePeer(peerID string) error
	SetPeerAttr(peerID, key, value string) error
	AuthReload() error
	ZKPTreeRebuild() (map[string]any, error)
	ZKPTreeInfo() (*ZKPTreeInfoResponse, error)
	GetMOTDStatus() (*MOTDStatusResponse, error)
	SetMOTD(message string) error
	ClearMOTD() error
	SetGoodbye(message string) error
	RetractGoodbye() error
	GoodbyeShutdown(message string) error
}

// AuthorizedPeerInfo is the JSON representation of an authorized peer
// for the admin API. Distinct from PeerInfo in tokens.go (pairing groups).
type AuthorizedPeerInfo struct {
	PeerID    string `json:"peer_id"`
	Role      string `json:"role"`
	Comment   string `json:"comment,omitempty"`
	Verified  string `json:"verified,omitempty"`
	Group     string `json:"group,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	RelayData bool   `json:"relay_data,omitempty"`
}

// ConnectedPeerInfo describes a currently connected peer with network details.
type ConnectedPeerInfo struct {
	PeerID       string `json:"peer_id"`
	ShortID      string `json:"short_id"`
	AgentVersion string `json:"agent_version,omitempty"`
	Direction    string `json:"direction"`
	ConnectedAt  string `json:"connected_at"`
	DurationSecs int    `json:"duration_seconds"`
	Transport    string `json:"transport"`
	RemoteAddr   string `json:"remote_addr"`
	IP           string `json:"ip"`
	IsRelay      bool   `json:"is_relay"`
	Authorized   bool   `json:"authorized"`
	Role         string `json:"role,omitempty"`
	Comment      string `json:"comment,omitempty"`
}
