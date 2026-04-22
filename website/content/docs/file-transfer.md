---
title: "File Transfer"
weight: 9
description: "Chunked P2P file transfer with BLAKE3 integrity, zstd compression, erasure coding, multi-source download, and AirDrop-style receive permissions."
---
<!-- Auto-synced from docs/FILE-TRANSFER.md by sync-docs - do not edit directly -->


Shurli includes a built-in file transfer plugin that sends files directly between peers over the P2P network. Files are chunked with FastCDC, compressed with zstd, and verified with a BLAKE3 Merkle tree. Relay is blocked for file transfer by default (drives own-relay adoption).

## Sending a File

```bash
# By peer name (fire-and-forget - exits immediately)
shurli send photo.jpg home-server

# With live progress
shurli send photo.jpg home-server --follow

# With priority (jumps the queue)
shurli send photo.jpg home-server --priority

# Send a directory
shurli send ./folder home-server

# JSON output (for scripting)
shurli send photo.jpg home-server --json
```

`shurli send` is fire-and-forget by default. The daemon handles the transfer in the background. Use `--follow` to watch progress inline.

## Receiving Files

Receive behavior is controlled by the **receive mode** (AirDrop-style):

| Mode | Behavior |
|------|----------|
| `off` | Reject all incoming transfers |
| `contacts` | Auto-accept from authorized peers (default) |
| `ask` | Queue for manual approval via `shurli accept`/`shurli reject` |
| `open` | Accept from any authorized peer without prompting |
| `timed` | Temporarily open, reverts to previous mode after duration |

Set the receive mode:
```bash
shurli config set transfer.receive_mode ask

# Timed mode: open for 10 minutes then revert
shurli config set transfer.receive_mode timed --duration 10m
```

**Default receive directory:** `~/Downloads/shurli/`

Change it with:
```bash
shurli config set transfer.receive_dir /path/to/your/dir
```

If a file with the same name already exists, Shurli creates `photo (1).jpg`, `photo (2).jpg`, etc.

## Managing Transfers

```bash
# View transfer inbox (pending + active)
shurli transfers

# Watch live (auto-refresh)
shurli transfers --watch

# View completed transfers
shurli transfers --history

# Accept a pending transfer
shurli accept <transfer-id>

# Accept all pending
shurli accept --all

# Reject a pending transfer
shurli reject <transfer-id>

# Cancel an outbound transfer
shurli cancel <transfer-id>
```

## Sharing Files

Share files for other peers to browse and download on demand:

```bash
# Share a file with all authorized peers
shurli share add /path/to/file.pdf

# Share with a specific peer only
shurli share add /path/to/file.pdf --to home-server

# List your shares
shurli share list

# Remove a share
shurli share remove /path/to/file.pdf

# Browse a peer's shared files
shurli browse home-server

# Download a specific file from a peer's shares
shurli download document.pdf home-server
```

Shares persist across daemon restarts (stored in `~/.shurli/shares.json`).

## Multi-Source Download

Download a file from multiple peers simultaneously using RaptorQ fountain codes:

```bash
shurli download large-file.zip home-server --multi-peer --peers home-server,laptop
```

Each peer contributes RaptorQ symbols. Any sufficient subset of symbols reconstructs the file. Faster than single-source for large files across multiple peers.

## Requirements

- Both peers must be running the daemon (`shurli daemon`)
- Peers must be paired (via `shurli invite` / `shurli join`)
- Works over LAN (mDNS) or direct connections. Relay is blocked by default.

## How It Works

**Chunking**: FastCDC content-defined chunking with 5 adaptive tiers (64KB-4MB based on file size). Single-pass with BLAKE3 hash per chunk.

**Integrity**: BLAKE3 Merkle tree over all chunk hashes. Root hash verified after all chunks received. Each chunk verified before writing to disk.

**Compression**: zstd compression on by default. Auto-detects incompressible data and skips re-compression. Bomb protection: decompression aborted if output exceeds 10x compressed size. Opt-out via `shurli config set transfer.compress false`.

