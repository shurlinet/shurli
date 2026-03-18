// Package filetransfer implements the file transfer plugin for Shurli.
//
// SECURITY INVARIANTS (Layer 1 - compiled-in plugin):
//  1. Plugin never accesses daemon auth tokens, cookie paths, vault keys, or Ed25519 private keys.
//  2. Plugin uses DeriveKey() for cryptographic keys (HKDF-SHA256), never raw identity material.
//  3. Plugin routes are wrapped with daemon auth middleware - plugins do NOT implement auth.
//  4. Plugin protocols are wrapped with state checking - streams only handled in ACTIVE state.
package filetransfer

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	libp2pnet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/pkg/p2pnet"
	"github.com/shurlinet/shurli/pkg/plugin"
)

// FileTransferPlugin implements the file transfer, sharing, and browse features.
type FileTransferPlugin struct {
	ctx             *plugin.PluginContext
	configDir       string

	// mu protects all mutable fields below (P1 fix: zero synchronization).
	// Handlers take RLock, Stop() takes full Lock when nilling fields.
	mu              sync.RWMutex
	network         *p2pnet.Network
	transferService *p2pnet.TransferService
	shareRegistry   *p2pnet.ShareRegistry
	config          TransferConfig

	// Drain mechanism (Finding 52 - CRITICAL).
	// Plugin owns context.Context + sync.WaitGroup for active transfers.
	// Start() creates cancelable context, passes to ALL TransferService operations.
	// Stop() cancels context, waits on WaitGroup (25s budget within 30s drain timeout).
	activeCtx    context.Context
	activeCancel context.CancelFunc
	wg           sync.WaitGroup

	// drainGate is set to true when Stop() begins. wrapHandler checks this
	// before wg.Add(1) to prevent new work after drain starts (P2 fix).
	drainGate    bool
}

// New creates a new FileTransferPlugin.
func New() *FileTransferPlugin {
	return &FileTransferPlugin{}
}

func (p *FileTransferPlugin) ID() string            { return "shurli.io/official/filetransfer" }
func (p *FileTransferPlugin) Name() string          { return "filetransfer" }
func (p *FileTransferPlugin) Version() string       { return "2.0.0" }
func (p *FileTransferPlugin) ConfigSection() string { return "filetransfer" }

// Init is called ONCE at load time. Receives PluginContext, parses config.
func (p *FileTransferPlugin) Init(ctx *plugin.PluginContext) error {
	p.ctx = ctx
	p.configDir = ctx.ConfigDir()
	p.config = loadConfig(ctx.Config())

	// M4 fix: warn if legacy config files exist in parent config dir.
	if p.configDir != "" {
		parentDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(p.configDir)))) // up from plugins/shurli.io/official/filetransfer
		for _, legacy := range []string{"queue.json", "shares.json"} {
			legacyPath := filepath.Join(parentDir, legacy)
			if _, err := os.Stat(legacyPath); err == nil {
				slog.Warn("plugin.filetransfer: legacy file found in parent config dir",
					"file", legacyPath, "note", "this file is no longer used; plugin config is now in "+p.configDir)
			}
		}
	}

	// Register hot-reload callback.
	ctx.OnConfigReload(p.reloadConfig)

	return nil
}

