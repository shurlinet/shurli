// Package plugin defines the Shurli plugin framework.
//
// Plugins extend Shurli with new capabilities (file transfer, Wake-on-LAN, etc.).
// Modules swap core implementations (reputation scoring, auth).
//
// Three-layer evolution:
//   - Layer 1: Compiled-in Go plugins (this package). Official plugins only.
//   - Layer 2: WASM via wazero (future). Any language, sandboxed.
//   - Layer 3: AI-driven plugin generation (future). Skills.md -> WASM.
//
// Layer 1 design constraints (keeps Layer 2 door open):
//   - Explicit capability grants via PluginContext (not raw internal access)
//   - No global state assumptions
//   - Inside the plugin: full Go capabilities, zero restrictions
//
// SECURITY INVARIANTS (G4 - must hold for ALL plugins, ALL layers):
//  1. Credential isolation: PluginContext never holds daemon auth tokens,
//     cookie paths, vault keys, or Ed25519 private keys. Enforced by
//     TestCredentialIsolation. DeriveKey() provides HKDF-derived keys only.
//  2. Auth delegation: plugin HTTP routes are wrapped with daemon auth
//     middleware. Plugins MUST NOT implement their own authentication.
//  3. State gating: plugin P2P stream handlers only execute in ACTIVE state.
//     The registry's wrapHandler rejects streams in any other state.
//  4. Namespace isolation: OpenStream enforces that plugins can only open
//     streams on protocols they declared via Protocols(). Violations return
//     ErrCodeNamespaceViolation.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// Checkpointer is optionally implemented by plugins that want state preserved
// across auto-restart cycles. When a plugin crashes and the supervisor triggers
// a restart, Checkpoint() is called before Stop() and Restore() after Start().
// Plugins that don't implement this interface restart with fresh state.
//
// IMPORTANT: Checkpoint() is called after a panic recovery in a handler, not
// during normal operation. The plugin's internal state may be inconsistent
// (half-written buffers, partial operations). Implementations MUST:
//   - Use internal synchronization to ensure only committed state is serialized
//   - Return ErrSkipCheckpoint if internal state may be inconsistent
//
// The framework cannot validate plugin-specific state semantics - this is
// the plugin author's responsibility. The framework provides HMAC integrity
// (detects external tampering) and timeout (detects hangs), but only the
// plugin knows whether its own internal state is consistent.
//
// The framework enforces a timeout on Checkpoint() (same as Start timeout).
// A hanging Checkpoint() results in a stateless restart.
type Checkpointer interface {
	Checkpoint() ([]byte, error)
	Restore([]byte) error
}

// ErrSkipCheckpoint is returned by Checkpoint() when a plugin has no state
// worth saving. The supervisor proceeds with a stateless restart.
var ErrSkipCheckpoint = errors.New("skip checkpoint: no state to save")

// StatusContributor is optionally implemented by plugins that contribute fields
// to the daemon status response. Any plugin can implement this - no special treatment.
// The returned map is included under plugin_status.<name> in the JSON response.
type StatusContributor interface {
	StatusFields() map[string]any
}

// Plugin is the interface that all Shurli plugins must implement.
//
// Lifecycle:
//   - Init() is called ONCE when the plugin is first loaded. It receives the PluginContext.
//   - Start() is called on enable. Can be called multiple times across enable/disable cycles.
//   - Stop() is called on disable. Clean shutdown of background work.
//   - OnNetworkReady() is called after bootstrap completes and relay is connected.
//
// Registration methods return static declarations read by the registry:
//   - Commands(), Routes(), Protocols() declare what the plugin provides.
//   - The registry handles registration/unregistration during Start()/Stop().
type Plugin interface {
	ID() string      // "shurli.io/official/filetransfer" - globally unique
	Name() string    // "filetransfer" - short display name for CLI/help
	Version() string

	// Lifecycle
	Init(ctx *PluginContext) error            // called ONCE at load time, gives context
	Start(ctx context.Context) error          // called on enable, ctx cancelled on shutdown/kill
	Stop() error                              // called on disable, clean shutdown
	OnNetworkReady() error                    // called after bootstrap + relay connected

	// Registration (static declarations, read by registry)
	Commands() []Command      // CLI commands this plugin provides
	Routes() []Route          // daemon HTTP endpoints
	Protocols() []Protocol    // P2P stream handlers
	ConfigSection() string    // YAML key this plugin owns (e.g. "filetransfer")
}

