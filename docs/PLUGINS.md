# Shurli Plugin System

Package plugin defines the Shurli plugin framework.

Plugins extend Shurli with new capabilities (file transfer, Wake-on-LAN, etc.). Modules swap core implementations (reputation scoring, auth).

Three-layer evolution:

- **Layer 1**: Compiled-in Go plugins (this package). Official plugins only.
- **Layer 2**: WASM via wazero (future). Any language, sandboxed.
- **Layer 3**: AI-driven plugin generation (future). Skills.md -> WASM.

Layer 1 design constraints (keeps Layer 2 door open):

- Explicit capability grants via PluginContext (not raw internal access)
- No global state assumptions
- Inside the plugin: full Go capabilities, zero restrictions

Security invariants (must hold for ALL plugins, ALL layers):

1. **Credential isolation**: PluginContext never holds daemon auth tokens, cookie paths, vault keys, or Ed25519 private keys. Enforced by TestCredentialIsolation. DeriveKey() provides HKDF-derived keys only.
2. **Auth delegation**: plugin HTTP routes are wrapped with daemon auth middleware. Plugins MUST NOT implement their own authentication.
3. **State gating**: plugin P2P stream handlers only execute in ACTIVE state. The registry's wrapHandler rejects streams in any other state.
4. **Namespace isolation**: OpenStream enforces that plugins can only open streams on protocols they declared via Protocols(). Violations return ErrCodeNamespaceViolation.

```go
import "github.com/shurlinet/shurli/pkg/plugin"
```

---

## Constants

### Error Codes

Structured error codes returned by PluginContext methods. Messages are generic and safe - they never contain peer IDs, IPs, or file paths. This prevents identity/topology leakage through error messages.

```go
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
```

---

## Variables

```go
var ErrSkipCheckpoint = errors.New("skip checkpoint: no state to save")
```

ErrSkipCheckpoint is returned by Checkpoint() when a plugin has no state worth saving. The supervisor proceeds with a stateless restart.

---

## Functions

### func AtomicWriteFile

```go
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error
```

AtomicWriteFile writes data to a file atomically using temp file + fsync + rename. This ensures that a crash during write leaves either the old file or the new file, never a half-written file. Used for queue.json, shares.json, config writes.

### func RegisterCLICommand

```go
func RegisterCLICommand(entry CLICommandEntry)
```

RegisterCLICommand adds a CLI command to the global registry. Validates command names to prevent shell injection in completion scripts.

### func UnregisterCLICommands

```go
func UnregisterCLICommands(pluginName string)
```

UnregisterCLICommands removes all CLI commands for a plugin.

### func FindCLICommand

```go
func FindCLICommand(name string) (*CLICommandEntry, bool)
```

FindCLICommand looks up a CLI command by name.

### func CLICommandDescriptions

```go
func CLICommandDescriptions() []CLICommandEntry
```

CLICommandDescriptions returns all registered CLI commands sorted by name. Deep-copies Flags and Subcommands slices so callers cannot mutate originals.

### func ValidTransition

```go
func ValidTransition(from, to State) error
```

ValidTransition checks whether a state transition is allowed.

Valid transitions:

```
LOADING  -> READY     (Init succeeded)
READY    -> ACTIVE    (Start succeeded, first enable)
ACTIVE   -> DRAINING  (Stop called)
DRAINING -> STOPPED   (drain complete)
STOPPED  -> ACTIVE    (re-enable, Start called)
READY    -> STOPPED   (never started, daemon shutting down)
```

### func GenerateBashCompletion

```go
func GenerateBashCompletion(cmds []CLICommandEntry) string
```

GenerateBashCompletion returns bash completion additions for all registered plugin commands. Output is inserted into the bash completion script's command list and case branches.

### func GenerateZshCompletion

```go
func GenerateZshCompletion(cmds []CLICommandEntry) string
```

GenerateZshCompletion returns zsh completion additions for all registered plugin commands.

### func GenerateFishCompletion

```go
func GenerateFishCompletion(cmds []CLICommandEntry) string
```

GenerateFishCompletion returns fish completion additions for all registered plugin commands.

### func GenerateManSection

```go
func GenerateManSection(cmds []CLICommandEntry) string
```

GenerateManSection returns troff-formatted man page section for all registered plugin commands.

