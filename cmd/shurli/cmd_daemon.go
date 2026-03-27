package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"golang.org/x/crypto/hkdf"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/daemon"
	"github.com/shurlinet/shurli/internal/grants"
	"github.com/shurlinet/shurli/internal/macaroon"
	"github.com/shurlinet/shurli/internal/notify"
	"github.com/shurlinet/shurli/internal/watchdog"
	"github.com/shurlinet/shurli/pkg/sdk"
	"github.com/shurlinet/shurli/pkg/plugin"
	"github.com/shurlinet/shurli/plugins"
)

// --- RuntimeInfo adapter (implements daemon.RuntimeInfo on serveRuntime) ---

func (rt *serveRuntime) Network() *sdk.Network            { return rt.network }
func (rt *serveRuntime) ConfigFile() string                   { return rt.configFile }
func (rt *serveRuntime) AuthKeysPath() string                 { return rt.authKeys }
func (rt *serveRuntime) Version() string                      { return rt.version }
func (rt *serveRuntime) StartTime() time.Time                 { return rt.startTime }
func (rt *serveRuntime) PingProtocolID() string               { return rt.config.Protocols.PingPong.ID }
func (rt *serveRuntime) Interfaces() *sdk.InterfaceSummary { return rt.ifSummary }
func (rt *serveRuntime) PathTracker() *sdk.PathTracker         { return rt.pathTracker }
func (rt *serveRuntime) BandwidthTracker() *sdk.BandwidthTracker { return rt.bwTracker }
func (rt *serveRuntime) RelayHealth() *sdk.RelayHealth           { return rt.relayHealth }
func (rt *serveRuntime) STUNResult() *sdk.STUNResult {
	if rt.stunProber == nil {
		return nil
	}
	return rt.stunProber.Result()
}
func (rt *serveRuntime) IsRelaying() bool {
	if rt.peerRelay == nil {
		return false
	}
	return rt.peerRelay.Enabled()
}

func (rt *serveRuntime) RelayAddresses() []string    { return rt.config.Relay.Addresses }
func (rt *serveRuntime) DiscoveryNetwork() string     { return rt.config.Discovery.Network }
func (rt *serveRuntime) GrantStore() *grants.Store              { return rt.grantStore }
func (rt *serveRuntime) GrantPouch() *grants.Pouch              { return rt.grantPouch }
func (rt *serveRuntime) GrantProtocol() *grants.GrantProtocol   { return rt.grantProtocol }
func (rt *serveRuntime) GrantsAutoRefresh() bool                { return rt.config.Grants.AutoRefresh }
func (rt *serveRuntime) GrantsMaxRefreshDuration() string       { return rt.config.Grants.MaxRefreshDuration }
func (rt *serveRuntime) NotifyRouter() *notify.Router            { return rt.notifyRouter }
func (rt *serveRuntime) PeerManager() *sdk.PeerManager        { return rt.peerManager }
func (rt *serveRuntime) GrantCacheSnapshot() []*grants.GrantReceipt {
	if rt.grantCache == nil {
		return nil
	}
	return rt.grantCache.All()
}

func (rt *serveRuntime) RelayMOTDs() []daemon.MOTDInfo {
	if rt.motdClient == nil {
		return nil
	}

	h := rt.network.Host()
	var result []daemon.MOTDInfo

	for _, addrStr := range rt.config.Relay.Addresses {
		maddr, err := ma.NewMultiaddr(addrStr)
		if err != nil {
			continue
		}
		info, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			continue
		}
		relayPeer := info.ID

		// Parse relay name from AgentVersion.
		relayName := ""
		if av, avErr := h.Peerstore().Get(relayPeer, "AgentVersion"); avErr == nil {
			if s, ok := av.(string); ok {
				relayName = parseRelayName(s)
			}
		}

		// Check for goodbye (persisted to disk).
		if msg, ts, ok := rt.motdClient.GetStoredGoodbye(relayPeer); ok {
			result = append(result, daemon.MOTDInfo{
				RelayPeerID: relayPeer.String(),
				RelayName:   relayName,
				Message:     msg,
				Type:        "goodbye",
				Timestamp:   time.Unix(ts, 0).UTC().Format(time.RFC3339),
			})
			continue // goodbye supersedes MOTD
		}

		// Check for MOTD (in-memory, 24h window).
		if msg, ts, ok := rt.motdClient.GetLastMOTD(relayPeer); ok {
			result = append(result, daemon.MOTDInfo{
				RelayPeerID: relayPeer.String(),
				RelayName:   relayName,
				Message:     msg,
				Type:        "motd",
				Timestamp:   time.Unix(ts, 0).UTC().Format(time.RFC3339),
			})
		}
	}
	return result
}