// Command describes a CLI command provided by a plugin.
type Command struct {
	Name        string           // e.g. "send"
	Description string           // one-line for help output
	Usage       string           // e.g. "shurli send <file> <peer>"
	Run         func(args []string) // execution entry point
	Hidden      bool             // hidden when plugin disabled
}

// Route describes a daemon HTTP endpoint provided by a plugin.
// Handler is wrapped with the daemon's auth middleware before registration.
// Plugins should NOT implement their own auth.
type Route struct {
	Method  string                                   // "GET", "POST", "DELETE"
	Path    string                                   // "/v1/send"
	Handler func(http.ResponseWriter, *http.Request) // standard http handler
}

// Protocol describes a P2P stream handler provided by a plugin.
//
// Version matching is EXACT: the full protocol ID is /shurli/<name>/<version>.
// There is no semver negotiation. To support multiple versions, register separate
// Protocol entries with different Version strings and route internally (X6 documentation).
type Protocol struct {
	Name    string               // e.g. "file-transfer" (a-z, 0-9, hyphens only, max 64 chars)
	Version string               // e.g. "2.0.0" - exact match, no semver negotiation
	Handler p2pnet.StreamHandler // func(serviceName string, s network.Stream)
	Policy  *p2pnet.PluginPolicy // transport + peer restrictions (nil = default)
}

// --- State machine ---

// State represents a plugin's lifecycle state.
type State int

const (
	StateLoading  State = iota // being registered
	StateReady                 // Init() completed, not started
	StateActive                // Start() completed, handling streams
	StateDraining              // Stop() called, waiting for in-progress
	StateStopped               // fully stopped, can re-enable via Start()
)