---

## Interfaces

### type Plugin

```go
type Plugin interface {
    ID() string      // "shurli.io/official/filetransfer" - globally unique
    Name() string    // "filetransfer" - short display name for CLI/help
    Version() string

    // Lifecycle
    Init(ctx *PluginContext) error        // called ONCE at load time, gives context
    Start(ctx context.Context) error      // called on enable, ctx cancelled on shutdown/kill
    Stop() error                          // called on disable, clean shutdown
    OnNetworkReady() error               // called after bootstrap + relay connected

    // Registration (static declarations, read by registry)
    Commands() []Command      // CLI commands this plugin provides
    Routes() []Route          // daemon HTTP endpoints
    Protocols() []Protocol    // P2P stream handlers
    ConfigSection() string    // YAML key this plugin owns (e.g. "filetransfer")
}
```

Plugin is the interface that all Shurli plugins must implement.

Lifecycle:

- Init() is called ONCE when the plugin is first loaded. It receives the PluginContext.
- Start() is called on enable. Can be called multiple times across enable/disable cycles.
- Stop() is called on disable. Clean shutdown of background work.
- OnNetworkReady() is called after bootstrap completes and relay is connected.

Registration methods return static declarations read by the registry:

- Commands(), Routes(), Protocols() declare what the plugin provides.
- The registry handles registration/unregistration during Start()/Stop().

Plugin identification uses a Terraform-style address: `host/namespace/name`. Official: `shurli.io/official/filetransfer`. Third-party: `github.com/someone/shurli-wakeonlan`. Names must be lowercase alphanumeric with hyphens (`a-z`, `0-9`, `-`), max 64 characters. IDs max 128 characters with at least 2 segments.

### type Checkpointer

```go
type Checkpointer interface {
    Checkpoint() ([]byte, error)
    Restore([]byte) error
}
```

Checkpointer is optionally implemented by plugins that want state preserved across auto-restart cycles. When a plugin crashes and the supervisor triggers a restart, Checkpoint() is called before Stop() and Restore() after Start(). Plugins that don't implement this interface restart with fresh state.

IMPORTANT: Checkpoint() is called after a panic recovery in a handler, not during normal operation. The plugin's internal state may be inconsistent (half-written buffers, partial operations). Implementations MUST:

- Use internal synchronization to ensure only committed state is serialized
- Return ErrSkipCheckpoint if internal state may be inconsistent

The framework cannot validate plugin-specific state semantics - this is the plugin author's responsibility. The framework provides HMAC integrity (detects external tampering) and timeout (detects hangs), but only the plugin knows whether its own internal state is consistent.

The framework enforces a timeout on Checkpoint() (same as Start timeout, 30s). A hanging Checkpoint() results in a stateless restart. Maximum checkpoint size is 10MB per plugin.

### type StatusContributor

```go
type StatusContributor interface {
    StatusFields() map[string]any
}
```

StatusContributor is optionally implemented by plugins that contribute fields to the daemon status response. Any plugin can implement this - no special treatment. The returned map is included under `plugin_status.<name>` in the JSON response.

---

## Types

### type State

```go
type State int
```

State represents a plugin's lifecycle state.

```go
const (
    StateLoading  State = iota // being registered
    StateReady                  // Init() completed, not started
    StateActive                 // Start() completed, handling streams
    StateDraining               // Stop() called, waiting for in-progress
    StateStopped                // fully stopped, can re-enable via Start()
)
```

State machine:

```
LOADING ──> READY ──> ACTIVE ──> DRAINING ──> STOPPED
                        ^                        |
                        |________________________|
                              (re-enable)
```

#### func (State) String

```go
func (s State) String() string
```

String returns the human-readable state name: "loading", "ready", "active", "draining", or "stopped".

### type PluginError

```go
type PluginError struct {
    Code    int
    Message string
}
```

PluginError is a structured error returned by PluginContext methods. Messages are generic and safe - they never contain peer IDs, IPs, or file paths. This prevents identity/topology leakage through error messages.

#### func (*PluginError) Error

```go
func (e *PluginError) Error() string
```

Error returns the formatted error string: `"plugin error <code>: <message>"`.

### type Command