// parseRelayName extracts the name from a relay UserAgent like "relay-server/0.1.0 (AU-Sydney)".
func parseRelayName(agentVersion string) string {
	start := strings.Index(agentVersion, "(")
	end := strings.LastIndex(agentVersion, ")")
	if start >= 0 && end > start+1 {
		return agentVersion[start+1 : end]
	}
	return ""
}

func (rt *serveRuntime) ConfigReloader() daemon.ConfigReloader {
	return &configReloader{rt: rt}
}

func (rt *serveRuntime) GaterForHotReload() daemon.GaterReloader {
	if rt.gater == nil || rt.authKeys == "" {
		return nil
	}
	return &gaterReloader{
		gater:        rt.gater,
		authKeysPath: rt.authKeys,
		peerManager:  rt.peerManager,
	}
}

// gaterReloader implements daemon.GaterReloader by re-reading the
// authorized_keys file and updating the live connection gater.
// Also syncs PeerManager's watchlist so newly authorized peers
// are immediately reconnected.
type gaterReloader struct {
	gater        *auth.AuthorizedPeerGater
	authKeysPath string
	peerManager  *sdk.PeerManager // nil-safe
}

func (g *gaterReloader) ReloadFromFile() error {
	peers, err := auth.LoadAuthorizedKeys(g.authKeysPath)
	if err != nil {
		return fmt.Errorf("failed to reload authorized_keys: %w", err)
	}
	g.gater.UpdateAuthorizedPeers(peers)
	if g.peerManager != nil {
		g.peerManager.SetWatchlist(g.gater.GetAuthorizedPeerIDs())
	}
	return nil
}

// configReloader implements daemon.ConfigReloader by re-reading the
// config file from disk and cascading changes to live subsystems.
type configReloader struct {
	rt *serveRuntime
}