**Erasure Coding**: Reed-Solomon erasure coding, auto-enabled on Direct WAN connections only. Recovers from lost chunks without retransmission. Wire overhead matches the configured `transfer.erasure_overhead` (default 10%); `transfer.bandwidth_budget` and per-peer `bandwidth_budget` ACL attributes are enforced on TOTAL wire bytes (data + parity), so a 100 MB file with 10% erasure consumes ~110 MB of budget. Memory footprint per erasure-coded transfer is bounded to roughly one stripe (≤~400 MB sustained, ≤~880 MB momentary during encode) via the incremental per-stripe encoder; LAN transfers skip erasure entirely and avoid this cost.

**Parallel Streams**: Adaptive parallel QUIC streams per transfer. Defaults: 8 on LAN (max 32), 4 on WAN (max 20). Minimum 4 chunks per stream to justify parallelism.

**Resume**: Checkpoint files (`.shurli-ckpt-<hash>`) store a bitfield of received chunks. Interrupted transfers resume from the last checkpoint. Checkpoints cleaned up on successful completion.

## Security

- **Integrity**: BLAKE3 Merkle tree verification. Corrupted chunks are rejected before writing to disk.
- **Path traversal**: Filenames like `../../../etc/passwd` are sanitized. Only the base filename is used. Receive directory is a jail.
- **Transport encryption**: All data travels over libp2p's encrypted transport (TLS 1.3 or Noise).
- **Authorization**: Only paired peers can send files. Unauthorized peers are silently rejected at the connection gating layer.
- **Resource limits**: Max 3 pending transfers per peer, 5 concurrent active, 1M chunk limit, 40MB manifest limit, 1h timeout.
- **Disk space**: Re-checked before each chunk write, not just at accept time.
- **Transfer IDs**: Random hex (`xfer-<12hex>`), not sequential (prevents enumeration).
- **Compression bombs**: zstd decompression capped at 10x ratio per chunk.
- **No symlink following** in share paths. Regular files only.

## Configuration

| Key | Default | Description |
|-----|---------|-------------|
| `transfer.receive_mode` | `contacts` | Receive mode: off, contacts, ask, open, timed |
| `transfer.receive_dir` | `~/Downloads/shurli/` | Directory for received files |
| `transfer.compress` | `true` | Enable zstd compression |
| `transfer.erasure_overhead` | `0.1` | Reed-Solomon parity ratio (0.0-0.5) |
| `transfer.max_concurrent` | `5` | Max concurrent outbound transfers |
| `transfer.max_file_size` | `0` (unlimited) | Max file size to accept (bytes) |
| `transfer.timed_duration` | `10m` | Default duration for timed receive mode |
| `transfer.notify` | `none` | Notification mode: none, desktop, command |
| `transfer.notify_command` | `""` | Command template with {from}, {file}, {size} |
| `transfer.log_path` | `~/.shurli/logs/transfers.log` | Transfer event log path |
| `transfer.multi_peer_enabled` | `true` | Enable multi-peer swarming downloads |
| `transfer.multi_peer_max_peers` | `4` | Max peers for multi-source download |

## Daemon API

For programmatic use (SDK consumers, scripts, other applications):

### Send a file

```bash
curl -X POST --unix-socket ~/.shurli/shurli.sock \
  http://localhost/v1/send \
  -H "Cookie: auth=$(cat ~/.shurli/.daemon-cookie)" \
  -H "Content-Type: application/json" \
  -d '{"file_path": "/absolute/path/to/file.pdf", "peer": "home-server"}'
```

Response:
```json
{
  "transfer_id": "xfer-a1b2c3d4e5f6"
}
```

### Check transfer progress

```bash
curl --unix-socket ~/.shurli/shurli.sock \
  "http://localhost/v1/transfers/xfer-a1b2c3d4e5f6" \
  -H "Cookie: auth=$(cat ~/.shurli/.daemon-cookie)"
```

### List all transfers

```bash
curl --unix-socket ~/.shurli/shurli.sock \
  http://localhost/v1/transfers \
  -H "Cookie: auth=$(cat ~/.shurli/.daemon-cookie)"
```