```go
type Command struct {
    Name        string              // e.g. "send"
    Description string              // one-line for help output
    Usage       string              // e.g. "shurli send <file> <peer>"
    Run         func(args []string) // execution entry point
    Hidden      bool                // hidden when plugin disabled
}
```

Command describes a CLI command provided by a plugin.

### type Route

```go
type Route struct {
    Method  string                                   // "GET", "POST", "DELETE"
    Path    string                                   // "/v1/send"
    Handler func(http.ResponseWriter, *http.Request) // standard http handler
}
```

Route describes a daemon HTTP endpoint provided by a plugin. Handler is wrapped with the daemon's auth middleware before registration. Plugins should NOT implement their own auth.

### type Protocol

```go
type Protocol struct {
    Name    string               // e.g. "file-transfer" (a-z, 0-9, hyphens only, max 64 chars)
    Version string               // e.g. "2.0.0" - exact match, no semver negotiation
    Handler sdk.StreamHandler    // func(serviceName string, s network.Stream)
    Policy  *sdk.PluginPolicy   // transport + peer restrictions (nil = default)
}
```

Protocol describes a P2P stream handler provided by a plugin.

Version matching is EXACT: the full protocol ID is `/shurli/<name>/<version>`. There is no semver negotiation. To support multiple versions, register separate Protocol entries with different Version strings and route internally.

Protocol names must be lowercase alphanumeric with hyphens only. Core Shurli protocol names (relay-pair, relay-unseal, relay-admin, relay-motd, peer-notify, zkp-auth, ping, kad) are reserved and cannot be used by plugins.

### type PluginContext

```go
type PluginContext struct {
    // contains unexported fields
}
```

PluginContext provides a plugin with controlled access to Shurli's runtime. It is a concrete struct, NOT an interface. Only exported methods are available to plugins.

CREDENTIAL ISOLATION: This struct has no field that holds daemon auth tokens, cookie paths, vault keys, or Ed25519 private keys. No method returns types from internal/identity or internal/vault. This is enforced by TestCredentialIsolation.

Network methods (ConnectToPeer, OpenStream, ResolveName) are only valid during or after Start(), not during Init(). Init() is for receiving context and parsing config.

#### func (*PluginContext) Logger

```go
func (c *PluginContext) Logger() *slog.Logger
```

Logger returns a plugin-scoped structured logger. All log entries are tagged with `plugin=<name>`.

#### func (*PluginContext) ConnectToPeer

```go
func (c *PluginContext) ConnectToPeer(ctx context.Context, peerID peer.ID) *PluginError
```

ConnectToPeer establishes a connection to a remote peer using DHT + relay fallback. Only valid during or after Start(), not during Init().

#### func (*PluginContext) OpenStream

```go
func (c *PluginContext) OpenStream(ctx context.Context, peerID peer.ID, protocolID string) (network.Stream, *PluginError)
```

OpenStream opens a P2P stream to a remote peer on the given protocol. The protocolID must be one of the protocols declared by this plugin's Protocols() method. Attempting to open a stream on an undeclared protocol returns ErrCodeNamespaceViolation. Only valid during or after Start(), not during Init().

#### func (*PluginContext) ResolveName

```go
func (c *PluginContext) ResolveName(name string) (peer.ID, *PluginError)
```

ResolveName resolves a peer name to a peer ID using Shurli's name resolution chain. Only valid during or after Start(), not during Init().

#### func (*PluginContext) Config

```go
func (c *PluginContext) Config() []byte
```

Config returns the raw YAML bytes for this plugin's own config section. The plugin unmarshals this into its own config struct during Init().

#### func (*PluginContext) ConfigDir

```go
func (c *PluginContext) ConfigDir() string
```

ConfigDir returns the plugin's config directory path. e.g. `~/.shurli/plugins/shurli.io/official/filetransfer/`

#### func (*PluginContext) OnConfigReload

```go
func (c *PluginContext) OnConfigReload(callback func([]byte))
```

OnConfigReload registers a callback that is invoked when the daemon's config is hot-reloaded and this plugin's section has changed. The callback receives the new raw YAML bytes for the plugin's section.

MUST be called from Init() only. The callback field is not synchronized - calling this from a goroutine after Init() races with NotifyConfigReload.

#### func (*PluginContext) DeriveKey