func (cr *configReloader) ReloadConfig() (*daemon.ConfigReloadResult, error) {
	result := &daemon.ConfigReloadResult{}

	// Re-read config from disk.
	newCfg, err := config.LoadNodeConfig(cr.rt.configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	config.ResolveConfigPaths(newCfg, filepath.Dir(cr.rt.configFile))
	if err := config.ValidateNodeConfig(newCfg); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	// Transfer config reload is now handled by the filetransfer plugin's
	// OnConfigReload callback, triggered via registry.NotifyConfigReload()
	// in handleConfigReload after this function returns.

	// Authorized keys (connection gating) - always refresh on reload.
	if reloader := cr.rt.GaterForHotReload(); reloader != nil {
		if err := reloader.ReloadFromFile(); err != nil {
			return nil, fmt.Errorf("authorized_keys reload failed: %w", err)
		}
		result.Changed = append(result.Changed, "security.authorized_keys")
	}

	// Reload name mappings from config so auth add --comment changes
	// take effect without daemon restart. Uses ReplaceNames (not LoadNames)
	// so that removed names are cleared from memory.
	if cr.rt.network != nil && newCfg.Names != nil {
		if err := cr.rt.network.ReplaceNames(newCfg.Names); err != nil {
			slog.Warn("failed to reload names from config", "err", err)
		} else {
			result.Changed = append(result.Changed, "names")
		}
	}

	// Update the stored config pointer for future comparisons.
	cr.rt.config = newCfg

	if len(result.Changed) == 0 {
		result.Changed = []string{} // empty slice, not nil (cleaner JSON)
	}
	return result, nil
}

// --- Daemon paths ---

func daemonSocketPath() string {
	return filepath.Join(daemonConfigDir(), "shurli.sock")
}

func daemonCookiePath() string {
	return filepath.Join(daemonConfigDir(), ".daemon-cookie")
}

// daemonConfigDir returns the directory where the daemon stores its socket and cookie.
// It uses FindConfigFile to locate the actual config, so the socket ends up next to
// the config file regardless of whether it's in /etc/shurli/ or ~/.shurli/.
func daemonConfigDir() string {
	cfgFile, err := config.FindConfigFile("")
	if err == nil {
		return filepath.Dir(cfgFile)
	}
	dir, dirErr := config.DefaultConfigDir()
	if dirErr != nil {
		fatal("Cannot determine config directory: %v", dirErr)
	}
	return dir
}

// --- Main daemon entry ---

func runDaemon(args []string) {
	// If no subcommand or "start", run the daemon foreground.
	if len(args) == 0 {
		runDaemonStart(args)
		return
	}

	switch args[0] {
	case "start":
		runDaemonStart(args[1:])
	case "status":
		runDaemonStatus(args[1:])
	case "stop":
		runDaemonStop()
	case "ping":
		runDaemonPing(args[1:])
	case "services":
		runDaemonServices(args[1:])
	case "peers":
		runDaemonPeers(args[1:])
	case "paths":
		runDaemonPaths(args[1:])
	case "connect":
		runDaemonConnect(args[1:])
	case "disconnect":
		runDaemonDisconnect(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown daemon subcommand: %s\n\n", args[0])
		printDaemonUsage()
		osExit(1)
	}
}

func printDaemonUsage() {
	fmt.Println("Usage: shurli daemon [subcommand]")
	fmt.Println()
	fmt.Println("  (no subcommand)  Start daemon in foreground")
	fmt.Println("  start            Start daemon in foreground")
	fmt.Println("  status [--json]  Show daemon status")
	fmt.Println("  stop             Graceful shutdown")
	fmt.Println("  ping <peer> [-c N] [--interval 1s] [--json]")
	fmt.Println("  services [--json]")
	fmt.Println("  peers [--all] [--json]")
	fmt.Println("  paths [--json]")
	fmt.Println("  connect --peer <name> --service <svc> --listen <addr>")
	fmt.Println("  disconnect <id>")
}

// --- Start daemon (foreground) ---

func runDaemonStart(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fs.Parse(reorderFlags(fs, args))

	fmt.Printf("shurli daemon %s (%s)\n", version, commit)
	fmt.Println()

	ctx, cancel := context.WithCancel(context.Background())

	rt, err := newServeRuntime(ctx, cancel, *configFlag, version)
	if err != nil {
		cancel()
		fatal("Failed to start: %v", err)
	}

	// Register protocol handlers BEFORE Bootstrap so they're ready when
	// the relay fires reconnect-notifier on our connection. Without this,
	// the relay tries to deliver peer introductions before the handler exists.
	rt.SetupPingPong()
	rt.SetupPeerNotify()
	rt.setupGrantReceiptHandler() // not gated by authKeys - any node can use relays
	rt.SetupMOTDClient()

	// Check plugin directory permissions (for future WASM plugins).
	// Layer 2 will make this a hard error; for now it's a warning.
	pluginDir := filepath.Join(filepath.Dir(rt.configFile), "plugins")
	if info, err := os.Stat(pluginDir); err == nil {
		if info.Mode().Perm() != 0700 {
			slog.Warn("plugin.dir-perms",
				"path", pluginDir,
				"perms", fmt.Sprintf("%04o", info.Mode().Perm()),
				"expected", "0700")
		}
	}

	// Start reputation score computation (PeerHistory -> ComputeScore pipeline).
	// Returns a resolver function that plugins use via PluginContext.peerScore().
	scoreResolver := rt.StartReputationScoreUpdater()

	// Create plugin registry and register compiled-in plugins.
	pluginProvider := &plugin.ContextProvider{
		Network:         rt.network,
		ServiceRegistry: rt.network.ServiceRegistry(),
		ConfigDir:       filepath.Dir(rt.configFile),
		NameResolver:    rt.network.ResolveName,
		PeerConnector:   rt.ConnectToPeer,
		ScoreResolver:   scoreResolver,
	}

	// Wire peer attribute resolver for per-peer settings (bandwidth_budget, etc.).
	if rt.authKeys != "" {
		akPath := rt.authKeys
		pluginProvider.PeerAttrFunc = func(peerID, key string) string {
			return auth.GetPeerAttr(akPath, peerID, key)
		}
	}

	// Wire HKDF-SHA256 key derivation from node identity (Finding 39, 56).
	// Uses the Ed25519 seed bytes (first 32 bytes of raw private key) as HKDF IKM.
	// Each (identity, domain) pair produces a unique, stable 32-byte key.
	hostPrivKey := rt.network.Host().Peerstore().PrivKey(rt.network.Host().ID())
	if hostPrivKey != nil {
		raw, err := hostPrivKey.Raw()
		if err == nil && len(raw) >= 32 {
			seed := make([]byte, 32)
			copy(seed, raw[:32])
			pluginProvider.KeyDeriver = func(domain string) []byte {
				r := hkdf.New(sha256.New, seed, nil, []byte(domain))
				key := make([]byte, 32)
				if _, err := io.ReadFull(r, key); err != nil {
					return nil
				}
				return key
			}
		}
	}
	// Initialize per-peer data access grant store (macaroon capability tokens).
	// Uses two separate HKDF-derived keys: one for macaroon root key (token creation/verification),
	// one for grants.json file integrity HMAC. Both derived from the same node identity.
	if pluginProvider.KeyDeriver != nil {
		grantRootKey := pluginProvider.KeyDeriver("shurli/grants/root/v1")
		grantHMACKey := pluginProvider.KeyDeriver("shurli/grants/hmac/v1")
		grantsPath := filepath.Join(filepath.Dir(rt.configFile), "grants.json")

		gs, err := grants.Load(grantsPath, grantRootKey, grantHMACKey)
		if err != nil {
			slog.Error("grants: failed to load, starting empty", "error", err)
			gs = grants.NewStore(grantRootKey, grantHMACKey)
			gs.SetPersistPath(grantsPath)
		}

		// C3 mitigation: close all connections to revoked peers.
		gs.SetOnRevoke(func(pid peer.ID) {
			if err := rt.network.Host().Network().ClosePeer(pid); err != nil {
				slog.Warn("grants: failed to close peer connections on revoke",
					"peer", pid.String()[:16], "error", err)
			}
		})

		// D2: configurable cleanup interval (B1 mitigation: periodic re-verify).
		cleanupInterval := 30 * time.Second
		if rt.config.Grants.CleanupInterval != "" {
			if parsed, err := time.ParseDuration(rt.config.Grants.CleanupInterval); err != nil {
				slog.Warn("grants: invalid cleanup_interval in config, using default 30s", "value", rt.config.Grants.CleanupInterval, "error", err)
			} else if parsed < 5*time.Second {
				slog.Warn("grants: cleanup_interval below minimum 5s, using default 30s", "value", rt.config.Grants.CleanupInterval)
			} else {
				cleanupInterval = parsed
				slog.Info("grants: cleanup interval from config", "interval", cleanupInterval)
			}
		}
		gs.StartCleanup(cleanupInterval)
		rt.grantStore = gs

		// Phase D1: integrity-chained audit log.
		auditKey := pluginProvider.KeyDeriver("shurli/grants/audit/v1")
		auditPath := filepath.Join(filepath.Dir(rt.configFile), "grant_audit.log")
		auditLog, err := grants.NewAuditLog(auditPath, auditKey)
		if err != nil {
			slog.Warn("grants: failed to init audit log, continuing without", "error", err)
		} else {
			gs.SetAuditLog(auditLog)
			slog.Info("grants: audit log enabled", "path", auditPath)
		}
		// Phase D3: per-peer ops rate limiter (10 ops/min per peer).
		// Note: onNotify callback is wired AFTER the notification router is set up (below).
		// For now, pass nil and we set the callback later.
		rl := grants.NewOpsRateLimiter(grants.DefaultOpsPerMinute, nil)
		gs.SetRateLimiter(rl)
		rt.opsRateLimiter = rl

		// Wire grant checker into service registry for stream-level enforcement.
		// This is the C2 mitigation: node-level enforcement independent of relay ACL.
		rt.network.ServiceRegistry().SetGrantChecker(gs.Check)

		// Wire grant checker into plugin context for share warnings (E1-design mitigation).
		pluginProvider.GrantChecker = gs.Check

		// Phase B: GrantPouch (received tokens) + delivery protocol + offline queue.
		configDir := filepath.Dir(rt.configFile)
		pouchHMACKey := pluginProvider.KeyDeriver("shurli/grants/pouch/v1")
		pouchPath := filepath.Join(configDir, "grant_pouch.json")

		pouch, err := grants.LoadPouch(pouchPath, pouchHMACKey)
		if err != nil {
			slog.Error("grants: failed to load pouch, starting empty", "error", err)
			pouch = grants.NewPouch(pouchHMACKey)
			pouch.SetPersistPath(pouchPath)
		}
		pouch.StartCleanup(cleanupInterval) // D2: same configurable interval as store
		rt.grantPouch = pouch

		queueHMACKey := pluginProvider.KeyDeriver("shurli/grants/queue/v1")
		queuePath := filepath.Join(configDir, "grant_delivery_queue.json")
		queueTTL := grants.DefaultDeliveryQueueTTL
		if rt.config.Grants.DeliveryQueueTTL != "" {
			if parsed, err := grants.ParseDurationExtended(rt.config.Grants.DeliveryQueueTTL); err == nil && parsed > 0 {
				queueTTL = parsed
				slog.Info("grants: delivery queue TTL from config", "ttl", queueTTL)
			} else if err != nil {
				slog.Warn("grants: invalid delivery_queue_ttl in config, using default", "value", rt.config.Grants.DeliveryQueueTTL, "error", err)
			}
		}

		dq, err := grants.LoadDeliveryQueue(queuePath, queueHMACKey, queueTTL)
		if err != nil {
			slog.Error("grants: failed to load delivery queue, starting empty", "error", err)
			dq = grants.NewDeliveryQueue(queueHMACKey, queueTTL)
			dq.SetPersistPath(queuePath)
		}
		rt.deliveryQueue = dq

		// Trust check: only accept grant deliveries from authorized peers.
		trustCheck := func(pid peer.ID) bool {
			if rt.gater == nil {
				return false
			}
			return rt.gater.IsAuthorized(pid)
		}

		grantProto := grants.NewGrantProtocol(rt.network.Host(), pouch, gs, dq, trustCheck)
		grantProto.Register()
		grantProto.StartQueueFlush()
		pouch.SetRefresher(grantProto) // B4: enable background token refresh
		rt.grantProtocol = grantProto

		// Grant receipt cache: client-side cache of relay grant receipts.
		// HKDF domain "grant-cache/v1" for file integrity (H10: separate from relay receipt key).
		cacheHMACKey := pluginProvider.KeyDeriver("grant-cache/v1")
		cachePath := filepath.Join(configDir, "grant_cache.json")
		gc, gcErr := grants.LoadGrantCache(cachePath, cacheHMACKey)
		if gcErr != nil {
			slog.Error("grants: failed to load receipt cache, starting empty", "error", gcErr)
			gc = grants.NewGrantCache(cacheHMACKey)
			gc.SetPersistPath(cachePath)
		}
		gc.StartCleanup(cleanupInterval)
		rt.grantCache = gc

		// Wire grant cache into plugin context for transfer budget/time checks (H7).
		pluginProvider.RelayGrantChecker = gc

		// Wire revocation -> cache clearing (H9/H12).
		grantProto.SetOnRevoke(func(issuerID peer.ID) {
			// Use current time as revocation time (best available - relay doesn't
			// send a timestamp in the revocation message).
			rt.grantCache.HandleRevocation(issuerID, time.Now())
		})

		// Wire delivery into Store: deliver grant tokens to peers on create/revoke.
		gs.SetOnGrant(func(peerID peer.ID, g *grants.Grant) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := grantProto.DeliverGrant(ctx, peerID, g.Token, g.Services, g.ExpiresAt, g.Permanent)
			if err != nil {
				slog.Info("grants: peer offline, queuing delivery", "peer", peerID.String()[:16], "error", err)
				if qErr := grantProto.EnqueueGrant(peerID, g.Token, g.Services, g.ExpiresAt, g.Permanent); qErr != nil {
					slog.Warn("grants: failed to queue delivery", "peer", peerID.String()[:16], "error", qErr)
				}
			}
		})
		gs.SetOnRevokeNotify(func(peerID peer.ID) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := grantProto.DeliverRevocation(ctx, peerID, "admin revoked")
			if err != nil {
				slog.Info("grants: peer offline, queuing revocation", "peer", peerID.String()[:16], "error", err)
				if qErr := grantProto.EnqueueRevocation(peerID, "admin revoked"); qErr != nil {
					slog.Warn("grants: failed to queue revocation", "peer", peerID.String()[:16], "error", qErr)
				}
			}
		})

		// Phase B2: wire token presentation on stream open.
		// TokenVerifier: inbound - verify presented tokens cryptographically.
		// D1 mitigation: constant-time HMAC on all paths (valid, invalid, malformed).
		dummyToken := macaroon.New("shurli-node", grantRootKey, "dummy")
		rt.network.ServiceRegistry().SetTokenVerifier(func(tokenB64 string, pid peer.ID, svc string) bool {
			short := pid.String()[:16]

			// D1 mitigation: all code paths must execute the same operations
			// (decode, extract, permissive check, verify) to prevent timing oracles.
			// When a step fails, subsequent steps run against dummy data.

			token, decodeErr := macaroon.DecodeBase64(tokenB64)
			if decodeErr != nil {
				token = dummyToken // use dummy for remaining steps
			}

			delegateTo := macaroon.ExtractDelegateTo(token.Caveats)
			hasPermissive := macaroon.HasPermissiveDelegation(token.Caveats)

			verifier := macaroon.DefaultVerifier(macaroon.VerifyContext{
				PeerID:     pid.String(),
				Service:    svc,
				DelegateTo: delegateTo,
				Now:        time.Now(),
			})
			verifyErr := token.Verify(grantRootKey, verifier)

			// Now evaluate results. Order doesn't matter for timing - all work is done.
			if decodeErr != nil {
				slog.Warn("grants: presented token is malformed",
					"peer", short, "service", svc, "error", decodeErr)
				return false
			}
			if delegateTo != "" && !hasPermissive {
				slog.Warn("grants: delegated token lacks delegation authorization",
					"peer", short, "service", svc, "delegate_to", delegateTo[:min(16, len(delegateTo))])
				return false
			}
			if verifyErr != nil {
				slog.Warn("grants: presented token verification failed",
					"peer", short, "service", svc, "error", verifyErr)
				return false
			}
			return true
		})

		// TokenLookup: outbound - retrieve tokens from pouch for presentation.
		rt.network.ServiceRegistry().SetTokenLookup(func(pid peer.ID, svc string) string {
			token := pouch.Get(pid, svc)
			if token == nil {
				return ""
			}
			b64, err := token.EncodeBase64()
			if err != nil {
				slog.Warn("grants: failed to encode pouch token for presentation",
					"peer", pid.String()[:16], "service", svc, "error", err)
				return ""
			}
			return b64
		})
	} else {
		slog.Warn("grants: no identity key available, grant store disabled")
	}

	// Phase C: notification router. LogSink is always active (audit trail).
	notifyRouter := notify.NewRouter(slog.Default(), notify.Severity(rt.config.Notifications.LogLevel))
	if rt.grantStore != nil {
		// Wire GrantStore lifecycle events into the notification router.
		rt.grantStore.SetOnNotify(func(eventType string, peerID peer.ID, meta map[string]string) {
			severity := notify.SeverityInfo
			if eventType == string(notify.EventGrantRevoked) || eventType == string(notify.EventGrantExpired) {
				severity = notify.SeverityWarn
			}
			msg := notifyEventMessage(eventType, meta)
			event := notify.NewEvent(notify.EventType(eventType), severity, peerID.String(), "", msg)
			for k, v := range meta {
				event = event.WithMetadata(k, v)
			}
			notifyRouter.Emit(event)
		})

		// Wire pre-expiry warning checker.
		expiryThreshold := 10 * time.Minute
		if rt.config.Notifications.ExpiryWarning != "" {
			if parsed, err := time.ParseDuration(rt.config.Notifications.ExpiryWarning); err == nil && parsed > 0 {
				expiryThreshold = parsed
			}
		}
		grantStore := rt.grantStore
		notifyRouter.SetExpiryChecker(notify.ExpiryCheckerFunc(func(d time.Duration) []notify.ExpiryInfo {
			expiring := grantStore.ExpiringWithin(d)
			result := make([]notify.ExpiryInfo, len(expiring))
			for i, g := range expiring {
				result[i] = notify.ExpiryInfo{
					PeerID:    g.PeerIDStr,
					ExpiresAt: g.ExpiresAt,
					Remaining: g.Remaining(),
				}
			}
			return result
		}), expiryThreshold, 60*time.Second)

		// Name resolver: peer ID -> human name.
		notifyRouter.SetNameResolver(func(peerID string) string {
			pnet := rt.network
			if pnet == nil {
				return ""
			}
			for name, pid := range pnet.ListNames() {
				if pid.String() == peerID {
					return name
				}
			}
			return ""
		})
	}
	// Phase C2: wire DesktopSink and WebhookSink from config.
	if rt.config.Notifications.IsDesktopEnabled() {
		if ds := notify.NewDesktopSink(); ds != nil {
			notifyRouter.AddSink(ds)
			slog.Info("notify: desktop sink enabled")
		}
	}
	if rt.config.Notifications.Webhook.URL != "" {
		ws := notify.NewWebhookSink(notify.WebhookConfig{
			URL:     rt.config.Notifications.Webhook.URL,
			Headers: rt.config.Notifications.Webhook.Headers,
			Events:  rt.config.Notifications.Webhook.Events,
		}, slog.Default())
		if ws != nil {
			notifyRouter.AddSink(ws)
			slog.Info("notify: webhook sink enabled", "url", rt.config.Notifications.Webhook.URL)
		}
	}

	notifyRouter.Start()
	rt.notifyRouter = notifyRouter

	// Phase D3: wire rate limiter notification callback now that router exists.
	if rt.opsRateLimiter != nil {
		router := notifyRouter
		rt.opsRateLimiter.SetOnNotify(func(eventType string, peerID peer.ID, meta map[string]string) {
			event := notify.NewEvent(notify.EventGrantRateLimited, notify.SeverityWarn, peerID.String(), "", "grant ops rate limit exceeded")
			for k, v := range meta {
				event = event.WithMetadata(k, v)
			}
			router.Emit(event)
		})
	}

	// All Set* callbacks configured. Seal the registry to enforce the
	// set-once-at-startup contract. Any future Set* call will panic.
	rt.network.ServiceRegistry().Seal()

	pluginRegistry := plugin.NewRegistry(pluginProvider)
	if err := plugins.RegisterAll(pluginRegistry); err != nil {
		slog.Error("plugin registration failed", "error", err)
		fmt.Fprintf(os.Stderr, "Fatal: plugin registration failed: %v\n", err)
		os.Exit(1)
	}
	if states := rt.config.Plugins.PluginStates(); states != nil {
		pluginRegistry.ApplyConfig(states)
	}
	pluginRegistry.StartAll()

	if err := rt.Bootstrap(); err != nil {
		rt.Shutdown()
		fatal("Bootstrap failed: %v", err)
	}

	// Notify plugins that network is ready (post-bootstrap).
	pluginRegistry.NotifyNetworkReady()

	rt.ExposeConfiguredServices()
	rt.StartPeerHistorySaver()

	// Start daemon API server
	socketPath := daemonSocketPath()
	cookiePath := daemonCookiePath()

	srv := daemon.NewServer(rt, socketPath, cookiePath, version)
	srv.SetInstrumentation(rt.metrics, rt.audit)
	srv.SetRegistry(pluginRegistry)
	if err := srv.Start(); err != nil {
		rt.Shutdown()
		fatal("Daemon API failed to start: %v", err)
	}

	// Start metrics endpoint (no-op if telemetry disabled)
	rt.StartMetricsServer()

	fmt.Printf("Daemon API: %s\n", socketPath)
	fmt.Println()

	// Watchdog with socket health check
	rt.StartWatchdog(watchdog.HealthCheck{
		Name: "daemon-socket",
		Check: func() error {
			if srv.Listener() == nil {
				return fmt.Errorf("daemon socket not listening")
			}
			return nil
		},
	})

	rt.StartStatusPrinter()
	rt.StartDHTHealthCheck()

	// Wait for signal or API-initiated shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		fmt.Printf("\nReceived %s, shutting down...\n", sig)
	case <-srv.ShutdownCh():
		fmt.Println("\nShutdown requested via API")
	case <-ctx.Done():
	}

	// P9 fix: stop plugins BEFORE HTTP server so in-flight handlers complete
	// before plugin resources are torn down. P10 fix: global shutdown watchdog.
	shutdownDone := make(chan struct{})
	go func() {
		pluginRegistry.StopAll() // drain active transfers first
		srv.Stop()               // then stop accepting new HTTP requests
		rt.Shutdown()            // finally close network + persistence
		close(shutdownDone)
	}()

	select {
	case <-shutdownDone:
	case <-time.After(60 * time.Second):
		slog.Error("shutdown watchdog: forced exit after 60s")
		fmt.Fprintln(os.Stderr, "Shutdown timed out after 60s, forcing exit.")
		os.Exit(1)
	}
	fmt.Println("Daemon stopped.")
}