// String returns the human-readable state name.
func (s State) String() string {
	switch s {
	case StateLoading:
		return "loading"
	case StateReady:
		return "ready"
	case StateActive:
		return "active"
	case StateDraining:
		return "draining"
	case StateStopped:
		return "stopped"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// --- Structured error codes ---

// PluginError is a structured error returned by PluginContext methods.
// Messages are generic and safe - they never contain peer IDs, IPs, or file paths.
// This prevents identity/topology leakage through error messages (threat vector #14).
type PluginError struct {
	Code    int
	Message string
}

const (
	ErrCodePermissionDenied   = 1 // operation not allowed for this plugin
	ErrCodeTimeout            = 2 // operation timed out
	ErrCodePeerUnreachable    = 3 // could not connect to or reach the peer
	ErrCodePluginDisabled     = 4 // plugin is not in ACTIVE state
	ErrCodeInvalidArgument    = 5 // invalid argument provided by the plugin
	ErrCodeNamespaceViolation = 6 // protocol not in plugin's declared namespace
	ErrCodeResourceExhausted  = 7 // resource limit exceeded
	ErrCodeNotFound           = 8 // requested resource not found
	ErrCodeInternal           = 9 // internal framework error (not plugin's fault)
)

func (e *PluginError) Error() string {
	return fmt.Sprintf("plugin error %d: %s", e.Code, e.Message)
}

// newPluginError creates a PluginError with the given code and message.
func newPluginError(code int, msg string) *PluginError {
	return &PluginError{Code: code, Message: msg}
}

// --- PluginContext ---

// PluginContext provides a plugin with controlled access to Shurli's runtime.
// It is a concrete struct, NOT an interface. Only exported methods are available to plugins.
//
// CREDENTIAL ISOLATION: This struct has no field that holds daemon auth tokens,
// cookie paths, vault keys, or Ed25519 private keys. No method returns types
// from internal/identity or internal/vault. This is enforced by TestCredentialIsolation.
//
// Network methods (ConnectToPeer, OpenStream, ResolveName) are only valid during
// or after Start(), not during Init(). Init() is for receiving context and parsing config.
type PluginContext struct {
	pluginName     string
	logger         *slog.Logger
	network        *p2pnet.Network
	nameResolver   func(string) (peer.ID, error)
	peerConnector  func(context.Context, peer.ID) error // DHT + relay fallback
	configBytes    []byte
	configDir      string          // plugin's config directory path
	configReloadCb func([]byte)
	declaredProtos map[string]bool // protocol IDs this plugin declared
	keyDeriver     func(domain string) []byte          // HKDF-SHA256 key derivation
	scoreResolver  func(peer.ID) int                  // reputation score lookup (0-100)
	grantChecker   func(peer.ID, string) bool         // data access grant check (E1)
	peerAttrFunc       func(string, string) string        // peer attribute lookup (peerID, key) -> value
	relayGrantChecker  p2pnet.RelayGrantChecker           // relay grant cache for transfer budget/time checks (H7)
}

// Logger returns a plugin-scoped structured logger.
func (c *PluginContext) Logger() *slog.Logger {
	return c.logger
}

// ConnectToPeer establishes a connection to a remote peer using DHT + relay fallback.
// Only valid during or after Start(), not during Init().
func (c *PluginContext) ConnectToPeer(ctx context.Context, peerID peer.ID) *PluginError {
	if c.peerConnector == nil {
		return newPluginError(ErrCodeInternal, "peer connector not available")
	}
	err := c.peerConnector(ctx, peerID)
	if err != nil {
		return newPluginError(ErrCodePeerUnreachable, "failed to connect to peer")
	}
	return nil
}

// OpenStream opens a P2P stream to a remote peer on the given protocol.
// The protocolID must be one of the protocols declared by this plugin's Protocols() method.
// Attempting to open a stream on an undeclared protocol returns ErrCodeNamespaceViolation.
// Only valid during or after Start(), not during Init().
func (c *PluginContext) OpenStream(ctx context.Context, peerID peer.ID, protocolID string) (network.Stream, *PluginError) {
	if !c.declaredProtos[protocolID] {
		return nil, newPluginError(ErrCodeNamespaceViolation,
			fmt.Sprintf("protocol %q not declared by plugin %q", protocolID, c.pluginName))
	}
	if c.network == nil {
		return nil, newPluginError(ErrCodeInternal, "network not available")
	}
	s, err := c.network.Host().NewStream(ctx, peerID, protocol.ID(protocolID))
	if err != nil {
		return nil, newPluginError(ErrCodePeerUnreachable, "failed to open stream")
	}
	return s, nil
}

// ResolveName resolves a peer name to a peer ID using Shurli's name resolution chain.
// Only valid during or after Start(), not during Init().
func (c *PluginContext) ResolveName(name string) (peer.ID, *PluginError) {
	if c.nameResolver == nil {
		return "", newPluginError(ErrCodeInternal, "name resolver not available")
	}
	id, err := c.nameResolver(name)
	if err != nil {
		return "", newPluginError(ErrCodeNotFound, "name not found")
	}
	return id, nil
}

// Config returns the raw YAML bytes for this plugin's own config section.
// The plugin unmarshals this into its own config struct during Init().
func (c *PluginContext) Config() []byte {
	return c.configBytes
}

// OnConfigReload registers a callback that is invoked when the daemon's config
// is hot-reloaded and this plugin's section has changed. The callback receives
// the new raw YAML bytes for the plugin's section.
//
// MUST be called from Init() only. The callback field is not synchronized -
// calling this from a goroutine after Init() races with NotifyConfigReload.
func (c *PluginContext) OnConfigReload(callback func([]byte)) {
	c.configReloadCb = callback
}

// EngineHost returns the p2pnet.Network for protocol engine initialization.
// LAYER 1 ONLY: compiled-in plugins use this to create their protocol engines
// AND for request-time stream operations (OpenPluginStream, ResolveName, etc.).
// Layer 2 WASM plugins will NOT have this - they use host functions instead.
func (c *PluginContext) EngineHost() *p2pnet.Network {
	return c.network
}

// ConfigDir returns the plugin's config directory path.
// e.g. ~/.shurli/plugins/shurli.io/official/filetransfer/
func (c *PluginContext) ConfigDir() string {
	return c.configDir
}

// DeriveKey returns a 32-byte cryptographic key derived from the node's
// identity and the given domain string using HKDF-SHA256 (RFC 5869).
// Each (identity, domain) pair produces a unique, stable key.
// The raw identity key is never exposed to plugins.
// Used for HMAC integrity (e.g. queue.json persistence).
// Returns nil if no key deriver is configured or domain is empty.
func (c *PluginContext) DeriveKey(domain string) []byte {
	if c.keyDeriver == nil || domain == "" {
		return nil
	}
	return c.keyDeriver(domain)
}

// HasGrant checks if a peer has a valid data access grant for the given service.
// Returns false if no grant checker is configured.
func (c *PluginContext) HasGrant(peerID peer.ID, service string) bool {
	if c.grantChecker == nil {
		return false
	}
	return c.grantChecker(peerID, service)
}

// PeerAttr returns the value of a peer attribute from authorized_keys.
// Returns empty string if no resolver is configured or attribute not found.
func (c *PluginContext) PeerAttr(peerID string, key string) string {
	if c.peerAttrFunc == nil {
		return ""
	}
	return c.peerAttrFunc(peerID, key)
}

// RelayGrantChecker returns the relay grant checker for transfer budget/time checks (H7).
// Returns nil if no grant cache is configured.
func (c *PluginContext) RelayGrantChecker() p2pnet.RelayGrantChecker {
	return c.relayGrantChecker
}

// X6 fix: Phase 1C stubs unexported until reputation/metrics are wired.
// Re-export when the reputation pipeline or metrics integration is built.
// These were exported but had zero callers across the entire codebase.

// incrementMetric increments a named counter metric scoped to this plugin.
// No-op until telemetry integration is built.
func (c *PluginContext) incrementMetric(name string, delta float64) {}

// peerScore returns the reputation score for a peer (0-100).
// Returns 0 if no score resolver is configured or the peer has no history.
func (c *PluginContext) peerScore(id peer.ID) int {
	if c.scoreResolver == nil {
		return 0
	}
	return c.scoreResolver(id)
}

// peerAboveThreshold returns whether a peer's reputation score meets the threshold.
// Returns true if no score resolver is configured (permissive default).
func (c *PluginContext) peerAboveThreshold(id peer.ID, threshold int) bool {
	if c.scoreResolver == nil {
		return true
	}
	return c.scoreResolver(id) >= threshold
}

// reportInteraction reports a peer interaction outcome to the reputation system.
// No-op until reputation wiring is completed (Phase 1C).
func (c *PluginContext) reportInteraction(_ interactionReport) {}

// interactionOutcome describes the result of a plugin's interaction with a peer.
type interactionOutcome int

const (
	outcomeSuccess interactionOutcome = iota
	outcomeFailure
	outcomeTimeout
)

// interactionReport is submitted by plugins to the reputation system.
type interactionReport struct {
	plugin  string
	peerID  peer.ID
	outcome interactionOutcome
	weight  float64 // 0.0-1.0
}

// --- ContextProvider ---

// ContextProvider supplies runtime dependencies for building PluginContexts.
// Constructed in cmd_daemon.go where all runtime components are available.
// ServiceRegistry is held by the Registry for protocol registration, NOT passed to PluginContext.
type ContextProvider struct {
	Network         *p2pnet.Network
	ServiceRegistry *p2pnet.ServiceRegistry
	ConfigDir       string                                      // base config dir (~/.shurli/)
	NameResolver    func(name string) (peer.ID, error)
	PeerConnector   func(ctx context.Context, id peer.ID) error // DHT + relay fallback connection
	KeyDeriver      func(domain string) []byte                  // HKDF-SHA256 key derivation from identity
	ScoreResolver   func(peerID peer.ID) int                    // reputation score lookup (0-100)
	GrantChecker    func(peerID peer.ID, service string) bool   // data access grant check (E1)
	PeerAttrFunc       func(peerID string, key string) string     // peer attribute lookup from authorized_keys
	RelayGrantChecker  p2pnet.RelayGrantChecker                   // relay grant cache for transfer budget/time checks (H7)
}

// --- Info ---

// Info holds plugin metadata for introspection.
type Info struct {
	Name            string
	Version         string
	Type            string // "built-in" or "installed"
	State           State
	Enabled         bool
	Commands        []string
	Routes          []string
	Protocols       []string
	ConfigKey       string
	CrashCount      int // crashes in current window (resets after 5 min)
	LifetimeCrashes int // total crashes ever (resets only on daemon restart, limit: 10)
}