```go
func (c *PluginContext) DeriveKey(domain string) []byte
```

DeriveKey returns a 32-byte cryptographic key derived from the node's identity and the given domain string using HKDF-SHA256 (RFC 5869). Each (identity, domain) pair produces a unique, stable key. The raw identity key is never exposed to plugins. Used for HMAC integrity (e.g. queue.json persistence). Returns nil if no key deriver is configured or domain is empty.

#### func (*PluginContext) HasGrant

```go
func (c *PluginContext) HasGrant(peerID peer.ID, service string) bool
```

HasGrant checks if a peer has a valid data access grant for the given service. Returns false if no grant checker is configured.

#### func (*PluginContext) PeerAttr

```go
func (c *PluginContext) PeerAttr(peerID string, key string) string
```

PeerAttr returns the value of a peer attribute from authorized_keys. Returns empty string if no resolver is configured or attribute not found.

#### func (*PluginContext) EngineHost

```go
func (c *PluginContext) EngineHost() *sdk.Network
```

EngineHost returns the sdk.Network for protocol engine initialization. LAYER 1 ONLY: compiled-in plugins use this to create their protocol engines AND for request-time stream operations (OpenPluginStream, ResolveName, etc.). Layer 2 WASM plugins will NOT have this - they use host functions instead.

#### func (*PluginContext) RelayGrantChecker

```go
func (c *PluginContext) RelayGrantChecker() sdk.RelayGrantChecker
```

RelayGrantChecker returns the relay grant checker for transfer budget/time checks. Returns nil if no grant cache is configured.

### type ContextProvider

```go
type ContextProvider struct {
    Network           *sdk.Network
    ServiceRegistry   *sdk.ServiceRegistry
    ConfigDir         string                                      // base config dir (~/.shurli/)
    NameResolver      func(name string) (peer.ID, error)
    PeerConnector     func(ctx context.Context, id peer.ID) error // DHT + relay fallback
    KeyDeriver        func(domain string) []byte                  // HKDF-SHA256 from identity
    ScoreResolver     func(peerID peer.ID) int                    // reputation score (0-100)
    GrantChecker      func(peerID peer.ID, service string) bool   // data access grant check
    PeerAttrFunc      func(peerID string, key string) string      // peer attribute lookup
    RelayGrantChecker sdk.RelayGrantChecker                       // relay grant cache
}
```

ContextProvider supplies runtime dependencies for building PluginContexts. Constructed in cmd_daemon.go where all runtime components are available. ServiceRegistry is held by the Registry for protocol registration, NOT passed to PluginContext.

### type Info

```go
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
```

Info holds plugin metadata for introspection. Returned by Registry.List() and Registry.GetInfo().

### type CLICommandEntry

```go
type CLICommandEntry struct {
    Name        string
    Description string
    Usage       string
    PluginName  string             // which plugin provides this
    Run         func(args []string)
    Flags       []CLIFlagEntry     // for dynamic completion/man generation
    Subcommands []CLISubcommand    // for commands like "share" with add/remove/list
}
```

CLICommandEntry describes a CLI command provided by a plugin. Used for dynamic help, man page generation, and shell completion.

### type CLIFlagEntry

```go
type CLIFlagEntry struct {
    Long        string   // e.g. "follow"
    Short       string   // e.g. "f" (empty = no short flag)
    Description string   // e.g. "Follow transfer progress"
    Type        string   // "bool", "string", "int", "enum", "file", "directory"
    Enum        []string // non-nil only when Type="enum" (e.g. ["low","normal","high"])
    RequiresArg bool     // true if flag takes a value (non-bool flags)
}
```

CLIFlagEntry describes a CLI flag for dynamic completion generation.

### type CLISubcommand

```go
type CLISubcommand struct {
    Name        string
    Description string
    Flags       []CLIFlagEntry
}
```

CLISubcommand describes a subcommand (e.g. "share add", "share remove").

### type Registry

```go
type Registry struct {
    // contains unexported fields
}
```

Registry manages plugin lifecycle: registration, enable/disable, and introspection.

Plugins cannot install, register, or discover other plugins. This is a hard-coded architectural constraint, not a permission that can be granted. The Registry.Register() method is called by the daemon startup code, never by plugins. PluginContext has no method for plugin installation or registration.