// Start is called on enable. Creates TransferService, ShareRegistry, loads state.
func (p *FileTransferPlugin) Start() error {
	// Reset drain gate for fresh enable cycle.
	p.mu.Lock()
	p.drainGate = false
	p.mu.Unlock()

	// Create cancelable context for drain mechanism.
	p.activeCtx, p.activeCancel = context.WithCancel(context.Background())

	p.network = p.ctx.EngineHost()
	if p.network == nil {
		return fmt.Errorf("network not available")
	}

	// Clean stale checkpoints from previous crash.
	if p.configDir != "" {
		cleanStaleCheckpoints(p.configDir)
	}

	// Build TransferService config.
	compress := true
	if p.config.Compress != nil {
		compress = *p.config.Compress
	}

	erasureOverhead := 0.1
	if p.config.ErasureOverhead != nil {
		erasureOverhead = *p.config.ErasureOverhead
	}

	multiPeerEnabled := true
	if p.config.MultiPeerEnabled != nil {
		multiPeerEnabled = *p.config.MultiPeerEnabled
	}

	logPath := p.config.LogPath
	if logPath == "" && p.configDir != "" {
		logPath = filepath.Join(p.configDir, "logs", "transfers.log")
	}

	// Parse failure backoff.
	var fbThreshold int
	var fbWindow, fbBlock time.Duration
	fb := p.config.FailureBackoff
	fbThreshold = fb.Threshold
	if fb.Window != "" {
		if d, err := time.ParseDuration(fb.Window); err == nil {
			fbWindow = d
		}
	}
	if fb.Block != "" {
		if d, err := time.ParseDuration(fb.Block); err == nil {
			fbBlock = d
		}
	}

	var tempExpiry time.Duration
	if p.config.TempFileExpiry != "" {
		if d, err := time.ParseDuration(p.config.TempFileExpiry); err == nil {
			tempExpiry = d
		}
	}

	// Derive HMAC key for queue persistence using HKDF.
	var queueHMACKey []byte
	if key := p.ctx.DeriveKey("shurli/queue/v1"); key != nil {
		queueHMACKey = key
	} else {
		// Fallback: derive from node's peer ID so each node has a unique key.
		// Not as strong as HKDF from the private key, but prevents trivial forgery.
		h := sha256.Sum256([]byte("shurli/queue/v1/" + p.network.Host().ID().String()))
		queueHMACKey = h[:]
	}

	queueFile := p.config.QueueFile
	if queueFile == "" && p.configDir != "" {
		queueFile = filepath.Join(p.configDir, "queue.json")
	}

	cfg := p2pnet.TransferConfig{
		ReceiveDir:        p.config.ReceiveDir,
		MaxSize:           p.config.MaxFileSize,
		ReceiveMode:       p2pnet.ReceiveMode(p.config.ReceiveMode),
		Compress:          compress,
		ErasureOverhead:   erasureOverhead,
		LogPath:           logPath,
		Notify:            p.config.Notify,
		NotifyCommand:     sanitizeNotifyCommand(p.config.NotifyCommand),
		MaxConcurrent:     p.config.MaxConcurrent,
		MultiPeerEnabled:  multiPeerEnabled,
		MultiPeerMaxPeers: p.config.MultiPeerMaxPeers,
		MultiPeerMinSize:  p.config.MultiPeerMinSize,
		RateLimit:         p.config.RateLimit,

		// DDoS defenses.
		GlobalRateLimit:         p.config.GlobalRateLimit,
		MaxQueuedPerPeer:        p.config.MaxQueuedPerPeer,
		MinSpeedBytes:           p.config.MinSpeedBytes,
		MinSpeedSeconds:         p.config.MinSpeedSeconds,
		MaxTempSize:             p.config.MaxTempSize,
		TempFileExpiry:          tempExpiry,
		BandwidthBudget:         p.config.BandwidthBudget,
		FailureBackoffThreshold: fbThreshold,
		FailureBackoffWindow:    fbWindow,
		FailureBackoffBlock:     fbBlock,

		// Queue persistence.
		QueueFile:    queueFile,
		QueueHMACKey: queueHMACKey,
	}

	ts, err := p2pnet.NewTransferService(cfg, nil, p.network.Events())
	if err != nil {
		return fmt.Errorf("create transfer service: %w", err)
	}
	p.transferService = ts

	// If config specifies timed mode at startup, activate the timer.
	if cfg.ReceiveMode == p2pnet.ReceiveModeTimed {
		durStr := p.config.TimedDuration
		if durStr == "" {
			durStr = "10m"
		}
		if dur, parseErr := time.ParseDuration(durStr); parseErr == nil {
			if timedErr := ts.SetTimedMode(dur); timedErr != nil {
				slog.Warn("plugin.filetransfer: timed mode failed", "error", timedErr)
			}
		}
	}

	// Load/create share registry.
	if p.configDir != "" {
		persistPath := filepath.Join(p.configDir, "shares.json")
		reg, loadErr := p2pnet.LoadShareRegistry(persistPath)
		if loadErr != nil {
			slog.Warn("plugin.filetransfer: failed to load shares", "error", loadErr)
			reg = p2pnet.NewShareRegistry()
			reg.SetPersistPath(persistPath)
		}

		// P6 fix: set HMAC key for shares.json integrity.
		if sharesKey := p.ctx.DeriveKey("shurli/shares/v1"); sharesKey != nil {
			reg.SetHMACKey(sharesKey)
		}

		p.shareRegistry = reg

		browseLimit := p.config.BrowseRateLimit
		if browseLimit == 0 {
			browseLimit = 10
		}
		if browseLimit > 0 {
			reg.SetBrowseRateLimit(browseLimit)
		}
	}

	slog.Info("plugin.filetransfer: started", "receive_dir", cfg.ReceiveDir, "receive_mode", string(cfg.ReceiveMode))
	return nil
}