See the [Daemon API reference](/docs/daemon-api/) for the full list of 15 file transfer endpoints.

---

## Go API Reference

The file transfer engine lives in `plugins/filetransfer/`. Import path:

```go
import "github.com/shurlinet/shurli/plugins/filetransfer"
```

Generic SDK utilities (MerkleRoot, transport classification, relay grant interface) are imported from `pkg/sdk/`. See the [Go SDK reference](/docs/sdk/) for those types. See the [Plugin System](/docs/plugins/) for the plugin framework (Plugin interface, PluginContext, lifecycle, registration).

### type FileTransferPlugin

```go
type FileTransferPlugin struct {
    // unexported fields
}
```

Implements the Plugin interface. Owns the TransferService and ShareRegistry.

```go
func New() *FileTransferPlugin
```

Creates a new FileTransferPlugin instance. Called by the plugin registry at startup.

### type PluginConfig

```go
type PluginConfig struct {
    ReceiveDir        string   `yaml:"receive_dir"`
    MaxFileSize       int64    `yaml:"max_file_size"`
    ReceiveMode       string   `yaml:"receive_mode"`
    TimedDuration     string   `yaml:"timed_duration"`
    Compress          *bool    `yaml:"compress"`
    Notify            string   `yaml:"notify"`
    NotifyCommand     string   `yaml:"notify_command"`
    LogPath           string   `yaml:"log_path"`
    MaxConcurrent     int      `yaml:"max_concurrent"`
    RateLimit         int      `yaml:"rate_limit"`
    BrowseRateLimit   int      `yaml:"browse_rate_limit"`
    QueueFile         string   `yaml:"queue_file"`
    MultiPeerEnabled  *bool    `yaml:"multi_peer_enabled"`
    MultiPeerMaxPeers int      `yaml:"multi_peer_max_peers"`
    MultiPeerMinSize  int64    `yaml:"multi_peer_min_size"`
    ErasureOverhead   *float64 `yaml:"erasure_overhead"`
    GlobalRateLimit   int      `yaml:"global_rate_limit"`
    MaxQueuedPerPeer  int      `yaml:"max_queued_per_peer"`
    MinSpeedBytes     int      `yaml:"min_speed_bytes"`
    MinSpeedSeconds   int      `yaml:"min_speed_seconds"`
    MaxTempSize       int64    `yaml:"max_temp_size"`
    TempFileExpiry    string   `yaml:"temp_file_expiry"`
    BandwidthBudget   string   `yaml:"bandwidth_budget"`
    DefaultPersistent *bool    `yaml:"default_persistent"`
}
```

YAML configuration loaded from the plugin's config directory. Parsed by loadConfig(), hot-reloaded by reloadConfig().

---

### Protocol Constants

```go
const (
    TransferProtocol  = "/shurli/file-transfer/2.0.0"
    BrowseProtocol    = "/shurli/file-browse/1.0.0"
    DownloadProtocol  = "/shurli/file-download/1.0.0"
    MultiPeerProtocol = "/shurli/file-multi-peer/1.0.0"
    CancelProtocol    = "/shurli/transfer-cancel/1.0.0"
)
```

### Cancel Protocol

```go
func RegisterCancelHandler(h host.Host, ts *TransferService)
```

Registers the multi-path cancel protocol handler on the host. Called from plugin.Start(). Handles inbound cancel messages: reads a 32-byte transferID, verifies the sender matches the active transfer peer, and cancels the matching send or receive session. Rate-limited to 10 messages per peer per minute with a 5-second stream deadline.

```go
func UnregisterCancelHandler(h host.Host)
```

Removes the cancel protocol handler from the host. Called from plugin.Stop() to prevent handler access after TransferService is closed.

### Reject Reasons

```go
const (
    RejectReasonNone  byte = 0x00 // no reason disclosed
    RejectReasonSpace byte = 0x01 // insufficient disk space
    RejectReasonBusy  byte = 0x02 // receiver busy
    RejectReasonSize  byte = 0x03 // file too large
)
```

### type ReceiveMode