#### func NewRegistry

```go
func NewRegistry(provider *ContextProvider) *Registry
```

NewRegistry creates a plugin registry with the given runtime dependencies. provider may be nil for testing (no network, no service registry).

#### func (*Registry) Register

```go
func (r *Registry) Register(p Plugin) error
```

Register adds a plugin to the registry and calls Init(). Transitions: LOADING -> READY on success. Panics in Init() are recovered and returned as errors.

Validates the plugin name (alphanumeric + hyphens, max 64 chars) and ID (host/namespace/name format, max 128 chars). Creates the plugin's config directory at `~/.shurli/plugins/<id>/` with 0700 permissions. Reads config.yaml from the config directory (limited to 1MB).

#### func (*Registry) Enable

```go
func (r *Registry) Enable(name string) error
```

Enable starts a plugin, registering its protocols with the service registry. Valid from READY or STOPPED state. Idempotent if already ACTIVE. Enforces a 5-second cooldown between transitions.

Start() is called with a 30-second timeout. If Start() exceeds the timeout, the plugin is stopped and returned to its previous state. On success, protocols are registered with the service registry and the plugin transitions to ACTIVE.

#### func (*Registry) Disable

```go
func (r *Registry) Disable(name string) error
```

Disable stops a plugin, unregistering its protocols. Transitions: ACTIVE -> DRAINING -> STOPPED. Idempotent if already STOPPED. Stop() has a 30-second timeout.

On disable, PluginContext runtime fields (network, nameResolver, peerConnector) are nilled so the disabled plugin cannot use them. The supervisor is marked as disabled to prevent auto-restart.

#### func (*Registry) DisableAll

```go
func (r *Registry) DisableAll() (int, error)
```

DisableAll stops every active plugin. Errors are collected but never stop iteration. This is the kill switch for incident response. It also catches plugins in LOADING state (mid-Enable) by forcing them to STOPPED, ensuring the kill switch is truly atomic. Disables in reverse registration order for deterministic shutdown.

#### func (*Registry) StartAll

```go
func (r *Registry) StartAll() error
```

StartAll starts all plugins that are in READY state (post-registration, pre-bootstrap).

#### func (*Registry) StopAll

```go
func (r *Registry) StopAll() error
```

StopAll stops all active plugins during daemon shutdown. Also handles LOADING and READY plugins by transitioning them to STOPPED.

#### func (*Registry) ApplyConfig

```go
func (r *Registry) ApplyConfig(pluginStates map[string]bool) error
```

ApplyConfig enables or disables plugins based on config state. key = plugin name, value = enabled. Unknown names are collected as errors.

#### func (*Registry) NotifyNetworkReady

```go
func (r *Registry) NotifyNetworkReady() error
```

NotifyNetworkReady fans out OnNetworkReady() to all ACTIVE plugins. Called after bootstrap completes and relay is connected. Each plugin has a 30-second timeout to prevent one hanging plugin from blocking all subsequent plugins.

#### func (*Registry) NotifyConfigReload

```go
func (r *Registry) NotifyConfigReload()
```

NotifyConfigReload re-reads each active plugin's config.yaml and calls their OnConfigReload callback if the bytes changed. Panic recovery ensures one panicking callback does not prevent subsequent reloads.

#### func (*Registry) List

```go
func (r *Registry) List() []Info
```

List returns metadata for all registered plugins.

#### func (*Registry) GetInfo

```go
func (r *Registry) GetInfo(name string) (*Info, error)
```

GetInfo returns metadata for a single plugin.

#### func (*Registry) GetPlugin

```go
func (r *Registry) GetPlugin(name string) Plugin
```

GetPlugin returns the Plugin instance by name. Used for direct plugin interaction (e.g., fetching StatusContributor). Returns nil if not found.

#### func (*Registry) AllCommands

```go
func (r *Registry) AllCommands() []Command
```

AllCommands returns commands from all ACTIVE plugins.

#### func (*Registry) AllRoutes

```go
func (r *Registry) AllRoutes() []Route
```

AllRoutes returns routes from all ACTIVE plugins.

#### func (*Registry) AllRegisteredRoutes

```go
func (r *Registry) AllRegisteredRoutes() []Route
```

