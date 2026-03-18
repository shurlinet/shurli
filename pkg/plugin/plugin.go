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
package plugin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/shurlinet/shurli/pkg/p2pnet"
)

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
	Init(ctx *PluginContext) error // called ONCE at load time, gives context
	Start() error                 // called on enable, can be called multiple times
	Stop() error                  // called on disable, clean shutdown
	OnNetworkReady() error        // called after bootstrap + relay connected

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
type Protocol struct {
	Name    string               // e.g. "file-transfer"
	Version string               // e.g. "2.0.0"
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
	keyDeriver     func(domain string) []byte // HKDF-SHA256 key derivation
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
// e.g. ~/.config/shurli/plugins/shurli.io/official/filetransfer/
func (c *PluginContext) ConfigDir() string {
	return c.configDir
}

// DeriveKey returns a 32-byte cryptographic key derived from the node's
// identity and the given domain string using HKDF-SHA256 (RFC 5869).
// Each (identity, domain) pair produces a unique, stable key.
// The raw identity key is never exposed to plugins.
// Used for HMAC integrity (e.g. queue.json persistence).
// Returns nil if no key deriver is configured.
func (c *PluginContext) DeriveKey(domain string) []byte {
	if c.keyDeriver == nil {
		return nil
	}
	return c.keyDeriver(domain)
}

// IncrementMetric increments a named counter metric scoped to this plugin.
// The metric name is automatically prefixed with the plugin name to prevent collisions.
// No-op if metrics are not enabled on this node.
func (c *PluginContext) IncrementMetric(name string, delta float64) {
	// Stub - metrics wiring added when telemetry integration is built.
	// When wired: creates/increments prometheus counter "shurli_plugin_<pluginName>_<name>".
}

// PeerScore returns the reputation score for a peer (0-100).
// Returns 0 until reputation wiring is completed (Phase 1C).
func (c *PluginContext) PeerScore(_ peer.ID) int {
	return 0 // stub until reputation pipeline is wired
}

// PeerAboveThreshold returns whether a peer's reputation score meets the threshold.
// Returns true until reputation wiring is completed (Phase 1C).
func (c *PluginContext) PeerAboveThreshold(_ peer.ID, _ int) bool {
	return true // stub until reputation pipeline is wired
}

// ReportInteraction reports a peer interaction outcome to the reputation system.
// No-op until reputation wiring is completed (Phase 1C).
func (c *PluginContext) ReportInteraction(_ InteractionReport) {
	// no-op until reputation pipeline is wired
}

// --- Interaction reporting ---

// InteractionOutcome describes the result of a plugin's interaction with a peer.
type InteractionOutcome int

const (
	OutcomeSuccess InteractionOutcome = iota
	OutcomeFailure
	OutcomeTimeout
)

// InteractionReport is submitted by plugins to the reputation system.
type InteractionReport struct {
	Plugin  string
	PeerID  peer.ID
	Outcome InteractionOutcome
	Weight  float64 // 0.0-1.0
}

// --- ContextProvider ---

// ContextProvider supplies runtime dependencies for building PluginContexts.
// Constructed in cmd_daemon.go where all runtime components are available.
// ServiceRegistry is held by the Registry for protocol registration, NOT passed to PluginContext.
type ContextProvider struct {
	Network         *p2pnet.Network
	ServiceRegistry *p2pnet.ServiceRegistry
	ConfigDir       string                                      // base config dir (~/.config/shurli/)
	NameResolver    func(name string) (peer.ID, error)
	PeerConnector   func(ctx context.Context, id peer.ID) error // DHT + relay fallback connection
	KeyDeriver      func(domain string) []byte                  // HKDF-SHA256 key derivation from identity
}

// --- Info ---

// Info holds plugin metadata for introspection.
type Info struct {
	Name       string
	Version    string
	Type       string // "built-in" or "installed"
	State      State
	Enabled    bool
	Commands   []string
	Routes     []string
	Protocols  []string
	ConfigKey  string
	CrashCount int
}