```go
type ReceiveMode string

const (
    ReceiveModeOff      ReceiveMode = "off"      // reject all
    ReceiveModeContacts ReceiveMode = "contacts"  // auto-accept from authorized peers (default)
    ReceiveModeAsk      ReceiveMode = "ask"       // queue for manual approval
    ReceiveModeOpen     ReceiveMode = "open"      // accept from any authorized peer
    ReceiveModeTimed    ReceiveMode = "timed"     // temporarily open, reverts after duration
)
```

### type TransferPriority

```go
type TransferPriority int

const (
    PriorityLow    TransferPriority = 0
    PriorityNormal TransferPriority = 1
    PriorityHigh   TransferPriority = 2
)
```

---

### type TransferConfig

```go
type TransferConfig struct {
    ReceiveDir        string              // directory for received files
    MaxSize           int64               // max file size (0 = unlimited)
    ReceiveMode       ReceiveMode         // default: contacts
    Compress          bool                // enable zstd compression (default: true)
    ErasureOverhead   float64             // RS parity overhead (0.10 = 10%, 0 = disabled)
    LogPath           string              // transfer event log path (empty = disabled)
    Notify            string              // "none" (default), "desktop", "command"
    NotifyCommand     string              // command template for "command" mode
    MaxConcurrent     int                 // max concurrent outbound transfers (default: 5)
    MultiPeerEnabled  bool                // enable multi-peer downloads (default: true)
    MultiPeerMaxPeers int                 // max peers for multi-peer (default: 4)
    MultiPeerMinSize  int64               // min file size for multi-peer (default: 10 MB)
    RateLimit         int                 // max requests per peer per minute (default: 600)
    GlobalRateLimit   int                 // max total inbound requests per minute (default: 600)
    MaxQueuedPerPeer  int                 // max pending+active per peer (default: 10)
    MinSpeedBytes     int                 // min transfer speed bytes/sec (default: 1024)
    MinSpeedSeconds   int                 // speed check window seconds (default: 30)
    MaxTempSize       int64               // max total .tmp size (default: 1GB)
    TempFileExpiry    time.Duration       // auto-expire old .tmp files (default: 1h)
    BandwidthBudget   int64               // max bytes per peer per hour (default: 100MB)
    PeerBudgetFunc    func(string) int64  // per-peer budget override (-1=unlimited, 0=global)
    FailureBackoffThreshold int           // fails to trigger block (default: 3)
    FailureBackoffWindow    time.Duration // failure counting window (default: 5m)
    FailureBackoffBlock     time.Duration // block duration (default: 60s)
    QueueFile         string              // persisted queue path (empty = disabled)
    QueueHMACKey      []byte              // 32-byte HMAC key for queue integrity
    GrantChecker      sdk.RelayGrantChecker // relay grant checker for budget/time checks
    ConnsToPeer       func(peer.ID) []network.Conn // returns connections to a peer
    HasVerifiedLANConn func(peer.ID) bool // true if peer has live mDNS-verified LAN connection
}
```

TransferConfig configures the transfer service. Passed to NewTransferService.

#### func NewTransferService

```go
func NewTransferService(cfg TransferConfig, metrics *sdk.Metrics, events *sdk.EventBus) (*TransferService, error)
```

Creates a new chunked transfer service. Returns error if receive directory cannot be created.

---

### type TransferService

```go
type TransferService struct {
    // unexported fields
}
```

Manages chunked file transfers over libp2p streams. Thread-safe.

#### Sending

```go
func (ts *TransferService) SendFile(s network.Stream, filePath string, opts ...SendOptions) (*TransferProgress, error)
```

Sends a file over a libp2p stream. Runs transfer in background goroutine.

```go
func (ts *TransferService) SendDirectory(ctx context.Context, dirPath string, openStream func() (network.Stream, error), opts SendOptions) ([]*TransferProgress, error)
```

Sends all files in a directory. Opens one stream per file.

```go
func (ts *TransferService) SubmitSend(filePath, peerID string, priority TransferPriority, openStream streamOpener, opts SendOptions) (*TransferProgress, error)
```