AllRegisteredRoutes returns routes from ALL registered plugins regardless of state. Used for mux setup at server start. Different from AllRoutes() which returns only ACTIVE. Callers MUST use IsRouteActive() per-request to gate disabled plugin routes.

#### func (*Registry) IsRouteActive

```go
func (r *Registry) IsRouteActive(method, path string) bool
```

IsRouteActive checks if the plugin providing a route is in ACTIVE state. Used per-request to return 404 when a plugin is disabled.

#### func (*Registry) ActiveProtocols

```go
func (r *Registry) ActiveProtocols() []Protocol
```

ActiveProtocols returns protocols from all ACTIVE plugins (for introspection).

#### func (*Registry) StatusContributions

```go
func (r *Registry) StatusContributions() map[string]map[string]any
```

StatusContributions returns status fields from all ACTIVE plugins that implement StatusContributor. Keyed by plugin Name().

---

## Supervisor

Every plugin gets an Erlang-style supervisor that handles crash recovery:

1. **Panic recovery** - stream handler panics are caught, logged with full stack trace, and the stream is reset. The plugin stays running.

2. **Crash counting** - panics are counted within a 5-minute sliding window.

3. **Auto-restart** - after a handler crash, the supervisor automatically restarts the plugin with exponential backoff (0s, 1s, 2s + random 0-500ms jitter).

4. **Circuit breaker** - 3 crashes within 5 minutes or 10 lifetime crashes (per daemon session) triggers automatic disable. No more restarts. The plugin stays stopped until manually re-enabled or the daemon is restarted.

5. **Checkpoint/Restore** - plugins that implement the Checkpointer interface can save state before a crash restart and restore it after. Checkpoint data is HMAC-SHA256 verified using keys derived from the node's identity via DeriveKey().

Lifecycle method panics (Init/Start/Stop/OnNetworkReady) record crashes for the circuit breaker but do NOT trigger auto-restart. Auto-restart is only triggered by stream handler panics during ACTIVE state (transient failures).

---

## Transport Policy

By default, plugins only operate over LAN and direct connections. Relay transport is excluded unless the plugin explicitly opts in via the Policy field on its Protocol declaration.

```go
const DefaultTransport = TransportLAN | TransportDirect
```

| Transport | Flag | Description |
|-----------|------|-------------|
| LAN | `sdk.TransportLAN` | Private/link-local network (same WiFi, same LAN) |
| Direct | `sdk.TransportDirect` | Public internet, direct peer-to-peer |
| Relay | `sdk.TransportRelay` | Mediated through a relay server (p2p-circuit) |

To allow relay transport for a protocol:

```go
plugin.Protocol{
    Name:    "file-transfer",
    Version: "1.0.0",
    Handler: handler,
    Policy:  &sdk.PluginPolicy{
        AllowedTransports: sdk.TransportLAN | sdk.TransportDirect | sdk.TransportRelay,
    },
}
```

When a relay connection requires a data access grant, the framework checks the peer's grant token before allowing the stream through.

---

## CLI Commands

Manage plugins through the `shurli plugin` command:

```bash
# List all plugins with their state
shurli plugin list
shurli plugin list --json

# Enable a plugin (starts it immediately)
shurli plugin enable <name>

# Disable a plugin (stops it, unregisters everything)
shurli plugin disable <name>

# Show detailed info about a plugin
shurli plugin info <name>
shurli plugin info <name> --json

# Emergency: disable ALL plugins immediately
shurli plugin disable-all
```

Example output:

```
$ shurli plugin list
NAME            VERSION  TYPE      STATE
filetransfer    1.0.0    built-in  active

$ shurli plugin info filetransfer
Name:        filetransfer
Version:     1.0.0
Type:        built-in
State:       active
Config key:  filetransfer
Commands:    [send download browse share transfers accept reject cancel clean]
Routes:      [GET /v1/shares POST /v1/shares DELETE /v1/shares ...]
Protocols:   [file-transfer/1.0.0 file-browse/1.0.0 file-download/1.0.0 file-multi-peer/1.0.0]
```

When the daemon is not running, `plugin list` and `plugin info` fall back to reading the config file and show which plugins are configured as enabled or disabled.

---

## Configuration

Plugins are configured in your `config.yaml` under the `plugins:` section. Each plugin gets its own key matching its Name().

