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
	Unseal(password, totpCode string) error
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