Enqueues outbound transfer to the queue processor.

#### Receiving

```go
func (ts *TransferService) HandleInbound() sdk.StreamHandler
```

Returns handler for inbound transfer protocol. Register with libp2p.

```go
func (ts *TransferService) ReceiveFrom(s network.Stream, remotePath, destDir string) (*TransferProgress, error)
```

Initiates receiver-side download from a peer's shared file.

```go
func (ts *TransferService) ProbeRootHash(openStream func() (network.Stream, error), remotePath string) ([32]byte, error)
```

Sends hash probe request and reads 45-byte response. Used by multi-peer download.

#### Transfer Management

```go
func (ts *TransferService) GetTransfer(id string) (*TransferProgress, bool)
func (ts *TransferService) ListTransfers() []TransferSnapshot
func (ts *TransferService) CancelTransfer(id string) error
func (ts *TransferService) CleanTempFiles() (int, int64)
func (ts *TransferService) Close() error
```

#### Configuration

```go
func (ts *TransferService) SetReceiveMode(mode ReceiveMode)
func (ts *TransferService) SetTimedMode(duration time.Duration) error
func (ts *TransferService) TimedModeRemaining() time.Duration
func (ts *TransferService) GetReceiveMode() ReceiveMode
func (ts *TransferService) SetReceiveDir(dir string)
func (ts *TransferService) SetCompress(enabled bool)
func (ts *TransferService) SetMaxSize(maxBytes int64)
func (ts *TransferService) SetNotifyMode(mode string)
func (ts *TransferService) SetNotifyCommand(cmd string)
func (ts *TransferService) ReceiveDir() string
```

#### Multi-Peer Download

### type MultiPeerStreamOpener

```go
type MultiPeerStreamOpener func(peerID peer.ID) (network.Stream, error)
```

Opens a stream to a specific peer for the multi-peer download protocol.

```go
func (ts *TransferService) MultiPeerEnabled() bool
func (ts *TransferService) MultiPeerMaxPeers() int
func (ts *TransferService) MultiPeerMinSize() int64
func (ts *TransferService) HandleMultiPeerRequest() sdk.StreamHandler
func (ts *TransferService) DownloadMultiPeer(ctx context.Context, rootHash [32]byte, peers []peer.ID, openStream MultiPeerStreamOpener, destDir string) (*TransferProgress, error)
```

#### Hash Registry

```go
func (ts *TransferService) RegisterHash(rootHash [32]byte, localPath string)
func (ts *TransferService) LookupHash(rootHash [32]byte) (string, bool)
```

Register hash-to-path mappings for multi-peer serving.

#### Queue

```go
func (ts *TransferService) LogPath() string
func (ts *TransferService) FlushQueue()
func (ts *TransferService) RequeuePersisted(streamFactory func(peerID string) func() (network.Stream, error))
```

#### Ask Mode

```go
func (ts *TransferService) ListPending() []PendingTransfer
func (ts *TransferService) AcceptTransfer(id, dest string) error
func (ts *TransferService) RejectTransfer(id string, reason byte) error
```

---

### type TransferProgress

```go
type TransferProgress struct {
    ID              string       `json:"id"`
    Filename        string       `json:"filename"`
    Size            int64        `json:"size"`
    Transferred     int64        `json:"transferred"`
    ChunksTotal     int          `json:"chunks_total"`
    ChunksDone      int          `json:"chunks_done"`
    Compressed      bool         `json:"compressed"`
    CompressedSize  int64        `json:"compressed_size,omitempty"`
    ErasureParity   int          `json:"erasure_parity,omitempty"`
    ErasureOverhead float64      `json:"erasure_overhead,omitempty"`
    StreamProgress  []StreamInfo `json:"stream_progress,omitempty"`
    PeerID          string       `json:"peer_id"`
    Direction       string       `json:"direction"`
    Status          string       `json:"status"`
    StartTime       time.Time    `json:"start_time"`
    Done            bool         `json:"done"`
    Error           string       `json:"error,omitempty"`
}
```

Tracks progress of an active transfer.