// Stop persists state and releases resources. Called on disable.
func (p *FileTransferPlugin) Stop() error {
	// Set drain gate to reject new handler work (P2 fix).
	p.mu.Lock()
	p.drainGate = true
	p.mu.Unlock()

	// Cancel active context (signals all transfer goroutines to stop).
	if p.activeCancel != nil {
		p.activeCancel()
	}

	// G8 fix: plugin drain timeout is 5s less than registry's 30s drain timeout,
	// ensuring plugin completes before registry force-transitions to STOPPED.
	pluginDrainTimeout := 25 * time.Second
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(pluginDrainTimeout):
		slog.Warn("plugin.filetransfer: drain timeout exceeded, forcing stop",
			"timeout", pluginDrainTimeout.String())
	}

	// Persist state and nil fields under full lock.
	p.mu.Lock()
	defer p.mu.Unlock()

	// P3 fix: persist queue before Close() so queue state survives shutdown.
	// G9 note: FlushQueue only persists pending (not-yet-started) queue items.
	// In-progress transfers were already dequeued and are tracked separately.
	// After activeCtx cancel, in-progress transfers are cancelled but pending
	// items are still valid for re-queue on next startup.
	if p.transferService != nil {
		p.transferService.FlushQueue()
	}

	if p.shareRegistry != nil && p.configDir != "" {
		persistPath := filepath.Join(p.configDir, "shares.json")
		if err := p.shareRegistry.SavePersistent(persistPath); err != nil {
			slog.Warn("plugin.filetransfer: failed to save shares", "error", err)
		}
	}

	if p.transferService != nil {
		if err := p.transferService.Close(); err != nil {
			slog.Warn("plugin.filetransfer: close failed", "error", err)
		}
	}

	p.transferService = nil
	p.shareRegistry = nil
	p.network = nil

	slog.Info("plugin.filetransfer: stopped")
	return nil
}

// OnNetworkReady is called after bootstrap. Requeues persisted transfers.
func (p *FileTransferPlugin) OnNetworkReady() error {
	p.mu.RLock()
	ts := p.transferService
	pnet := p.network
	p.mu.RUnlock()

	if ts == nil || pnet == nil {
		return nil
	}

	// Re-enqueue persisted transfers now that the network is ready.
	// Uses 1.0.0 protocol version (Decision 4), NOT the TransferProtocol constant (2.0.0).
	// P18 fix: use OpenPluginStream instead of direct host stream creation.
	ts.RequeuePersisted(func(peerID string) func() (libp2pnet.Stream, error) {
		return func() (libp2pnet.Stream, error) {
			pid, err := peer.Decode(peerID)
			if err != nil {
				return nil, fmt.Errorf("decode peer ID: %w", err)
			}
			return pnet.OpenPluginStream(p.activeCtx, pid, "file-transfer")
		}
	})

	return nil
}

// Commands returns the 9 CLI commands this plugin provides.
func (p *FileTransferPlugin) Commands() []plugin.Command {
	return []plugin.Command{
		{Name: "send", Description: "Send a file to a peer", Usage: "shurli send <file> <peer> [--follow] [--json]"},
		{Name: "download", Description: "Download from peer's shared files", Usage: "shurli download <peer>:<path> [--dest dir]"},
		{Name: "browse", Description: "Browse peer's shared files", Usage: "shurli browse <peer> [path]"},
		{Name: "share", Description: "Manage shared files", Usage: "shurli share <add|remove|list> ..."},
		{Name: "transfers", Description: "List active transfers", Usage: "shurli transfers [--watch] [--json]"},
		{Name: "accept", Description: "Accept a pending transfer", Usage: "shurli accept <id|--all>"},
		{Name: "reject", Description: "Reject a pending transfer", Usage: "shurli reject <id|--all>"},
		{Name: "cancel", Description: "Cancel a transfer", Usage: "shurli cancel <id>"},
		{Name: "clean", Description: "Clean temp files", Usage: "shurli clean [--json]"},
	}
}