// --- Client helper ---

func daemonClient() *daemon.Client {
	c, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
	return c
}

// tryDaemonClient attempts to connect to a running daemon.
// Returns nil if the daemon is not running or unreachable.
func tryDaemonClient() *daemon.Client {
	c, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return nil
	}
	return c
}

// configAllowsStandalone loads the node config and returns true if
// cli.allow_standalone is set. Returns false on any config load error
// (missing config is not an error condition for this check).
func configAllowsStandalone(configPath string) bool {
	cfgFile, err := config.FindConfigFile(configPath)
	if err != nil {
		return false
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		return false
	}
	return cfg.CLI.AllowStandalone
}

// --- Client subcommands ---

func runDaemonStatus(args []string) {
	fs := flag.NewFlagSet("daemon status", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(reorderFlags(fs, args))

	c := daemonClient()

	if *jsonFlag {
		resp, err := c.Status()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
	} else {
		text, err := c.StatusText()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		fmt.Print(text)
	}
}

func runDaemonStop() {
	c := daemonClient()
	if err := c.Shutdown(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
	fmt.Println("Shutdown requested.")
}

func runDaemonPing(args []string) {
	args = reorderArgs(args, map[string]bool{"json": true})

	fs := flag.NewFlagSet("daemon ping", flag.ExitOnError)
	count := fs.Int("c", 4, "number of pings")
	intervalMs := fs.Int("interval", 1000, "interval between pings (ms)")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: shurli daemon ping <peer> [-c N] [--json]")
		osExit(1)
	}

	peer := remaining[0]
	c := daemonClient()

	if *jsonFlag {
		resp, err := c.Ping(peer, *count, *intervalMs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
	} else {
		text, err := c.PingText(peer, *count, *intervalMs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		fmt.Print(text)
	}
}

func runDaemonServices(args []string) {
	fs := flag.NewFlagSet("daemon services", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(reorderFlags(fs, args))

	c := daemonClient()

	if *jsonFlag {
		resp, err := c.Services()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
	} else {
		text, err := c.ServicesText()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		fmt.Print(text)
	}
}

func runDaemonPeers(args []string) {
	fs := flag.NewFlagSet("daemon peers", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	allFlag := fs.Bool("all", false, "show all connected peers (including DHT/IPFS neighbors)")
	fs.Parse(reorderFlags(fs, args))

	c := daemonClient()

	if *jsonFlag {
		resp, err := c.Peers(*allFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
	} else {
		text, err := c.PeersText(*allFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		fmt.Print(text)
	}
}

func runDaemonPaths(args []string) {
	fs := flag.NewFlagSet("daemon paths", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(reorderFlags(fs, args))

	c := daemonClient()

	if *jsonFlag {
		resp, err := c.Paths()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
	} else {
		text, err := c.PathsText()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		fmt.Print(text)
	}
}

func runDaemonConnect(args []string) {
	fs := flag.NewFlagSet("daemon connect", flag.ExitOnError)
	peerFlag := fs.String("peer", "", "peer name or ID")
	serviceFlag := fs.String("service", "", "service name")
	listenFlag := fs.String("listen", "", "local listen address (e.g. 127.0.0.1:2222)")
	fs.Parse(reorderFlags(fs, args))

	if *peerFlag == "" || *serviceFlag == "" || *listenFlag == "" {
		fmt.Fprintln(os.Stderr, "Usage: shurli daemon connect --peer <name> --service <svc> --listen <addr>")
		osExit(1)
	}

	c := daemonClient()
	resp, err := c.Connect(*peerFlag, *serviceFlag, *listenFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
	fmt.Printf("Proxy created: %s -> %s:%s (listen: %s)\n", resp.ID, *peerFlag, *serviceFlag, resp.ListenAddress)
}

func runDaemonDisconnect(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: shurli daemon disconnect <proxy-id>")
		osExit(1)
	}

	c := daemonClient()
	if err := c.Disconnect(args[0]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
	fmt.Println("Proxy disconnected.")
}

// notifyEventMessage returns a human-readable message for a grant event type.
// Messages include services and duration context so notifications are actionable.
func notifyEventMessage(eventType string, meta map[string]string) string {
	svcLabel := "relay data access"
	if svcs, ok := meta["services"]; ok && svcs != "" {
		svcLabel = svcs
	}

	switch eventType {
	case string(notify.EventGrantCreated):
		if meta["permanent"] == "true" {
			return "granted permanent " + svcLabel
		}
		if exp, ok := meta["expires_at"]; ok {
			return "granted " + svcLabel + ", expires " + exp
		}
		return "granted " + svcLabel
	case string(notify.EventGrantRevoked):
		msg := "revoked " + svcLabel
		if rem, ok := meta["was_remaining"]; ok {
			msg += " (" + rem + " remaining)"
		}
		return msg
	case string(notify.EventGrantExpired):
		return svcLabel + " expired"
	case string(notify.EventGrantExtended):
		if exp, ok := meta["expires_at"]; ok {
			return "extended " + svcLabel + ", new expiry " + exp
		}
		return "extended " + svcLabel
	case string(notify.EventGrantRefreshed):
		msg := "refreshed " + svcLabel
		if used, ok := meta["refreshes_used"]; ok {
			if max, ok2 := meta["max_refreshes"]; ok2 {
				msg += " (" + used + "/" + max + " refreshes)"
			}
		}
		return msg
	case string(notify.EventGrantRateLimited):
		if limit, ok := meta["limit"]; ok {
			return "grant ops rate limit exceeded (" + limit + "/min)"
		}
		return "grant ops rate limit exceeded"
	default:
		return eventType
	}
}