```go
func (p *TransferProgress) Snapshot() TransferSnapshot
func (p *TransferProgress) Sent() int64
```

### type TransferSnapshot

Same fields as TransferProgress. Mutex-free copy safe for JSON serialization and API responses.

### type StreamInfo

```go
type StreamInfo struct {
    ChunksDone int   `json:"chunks_done"`
    BytesDone  int64 `json:"bytes_done"`
}
```

Per-stream progress for parallel transfers.

### type PendingTransfer

```go
type PendingTransfer struct {
    ID       string    `json:"id"`
    Filename string    `json:"filename"`
    Size     int64     `json:"size"`
    PeerID   string    `json:"peer_id"`
    Time     time.Time `json:"time"`
}
```

Inbound transfer awaiting approval in ask mode.

### type SendOptions

```go
type SendOptions struct {
    NoCompress           bool         // disable compression for this transfer
    Streams              int          // parallel stream count (0 = adaptive default based on transport)
    StreamOpener         streamOpener // opens additional streams for parallel transfer
    RelativeName         string       // override manifest filename (for directory transfer)
    RateLimitBytesPerSec int64        // per-transfer send rate limit (0 = use service default)
}
```

---

### type ShareRegistry

```go
type ShareRegistry struct {
    // unexported fields
}
```

Manages shared paths with per-peer ACLs. Thread-safe. Persistent shares survive restarts.

#### func NewShareRegistry

```go
func NewShareRegistry() *ShareRegistry
```

Creates an empty share registry.

#### func LoadShareRegistry

```go
func LoadShareRegistry(path string) (*ShareRegistry, error)
```

Loads persistent shares from JSON file with HMAC verification.

#### Configuration

```go
func (r *ShareRegistry) SetPersistPath(path string)
func (r *ShareRegistry) SetHMACKey(key []byte)
func (r *ShareRegistry) SetBrowseRateLimit(maxPerMin int)
```

#### Share Management

```go
func (r *ShareRegistry) Share(path string, peers []peer.ID, persistent bool) error
func (r *ShareRegistry) Unshare(path string) error
func (r *ShareRegistry) DenyPeer(path string, peerID peer.ID) error
func (r *ShareRegistry) ListShares(forPeer *peer.ID) []*ShareEntry
func (r *ShareRegistry) LookupShare(path string) (*ShareEntry, bool)
func (r *ShareRegistry) LookupShareByID(shareID string, peerID peer.ID) (*ShareEntry, bool)
func (r *ShareRegistry) IsPathShared(path string, peerID peer.ID) bool
func (r *ShareRegistry) SavePersistent(path string) error
```

#### Protocol Handlers

```go
func (r *ShareRegistry) BrowseForPeer(peerID peer.ID) []BrowseEntry
func (r *ShareRegistry) HandleBrowse() sdk.StreamHandler
func (r *ShareRegistry) HandleDownload(ts *TransferService) sdk.StreamHandler
```

### type ShareEntry

```go
type ShareEntry struct {
    ID         string           `json:"id"`
    Path       string           `json:"path"`
    Name       string           `json:"name"`
    Peers      map[peer.ID]bool `json:"-"`
    PeerIDs    []string         `json:"peers"`
    Persistent bool             `json:"persistent"`
    SharedAt   time.Time        `json:"shared_at"`
    IsDir      bool             `json:"is_dir"`
}
```

Single shared path with its ACL. Path is never sent to peers. ID is opaque.

### type BrowseEntry

```go
type BrowseEntry struct {
    Name    string `json:"name"`
    Path    string `json:"path"`
    ShareID string `json:"share_id"`
    Size    int64  `json:"size"`
    IsDir   bool   `json:"is_dir"`
    ModTime int64  `json:"mod_time"`
}
```

Item in browse results. Path is always relative to share root.

### type BrowseResult

```go
type BrowseResult struct {
    Entries []BrowseEntry `json:"entries"`
    Error   string        `json:"error,omitempty"`
}
```

### type HashProbeResult

```go
type HashProbeResult struct {
    RootHash   [32]byte
    TotalSize  int64
    ChunkCount uint32
}
```