// Routes returns the 14 HTTP endpoints this plugin provides.
// All handlers are wrapped with wrapHandler for drain WaitGroup tracking (C1 fix).
func (p *FileTransferPlugin) Routes() []plugin.Route {
	return []plugin.Route{
		{Method: "GET", Path: "/v1/shares", Handler: p.wrapHandler(p.handleShareList)},
		{Method: "POST", Path: "/v1/shares", Handler: p.wrapHandler(p.handleShareAdd)},
		{Method: "DELETE", Path: "/v1/shares", Handler: p.wrapHandler(p.handleShareRemove)},
		{Method: "POST", Path: "/v1/browse", Handler: p.wrapHandler(p.handleBrowse)},
		{Method: "POST", Path: "/v1/download", Handler: p.wrapHandler(p.handleDownload)},
		{Method: "POST", Path: "/v1/send", Handler: p.wrapHandler(p.handleSend)},
		{Method: "GET", Path: "/v1/transfers", Handler: p.wrapHandler(p.handleTransferList)},
		{Method: "GET", Path: "/v1/transfers/history", Handler: p.wrapHandler(p.handleTransferHistory)},
		{Method: "GET", Path: "/v1/transfers/pending", Handler: p.wrapHandler(p.handleTransferPending)},
		{Method: "GET", Path: "/v1/transfers/{id}", Handler: p.wrapHandler(p.handleTransferStatus)},
		{Method: "POST", Path: "/v1/transfers/{id}/accept", Handler: p.wrapHandler(p.handleTransferAccept)},
		{Method: "POST", Path: "/v1/transfers/{id}/reject", Handler: p.wrapHandler(p.handleTransferReject)},
		{Method: "POST", Path: "/v1/transfers/{id}/cancel", Handler: p.wrapHandler(p.handleTransferCancel)},
		{Method: "POST", Path: "/v1/clean", Handler: p.wrapHandler(p.handleClean)},
	}
}

// Protocols returns the 4 P2P stream handlers this plugin provides.
// All use version 1.0.0 (Decision 4).
func (p *FileTransferPlugin) Protocols() []plugin.Protocol {
	p.mu.RLock()
	ts := p.transferService
	sr := p.shareRegistry
	p.mu.RUnlock()

	var protos []plugin.Protocol

	if ts != nil {
		protos = append(protos,
			plugin.Protocol{Name: "file-transfer", Version: "1.0.0", Handler: ts.HandleInbound()},
			plugin.Protocol{Name: "file-multi-peer", Version: "1.0.0", Handler: ts.HandleMultiPeerRequest()},
		)
	}

	if sr != nil {
		protos = append(protos,
			plugin.Protocol{Name: "file-browse", Version: "1.0.0", Handler: sr.HandleBrowse()},
		)
		if ts != nil {
			protos = append(protos,
				plugin.Protocol{Name: "file-download", Version: "1.0.0", Handler: sr.HandleDownload(ts)},
			)
		}
	}

	return protos
}

// StatusFields implements the StatusContributor interface.
// Returns receive_mode + timed_mode_remaining_seconds for the daemon status response.
func (p *FileTransferPlugin) StatusFields() map[string]any {
	p.mu.RLock()
	ts := p.transferService
	p.mu.RUnlock()

	if ts == nil {
		return nil
	}
	fields := map[string]any{
		"receive_mode": string(ts.GetReceiveMode()),
	}
	if remaining := ts.TimedModeRemaining(); remaining > 0 {
		fields["timed_mode_remaining_seconds"] = int(remaining.Seconds())
	}
	return fields
}

// wrapHandler adds drain WaitGroup tracking and mutex protection around HTTP handler calls.
// Checks drain gate before admitting new work (P2 fix).
func (p *FileTransferPlugin) wrapHandler(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.mu.RLock()
		if p.drainGate {
			p.mu.RUnlock()
			http.Error(w, "plugin is shutting down", http.StatusServiceUnavailable)
			return
		}
		p.wg.Add(1)
		p.mu.RUnlock()
		defer p.wg.Done()
		handler(w, r)
	}
}

// Ensure types satisfy interface at compile time.
var _ plugin.Plugin = (*FileTransferPlugin)(nil)
var _ plugin.StatusContributor = (*FileTransferPlugin)(nil)

// osExit is a testable exit function.
var osExit = os.Exit