```yaml
plugins:
  filetransfer:
    enabled: true
    receive_dir: ~/Downloads
    receive_mode: contacts    # off, contacts, ask, open, timed
    timed_duration: "10m"     # duration for timed mode
    max_file_size: 0          # 0 = unlimited
    compress: true
    bandwidth_budget: "1GB"   # daily budget per peer (unlimited, 500MB, 1GB, etc.)
    max_concurrent: 5
    notify: desktop           # none, desktop, command
    notify_command: ""        # custom command (shell metacharacters rejected)
    browse_rate_limit: 10     # requests per minute per peer
    rate_limit: 0             # per-peer transfer rate limit (bytes/sec, 0 = unlimited)
    multi_peer_enabled: true
    multi_peer_max_peers: 0   # 0 = auto
    multi_peer_min_size: 0    # minimum file size for multi-peer swarming
    erasure_overhead: 0.1     # 10% erasure coding overhead
    global_rate_limit: 0      # global transfer rate limit
    max_queued_per_peer: 0    # max pending transfers per peer
    min_speed_bytes: 0        # minimum speed before disconnect
    min_speed_seconds: 0      # grace period for min speed check
    max_temp_size: 0          # max temp directory size
    temp_file_expiry: ""      # e.g. "24h"
    default_persistent: true  # default for --persist flag on share add
    failure_backoff:
      threshold: 0
      window: ""              # e.g. "5m"
      block: ""               # e.g. "10m"
```

The `enabled` field is handled by the framework. Everything else is passed as raw YAML bytes to the plugin via PluginContext.Config(). The plugin parses its own config independently.

### Plugin config directory

Each plugin gets its own config directory at `~/.shurli/plugins/<plugin-id>/`:

```
~/.shurli/plugins/shurli.io/official/filetransfer/
  config.yaml       # plugin-specific config
  queue.json         # persistent transfer queue (HMAC-verified)
  shares.json        # shared file registry (HMAC-verified)
  logs/
    transfers.log    # transfer history
```

Directory permissions are enforced at 0700. The plugin framework rejects directories with looser permissions. Config file size is limited to 1MB.

### Hot reload

When the daemon's config is reloaded, each active plugin that registered an OnConfigReload callback is notified with its new config bytes. The file transfer plugin supports hot-reloading these fields without restart:

- `receive_mode` (including timed mode with duration)
- `receive_dir`
- `max_file_size`
- `compress`
- `notify`
- `notify_command` (shell metacharacters rejected)

If any field fails validation during reload, all changes are rolled back atomically.

---

## File Transfer: The Reference Plugin

The file transfer plugin (`plugins/filetransfer/`) is the first and currently only plugin. It demonstrates the full plugin pattern.

| Property | Value |
|----------|-------|
| ID | `shurli.io/official/filetransfer` |
| Name | `filetransfer` |
| Version | `1.0.0` |
| Config key | `filetransfer` |

**9 CLI commands**: send, download, browse, share (add/remove/list/deny), transfers, accept, reject, cancel, clean

**15 HTTP routes**: shares CRUD, browse, download, send, transfer management (list, history, pending, status, accept, reject, cancel), clean

**4 P2P protocols**: file-transfer/1.0.0, file-browse/1.0.0, file-download/1.0.0, file-multi-peer/1.0.0

**Optional interfaces implemented**:
- `StatusContributor`: adds `receive_mode` and `timed_mode_remaining_seconds` to daemon status
- `Checkpointer`: saves transfer snapshots for crash recovery

**Transport policy**: explicitly allows relay transport for all file transfer protocols (most plugins would not need this)

**Drain mechanism**: on Stop(), sets a drain gate to reject new HTTP requests, cancels the active context (signals all transfer goroutines), then waits up to 25 seconds for in-progress transfers to complete before the framework's 30-second drain timeout

The entire file transfer engine (TransferService, ShareRegistry, chunker, Merkle tree, compression, erasure coding, multi-peer, checkpoint/resume) lives in `plugins/filetransfer/`. The plugin owns both the protocol engine and the integration layer (daemon routes, CLI commands, protocol handlers). Generic SDK utilities (MerkleRoot, transport classification, relay grant interface) are imported from `pkg/sdk/`.