Response from hash probe request. Used by multi-peer download to discover Merkle root hash.

#### Client Functions

```go
func BrowsePeer(s network.Stream, subPath string) (*BrowseResult, error)
func RequestDownload(s network.Stream, remotePath string) (io.Reader, error)
func RequestProbe(s network.Stream, remotePath string) (*HashProbeResult, error)
```

---

### type TransferQueue

```go
type TransferQueue struct {
    // unexported fields
}
```

Manages ordered transfer execution with priority queue.

```go
func NewTransferQueue(maxActive int) *TransferQueue
func (q *TransferQueue) Enqueue(filePath, peerID, direction string, priority TransferPriority) (string, error)
func (q *TransferQueue) Dequeue() *QueuedTransfer
func (q *TransferQueue) Complete(id string)
func (q *TransferQueue) Requeue(id, filePath, peerID, direction string, priority TransferPriority)
func (q *TransferQueue) Cancel(id string) bool
func (q *TransferQueue) Pending() []*QueuedTransfer
func (q *TransferQueue) ActiveCount() int
```

### type QueuedTransfer

```go
type QueuedTransfer struct {
    ID        string           `json:"id"`
    FilePath  string           `json:"file_path"`
    PeerID    string           `json:"peer_id"`
    Priority  TransferPriority `json:"priority"`
    Direction string           `json:"direction"`
    QueuedAt  time.Time        `json:"queued_at"`
}
```

### type DirectoryTransfer

```go
type DirectoryTransfer struct {
    RootDir   string
    Files     []dirFileEntry
    TotalSize int64
}
```

```go
func WalkDirectory(dirPath string) (*DirectoryTransfer, error)
func (d *DirectoryTransfer) RegularFiles() []dirFileEntry
```

---

### type TransferEvent

```go
type TransferEvent struct {
    Timestamp time.Time `json:"timestamp"`
    EventType string    `json:"event_type"`
    Direction string    `json:"direction"`
    PeerID    string    `json:"peer_id"`
    FileName  string    `json:"file_name"`
    FileSize  int64     `json:"file_size,omitempty"`
    BytesDone int64     `json:"bytes_done,omitempty"`
    Error     string    `json:"error,omitempty"`
    Duration  string    `json:"duration,omitempty"`
}
```

Structured log entry for file transfer event.

#### Event Type Constants

```go
const (
    EventLogRequestReceived   = "request_received"
    EventLogAccepted          = "accepted"
    EventLogRejected          = "rejected"
    EventLogStarted           = "started"
    EventLogProgress25        = "progress_25"
    EventLogProgress50        = "progress_50"
    EventLogProgress75        = "progress_75"
    EventLogCompleted         = "completed"
    EventLogFailed            = "failed"
    EventLogResumed           = "resumed"
    EventLogCancelled         = "cancelled"
    EventLogSpamBlocked       = "spam_blocked"
    EventLogDiskSpaceRejected = "disk_space_rejected"
    EventLogMultiPeerRejected = "multi_peer_rejected"
    EventLogPathFailover      = "path_failover"
)
```

### type TransferLogger

```go
type TransferLogger struct {
    // unexported fields
}
```

JSON-lines logger for transfer events. Rotation: 10 MB per file, 3 rotated files.

```go
func NewTransferLogger(path string) (*TransferLogger, error)
func (l *TransferLogger) Log(event TransferEvent)
func (l *TransferLogger) Close() error
func ReadTransferEvents(path string, max int) ([]TransferEvent, error)
```

### type TransferNotifier

```go
type TransferNotifier struct {
    // unexported fields
}
```

Sends notifications on incoming file transfers. Modes: "none" (default), "desktop" (OS-native), "command" (user template).

```go
func NewTransferNotifier(mode, command string) *TransferNotifier
func (n *TransferNotifier) SetMode(mode string)
func (n *TransferNotifier) SetCommand(cmd string)
func (n *TransferNotifier) Notify(from, fileName string, fileSize int64) error
```

---

### type Chunk

```go
type Chunk struct {
    Data   []byte
    Hash   [32]byte
    Offset int64
}
```

Single content-defined chunk with BLAKE3 hash.

```go
func ChunkTarget(fileSize int64) (minSize, avgSize, maxSize int)
```

Selects adaptive chunk sizes (min/avg/max) based on file size across 5 tiers: 64K/128K/256K for <64MB, 128K/256K/512K for <512MB, 256K/512K/1M for <2GB, 512K/1M/2M for <8GB, 1M/2M/4M for >=8GB.

```go
func ChunkReader(r io.Reader, fileSize int64, cb func(Chunk) error) error
```

Reads from reader and produces content-defined chunks using FastCDC with BLAKE3. Single-pass: each byte is hashed as the chunk boundary is found.

---

### Utility Functions

```go
func RejectReasonString(reason byte) string
```

Returns human-readable string for reject reason byte.

```go
func SanitizeDisplayName(name string) string
```

Sanitizes filename for safe terminal display, stripping ANSI escapes, BiDi overrides, and zero-width characters.

---

### HTTP API Types

Request and response types used by the daemon HTTP API for file transfer endpoints.

```go
type SendRequest struct {
    Path       string `json:"path"`
    Peer       string `json:"peer"`
    NoCompress bool   `json:"no_compress"`
    Streams    int    `json:"streams"`
    Priority   string `json:"priority"`
}

type SendResponse struct {
    TransferID string `json:"transfer_id"`
    Filename   string `json:"filename"`
    Size       int64  `json:"size"`
    PeerID     string `json:"peer_id"`
}

type TransferAcceptRequest struct {
    Dest string `json:"dest,omitempty"`
}

type TransferRejectRequest struct {
    Reason string `json:"reason,omitempty"`
}

type PendingTransferInfo struct {
    ID       string `json:"id"`
    Filename string `json:"filename"`
    Size     int64  `json:"size"`
    PeerID   string `json:"peer_id"`
    Time     string `json:"time"`
}

type ShareRequest struct {
    Path       string   `json:"path"`
    Peers      []string `json:"peers,omitempty"`
    Persistent *bool    `json:"persistent,omitempty"`
}

type UnshareRequest struct {
    Path string `json:"path"`
}

type ShareDenyRequest struct {
    Path string `json:"path"` // shared path
    Peer string `json:"peer"` // peer name or ID to remove
}

type ShareInfo struct {
    Path       string   `json:"path"`
    Peers      []string `json:"peers,omitempty"`
    Persistent bool     `json:"persistent"`
    IsDir      bool     `json:"is_dir"`
    SharedAt   string   `json:"shared_at"`
}

type BrowseRequest struct {
    Peer    string `json:"peer"`
    SubPath string `json:"sub_path,omitempty"`
}

type BrowseResponse struct {
    Entries []BrowseEntry `json:"entries"`
    Error   string        `json:"error,omitempty"`
}

type DownloadRequest struct {
    Peer       string   `json:"peer"`
    RemotePath string   `json:"remote_path"`
    LocalDest  string   `json:"local_dest"`
    MultiPeer  bool     `json:"multi_peer,omitempty"`
    ExtraPeers []string `json:"extra_peers,omitempty"`
}

type DownloadResponse struct {
    TransferID string `json:"transfer_id"`
    FileName   string `json:"filename"`
    FileSize   int64  `json:"file_size"`
}
```

### Security Limits

```go
const (
    maxFilenameLen         = 4096     // max filename length in bytes
    maxFileSize            = 1 << 40  // 1 TB max single file
    maxChunkCount          = 1 << 20  // 1M chunks max per transfer
    maxManifestSize        = 40 << 20 // 40 MB max manifest wire size
    maxChunkWireSize       = 4 << 20  // 4 MB max single chunk on wire
    maxDecompressedChunk   = 8 << 20  // 8 MB max decompressed chunk
    maxConcurrentTransfers = 10       // global inbound transfer limit
    maxPerPeerTransfers    = 3        // per-peer inbound limit
    maxTrackedTransfers    = 10000    // max tracked transfer entries
)
```
