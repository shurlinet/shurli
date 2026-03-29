package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"flag"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ws "github.com/libp2p/go-libp2p/p2p/transport/websocket"

	"golang.org/x/crypto/hkdf"
	"golang.org/x/term"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/deposit"
	"github.com/shurlinet/shurli/internal/grants"
	"github.com/shurlinet/shurli/internal/identity"
	"github.com/shurlinet/shurli/internal/relay"
	tc "github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/internal/validate"
	"github.com/shurlinet/shurli/internal/vault"
	"github.com/shurlinet/shurli/internal/watchdog"
	"github.com/shurlinet/shurli/pkg/sdk"
)

const relayConfigFile = "relay-server.yaml"

// relayUserAgent builds the UserAgent string for the relay server.
// If a name is configured, it is appended in parentheses.
func relayUserAgent(name string) string {
	if name != "" {
		return fmt.Sprintf("relay-server/%s (%s)", version, name)
	}
	return fmt.Sprintf("relay-server/%s", version)
}

// runRelayServe starts the circuit relay server. This is the equivalent of the
// former standalone relay-server binary's main() function.
func runRelayServe(args []string) {
	// Handle --config flag
	var explicitConfig string
	for i, arg := range args {
		if (arg == "--config" || arg == "-config") && i+1 < len(args) {
			explicitConfig = args[i+1]
		}
		if strings.HasPrefix(arg, "--config=") {
			explicitConfig = strings.TrimPrefix(arg, "--config=")
		}
	}

	// Search standard locations: ./relay-server.yaml, /etc/shurli/relay/relay-server.yaml
	configFile, err := config.FindRelayConfigFile(explicitConfig)
	if err != nil {
		fatal("Config not found: %v\n", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Printf("=== Private libp2p Relay Server (%s) ===\n", version)
	fmt.Println()

	// Load configuration (paths auto-resolved against config directory)
	cfg, err := config.LoadRelayServerConfig(configFile)
	if err != nil {
		fatal("Failed to load config: %v\n", err)
	}

	relayConfigDir := filepath.Dir(configFile)

	// Validate configuration
	if err := config.ValidateRelayServerConfig(cfg); err != nil {
		fatal("Invalid configuration: %v", err)
	}

	// Archive last-known-good config on successful validation
	if err := config.Archive(configFile); err != nil {
		log.Printf("Warning: failed to archive config: %v", err)
	}

	fmt.Printf("Loaded configuration from %s\n", configFile)
	fmt.Printf("Authentication: %v\n", cfg.Security.EnableConnectionGating)
	fmt.Println()

	var priv crypto.PrivKey
	if _, statErr := os.Stat(cfg.Identity.KeyFile); statErr == nil {
		// Existing key file: load it with session token or interactive prompt.
		pw, err := resolvePasswordInteractive(relayConfigDir, os.Stdout)
		if err != nil {
			fatal("Identity error: %v", err)
		}
		priv, err = identity.LoadIdentity(cfg.Identity.KeyFile, pw)
		if err != nil {
			fatal("Identity error: %v", err)
		}
		// Create session token if missing (enables systemd auto-start).
		if !identity.SessionExists(relayConfigDir) {
			if err := identity.CreateSession(relayConfigDir, pw); err != nil {
				slog.Warn("failed to create session token", "err", err)
			} else {
				fmt.Println("Session token created (auto-start enabled).")
			}
		}
	} else {
		// No key file: ask new identity vs recover.
		fmt.Println("Identity:")
		fmt.Println("  1. Create a new identity (default)")
		fmt.Println("  2. Recover from an existing seed phrase")
		fmt.Println()
		fmt.Print("Choice [1]: ")

		idChoice, _ := stdinReadLine()
		idChoice = strings.TrimSpace(idChoice)
		if idChoice == "" {
			idChoice = "1"
		}

		var mnemonic string
		var entropy []byte
		var pw string

		switch idChoice {
		case "2":
			// Recover from existing seed phrase.
			fmt.Println()
			fmt.Println("Enter your seed phrase to recover the relay identity.")
			fmt.Println()
			var seedErr error
			mnemonic, seedErr = readSeedPhrase(os.Stdout)
			if seedErr != nil {
				fatal("Failed to read seed phrase: %v", seedErr)
			}
			if seedErr = identity.ValidateMnemonic(mnemonic); seedErr != nil {
				fatal("Invalid seed phrase: %v", seedErr)
			}
			entropy, seedErr = identity.SeedFromMnemonic(mnemonic)
			if seedErr != nil {
				fatal("Failed to decode seed: %v", seedErr)
			}
			priv, seedErr = identity.DeriveIdentityKey(entropy)
			if seedErr != nil {
				fatal("Failed to derive identity key: %v", seedErr)
			}
			recPeerID, _ := peer.IDFromPrivateKey(priv)
			fmt.Println()
			fmt.Println("Seed phrase accepted.")
			fmt.Printf("Recovered Peer ID: %s\n\n", recPeerID)

		case "1":
			// Generate new BIP39 seed.
			var genErr error
			mnemonic, entropy, genErr = identity.GenerateSeed()
			if genErr != nil {
				fatal("Failed to generate seed: %v", genErr)
			}
			words := strings.Fields(mnemonic)
			fmt.Println()
			fmt.Println("=== RELAY SEED PHRASE ===")
			fmt.Println("Write this down and store it securely. This is the ONLY way to")
			fmt.Println("recover your relay identity and vault if this server is lost.")
			fmt.Println()
			fmt.Print(formatSeedGrid(words))
			fmt.Println()
			fmt.Println("Plain text (for copy/paste):")
			fmt.Println(strings.Join(words, " "))
			fmt.Println()
			fmt.Println("===========================")
			fmt.Println()

		default:
			fatal("Invalid choice: %s (enter 1 or 2)", idChoice)
		}

		// Password setup.
		fmt.Println("Set a password to protect the relay identity:")
		fmt.Printf("  Requirements: %d+ characters, at least 3 of: uppercase, lowercase, digit, symbol\n\n", validate.MinPasswordLen)
		const maxAttempts = 3
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			p, pwErr := readPasswordConfirm("Password: ", "Confirm: ", os.Stdout)
			if pwErr == nil {
				pw = p
				break
			}
			fmt.Printf("  %v\n", pwErr)
			if attempt < maxAttempts {
				fmt.Printf("  Try again (%d of %d)\n\n", attempt+1, maxAttempts)
			} else {
				fatal("Password setup failed after %d attempts", maxAttempts)
			}
		}

		// Derive key (for new identity; recovery already derived above).
		if idChoice != "2" {
			var dErr error
			priv, dErr = identity.DeriveIdentityKey(entropy)
			if dErr != nil {
				fatal("Failed to derive identity key: %v", dErr)
			}
		}

		if err := identity.SaveIdentity(cfg.Identity.KeyFile, priv, pw); err != nil {
			fatal("Failed to save identity: %v", err)
		}
		// Create session token for systemd auto-start.
		if err := identity.CreateSession(relayConfigDir, pw); err != nil {
			slog.Warn("failed to create session token", "err", err)
		} else {
			fmt.Println("Session token created (auto-start enabled).")
		}

		// Initialize vault using the same seed and password (one-shot setup).
		vaultPath := cfg.Security.VaultFile
		if vaultPath == "" {
			vaultPath = filepath.Join(relayConfigDir, "relay_vault.json")
		}
		v, vaultErr := vault.Create(entropy, mnemonic, pw, false, 30)
		if vaultErr != nil {
			fmt.Printf("Warning: vault init failed: %v\n", vaultErr)
			fmt.Println("You can initialize later with: shurli relay vault init")
		} else {
			if err := v.Save(vaultPath); err != nil {
				fmt.Printf("Warning: vault save failed: %v\n", err)
			} else {
				fmt.Printf("Vault initialized (auto-seal: 30 minutes)\n")
				// Update config with vault path if it wasn't set.
				if cfg.Security.VaultFile == "" {
					cfg.Security.VaultFile = vaultPath
				}
			}
		}
	}

	// Load authorized keys if connection gating is enabled
	var gater *auth.AuthorizedPeerGater
	if cfg.Security.EnableConnectionGating {
		if cfg.Security.AuthorizedKeysFile == "" {
			fatal("Connection gating enabled but no authorized_keys_file specified")
		}

		authorizedPeers, err := auth.LoadAuthorizedKeys(cfg.Security.AuthorizedKeysFile)
		if err != nil {
			fatal("Failed to load authorized keys: %v", err)
		}

		if len(authorizedPeers) == 0 {
			fmt.Printf("WARNING: authorized_keys file is empty - no peers can make reservations!\n")
			fmt.Printf("   Add authorized peer IDs to %s\n", cfg.Security.AuthorizedKeysFile)
		} else {
			fmt.Printf("Loaded %d authorized peer(s) from %s\n", len(authorizedPeers), cfg.Security.AuthorizedKeysFile)
		}

		gater = auth.NewAuthorizedPeerGater(authorizedPeers)
	} else {
		fmt.Println("WARNING: Connection gating is DISABLED - any peer can use this relay!")
	}
	fmt.Println()

	// Build host options.
	// Transport order: QUIC first (3 RTTs, native multiplexing), TCP second (universal fallback),
	// WebSocket last (anti-censorship/DPI evasion). AutoNAT v2 for per-address reachability testing.
	hostOpts := []libp2p.Option{
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(cfg.Network.ListenAddresses...),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(ws.New),
		libp2p.EnableAutoNATv2(),
		libp2p.UserAgent(relayUserAgent(cfg.Name)),
	}

	// Resource manager (always enabled on relay - public-facing service)
	{
		limits := rcmgr.DefaultLimits
		libp2p.SetDefaultServiceLimits(&limits)
		scaled := limits.AutoScale()
		rm, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(scaled))
		if err != nil {
			fatal("Failed to create resource manager: %v", err)
		}
		hostOpts = append(hostOpts, libp2p.ResourceManager(rm))
		slog.Info("resource manager enabled", "limits", "auto-scaled")
	}

	// Add connection gater if enabled
	if gater != nil {
		hostOpts = append(hostOpts, libp2p.ConnectionGater(gater))
	}

	// Create host - relay service is added separately below
	h, err := libp2p.New(hostOpts...)
	if err != nil {
		fatal("Failed to create host: %v", err)
	}
	defer h.Close()

	// Initialize relay grant store for time-limited per-peer data access.
	// Uses HKDF-SHA256 to derive keys from the relay identity:
	// rootKey for macaroon tokens, hmacKey for grants.json integrity,
	// receiptKey for grant receipt HMAC (H10: dedicated domain separation).
	var relayGrantStore *grants.Store
	var receiptHMACKey []byte
	if priv != nil {
		raw, rawErr := priv.Raw()
		if rawErr == nil && len(raw) >= 32 {
			seed := make([]byte, 32)
			copy(seed, raw[:32])
			deriveKey := func(domain string) []byte {
				r := hkdf.New(sha256.New, seed, nil, []byte(domain))
				key := make([]byte, 32)
				if _, err := io.ReadFull(r, key); err != nil {
					return nil
				}
				return key
			}
			grantRootKey := deriveKey("shurli/relay/grants/root/v1")
			grantHMACKey := deriveKey("shurli/relay/grants/hmac/v1")
			receiptHMACKey = deriveKey("grant-receipt/v1")
			// Zero seed after derivation (key material hygiene).
			for i := range seed {
				seed[i] = 0
			}
			if grantRootKey == nil || grantHMACKey == nil || receiptHMACKey == nil {
				slog.Error("relay grants: key derivation failed, grant store disabled")
			} else {
				grantsPath := filepath.Join(filepath.Dir(configFile), "grants.json")

				gs, gsErr := grants.Load(grantsPath, grantRootKey, grantHMACKey)
				if gsErr != nil {
					slog.Error("relay grants: failed to load, starting empty", "error", gsErr)
					gs = grants.NewStore(grantRootKey, grantHMACKey)
					gs.SetPersistPath(grantsPath)
				}

				// Terminate active circuits when a grant is revoked or expires.
				gs.SetOnRevoke(func(pid peer.ID) {
					if err := h.Network().ClosePeer(pid); err != nil {
						short := pid.String()
						if len(short) > 16 {
							short = short[:16]
						}
						slog.Warn("relay grants: failed to close peer on revoke",
							"peer", short, "error", err)
					}
				})

				gs.StartCleanup(60 * time.Second)
				relayGrantStore = gs
				slog.Info("relay grants: store initialized", "path", grantsPath)
			}
		}
	}

	relayResources, relayLimit := buildRelayResources(&cfg.Resources)
	circuitACL := relay.NewCircuitACL(cfg.Security.AuthorizedKeysFile, cfg.Security.EnableDataRelay, cfg.Security.EnableConnectionGating, relayGrantStore)
	_, err = relayv2.New(h,
		relayv2.WithResources(relayResources),
		relayv2.WithLimit(relayLimit),
		relayv2.WithACL(circuitACL),
	)
	if err != nil {
		fatal("Failed to start relay service: %v", err)
	}
	fmt.Printf("Relay limits: max_reservations=%d, max_circuits=%d, session=%s, data=%s/direction\n",
		cfg.Resources.MaxReservations, cfg.Resources.MaxCircuits,
		cfg.Resources.SessionDuration, cfg.Resources.SessionDataLimit)
	if cfg.Security.EnableDataRelay {
		fmt.Println("Data relay: ENABLED (all authorized peers can relay data)")
	} else {
		fmt.Println("Data relay: DISABLED (discovery and signaling only)")
		fmt.Println("  Peers connect directly. No SSH/XRDP data flows through this relay.")
		fmt.Println("  Exceptions: admin peers, peers with active data grants.")
		fmt.Println("  Grant access: shurli relay grant <peer-id> --duration 1h")
		fmt.Println("  Enable for all: set enable_data_relay: true in relay-server.yaml")
	}

	// Bootstrap into the private shurli DHT as a server.
	// The relay is the primary bootstrap peer - all shurli nodes connect here first
	// and use this DHT for peer discovery.
	dhtPrefix := sdk.DHTProtocolPrefixForNamespace(cfg.Discovery.Network)
	kdht, err := dht.New(ctx, h,
		dht.Mode(dht.ModeServer),
		dht.ProtocolPrefix(protocol.ID(dhtPrefix)),
		dht.RoutingTablePeerDiversityFilter(dht.NewRTPeerDiversityFilter(h, 3, 50)),
	)
	if err != nil {
		fatal("DHT error: %v", err)
	}
	if err := kdht.Bootstrap(ctx); err != nil {
		fatal("DHT bootstrap error: %v", err)
	}
	defer kdht.Close()
	if cfg.Discovery.Network != "" {
		fmt.Printf("Private DHT active: network %q (protocol: %s/kad/1.0.0)\n", cfg.Discovery.Network, dhtPrefix)
	} else {
		fmt.Printf("Private DHT active (protocol: %s/kad/1.0.0)\n", dhtPrefix)
	}

	// Initialize token store and pairing protocol handler.
	tokenStore := relay.NewTokenStore()
	depositStore := deposit.NewDepositStore()
	// Same nil interface trap guard for pairing handler.
	var pairingGater relay.GaterInterface
	if gater != nil {
		pairingGater = gater
	}
	pairingHandler := &relay.PairingHandler{
		Store:        tokenStore,
		AuthKeysPath: cfg.Security.AuthorizedKeysFile,
		Gater:        pairingGater,
		Deposits:     depositStore,
	}
	notifier := &relay.PeerNotifier{Host: h, AuthKeysPath: cfg.Security.AuthorizedKeysFile, Store: tokenStore}
	h.SetStreamHandler(protocol.ID(relay.InviteProtocol), func(s network.Stream) {
		joinedPeer, groupID := pairingHandler.HandleStream(s)
		if joinedPeer != "" && groupID != "" {
			go notifier.NotifyGroupMembers(ctx, groupID, joinedPeer)
		}
	})
	slog.Info("pairing protocol registered", "protocol", relay.InviteProtocol)

	// Start admin socket for relay CLI.
	adminSocketPath := filepath.Join(filepath.Dir(configFile), ".relay-admin.sock")
	adminCookiePath := filepath.Join(filepath.Dir(configFile), ".relay-admin.cookie")
	relayPeerID, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		fatal("Failed to derive peer ID: %v", err)
	}
	relayAddrStr, err := buildRelayAddr(cfg, relayPeerID)
	if err != nil {
		slog.Warn("admin socket: could not build relay addr for code encoding", "err", err)
		relayAddrStr = "" // non-fatal: admin socket still works, code encoding will fail
	}
	// Pass gater as typed interface to avoid Go's nil interface trap:
	// a nil *AuthorizedPeerGater passed as AdminGaterInterface becomes
	// a non-nil interface with nil value, causing panics on method calls.
	var adminGater relay.AdminGaterInterface
	if gater != nil {
		adminGater = gater
	}
	adminSrv := relay.NewAdminServer(tokenStore, adminGater, relayAddrStr, cfg.Discovery.Network, adminSocketPath, adminCookiePath)
	adminSrv.SetAuthKeysPath(cfg.Security.AuthorizedKeysFile)
	adminSrv.SetCircuitACL(circuitACL)
	adminSrv.SetHost(h)
	if relayGrantStore != nil {
		adminSrv.SetGrantStore(relayGrantStore)
		defer relayGrantStore.Stop()
	}
	// Parse session limits once for both AdminServer and reconnect notifier (H13).
	receiptSessionDuration, _ := time.ParseDuration(cfg.Resources.SessionDuration)
	receiptSessionDataLimit, _ := config.ParseDataSize(cfg.Resources.SessionDataLimit)
	// Wire grant receipt HMAC key and session limits for receipt push (H10, H13).
	if receiptHMACKey != nil {
		adminSrv.SetReceiptHMACKey(receiptHMACKey)
		adminSrv.SetSessionLimits(receiptSessionDataLimit, receiptSessionDuration)
	}

	// Load vault if configured. When sealed, the relay starts in watch-only mode:
	// existing peers can use the relay, but no new peers can be authorized.
	var relayVault *vault.Vault
	if cfg.Security.VaultFile != "" {
		if _, statErr := os.Stat(cfg.Security.VaultFile); statErr == nil {
			v, loadErr := vault.Load(cfg.Security.VaultFile)
			if loadErr != nil {
				fatal("Failed to load vault: %v", loadErr)
			}
			relayVault = v
			adminSrv.SetVault(v, cfg.Security.VaultFile)
			fmt.Printf("Vault loaded from %s (sealed, watch-only mode)\n", cfg.Security.VaultFile)
		} else {
			// Vault file doesn't exist yet - will be created via `vault init`
			adminSrv.SetVault(nil, cfg.Security.VaultFile)
			fmt.Printf("Vault not initialized (run 'shurli relay vault init')\n")
		}
	}

	// Wire macaroon verification: root key comes from vault (available only when unsealed).
	if relayVault != nil {
		pairingHandler.RootKeyFunc = relayVault.RootKey
	}

	// Wire up invite deposit store. The root key comes from the vault dynamically
	// (available only when unsealed). The deposit store itself is always available.
	adminSrv.SetDepositStore(depositStore)

	if err := adminSrv.Start(); err != nil {
		slog.Error("failed to start admin socket", "err", err)
		// Non-fatal: relay still functions, just no CLI pairing
	} else {
		defer adminSrv.Stop()
	}

	// Register remote admin protocol for general admin operations over P2P.
	// Available even when sealed - admin peers can unseal, check status, etc.
	remoteAdminHandler := relay.NewRemoteAdminHandler(adminSrv, cfg.Security.AuthorizedKeysFile)
	remoteAdminHandler.SetInvitePolicy(cfg.Security.InvitePolicy)
	h.SetStreamHandler(protocol.ID(relay.RemoteAdminProtocol), func(s network.Stream) {
		remoteAdminHandler.HandleStream(s)
	})
	slog.Info("remote admin protocol registered", "protocol", relay.RemoteAdminProtocol)

	// Register dedicated unseal protocol for remote vault unseal over P2P.
	// This has its own iOS-style escalating lockout (4 free tries, then
	// 1min/5min/15min/1hr, then permanent block) and binary wire format.
	// /v1/unseal remains blocked on the generic admin protocol by design.
	var unsealHandler *relay.UnsealHandler
	if relayVault != nil {
		lockoutFile := relay.LockoutStateFile(filepath.Dir(configFile))
		unsealHandler = relay.NewUnsealHandler(relayVault, cfg.Security.AuthorizedKeysFile, lockoutFile)
		h.SetStreamHandler(protocol.ID(relay.UnsealProtocol), func(s network.Stream) {
			unsealHandler.HandleStream(s)
		})
		slog.Info("unseal protocol registered", "protocol", relay.UnsealProtocol)
	}

	// Initialize MOTD handler for relay operator announcements.
	goodbyeFile := filepath.Join(filepath.Dir(configFile), ".relay-goodbye.json")
	motdHandler := relay.NewMOTDHandler(h, priv, goodbyeFile)
	adminSrv.SetMOTDHandler(motdHandler)

	// Wire shutdown func: goodbye/shutdown admin endpoint triggers graceful process exit.
	shutdownCh := make(chan struct{}, 1)
	adminSrv.SetShutdownFunc(func() {
		select {
		case shutdownCh <- struct{}{}:
		default:
		}
	})

	// Start MOTD notifier: pushes MOTD/goodbye to peers as they connect.
	go motdHandler.RunMOTDNotifier(ctx)
	slog.Info("motd handler initialized", "protocol", relay.MOTDProtocol)

	// Register ZKP anonymous auth protocol (Phase 7).
	var zkpAuthHandler *relay.ZKPAuthHandler
	if cfg.Security.ZKP.Enabled {
		keysDir := cfg.Security.ZKP.SRSCacheDir
		if keysDir == "" {
			home, _ := os.UserHomeDir()
			keysDir = filepath.Join(home, ".shurli", "zkp")
		}

		var zkpErr error
		zkpAuthHandler, zkpErr = relay.NewZKPAuthHandler(cfg.Security.AuthorizedKeysFile, keysDir)
		if zkpErr != nil {
			slog.Error("zkp auth handler failed to initialize", "err", zkpErr)
			fmt.Printf("WARNING: ZKP auth disabled: %v\n", zkpErr)
		} else {
			h.SetStreamHandler(protocol.ID(relay.ZKPAuthProtocol), func(s network.Stream) {
				zkpAuthHandler.HandleStream(s)
			})
			adminSrv.SetZKPAuth(zkpAuthHandler)
			// Build initial tree from authorized_keys.
			if err := zkpAuthHandler.RebuildTree(); err != nil {
				slog.Warn("zkp: initial tree build failed (rebuild via admin API)", "err", err)
			}
			// Start periodic challenge cleanup goroutine.
			zkpAuthHandler.Challenges().StartCleanup(ctx)
			slog.Info("zkp auth protocol registered", "protocol", relay.ZKPAuthProtocol)
		}
	}

	// Vault auto-seal goroutine: re-seals the vault after the configured timeout.
	if relayVault != nil {
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if relayVault.ShouldAutoSeal() {
						relayVault.Seal()
						if adminSrv.Metrics != nil {
							adminSrv.Metrics.VaultSealOpsTotal.WithLabelValues("auto_seal").Inc()
							adminSrv.Metrics.VaultSealed.Set(1)
						}
						slog.Info("vault auto-sealed after timeout")
					}
				}
			}
		}()
	}

	// Reconnect notifier: push peer introductions + grant receipts when authorized peers reconnect.
	var receiptDelivery *relay.GrantReceiptDelivery
	if relayGrantStore != nil && receiptHMACKey != nil {
		receiptDelivery = &relay.GrantReceiptDelivery{
			GrantStore:       relayGrantStore,
			HMACKey:          receiptHMACKey,
			SessionDataLimit: receiptSessionDataLimit,
			SessionDuration:  receiptSessionDuration,
		}
	}
	go relay.RunReconnectNotifier(ctx, h, notifier, cfg.Security.AuthorizedKeysFile, receiptDelivery)

	// Token expiry cleanup goroutine.
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if removed := tokenStore.CleanExpired(); removed > 0 {
					slog.Info("cleaned expired pairing groups", "removed", removed)
				}
				// Auto-disable enrollment when no active groups.
				if gater != nil && tokenStore.ActiveGroupCount() == 0 && gater.IsEnrollmentEnabled() {
					gater.SetEnrollmentMode(false, 0, 0)
				}
			}
		}
	}()

	// Probation cleanup goroutine (evict stale probation peers).
	if gater != nil {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					gater.CleanupProbation(func(p peer.ID) {
						if err := h.Network().ClosePeer(p); err != nil {
							slog.Warn("failed to disconnect probation peer", "peer", p.String()[:16]+"...", "err", err)
						}
					})
				}
			}
		}()
	}

	fmt.Printf("Relay Peer ID: %s\n", h.ID())
	fmt.Println()

	// Verify the relay protocol is registered
	fmt.Println("Registered protocols:")
	for _, p := range h.Mux().Protocols() {
		fmt.Printf("  %s\n", p)
	}

	fmt.Println()
	fmt.Println("Multiaddrs:")
	for _, addr := range h.Addrs() {
		fmt.Printf("  %s/p2p/%s\n", addr, h.ID())
	}

	go func() {
		ticker := time.NewTicker(cfg.Logging.PeerListIntervalDuration())
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				peers := h.Network().Peers()
				fmt.Printf("\n--- %d connected peers ---\n", len(peers))
				for _, p := range peers {
					fmt.Printf("  %s\n", p.String()[:16])
				}
			}
		}
	}()

	// Start watchdog health checks and notify systemd
	watchdog.Ready()
	go watchdog.Run(ctx, watchdog.Config{Interval: 30 * time.Second}, []watchdog.HealthCheck{
		{
			Name: "host-listening",
			Check: func() error {
				if len(h.Addrs()) == 0 {
					return fmt.Errorf("no listen addresses")
				}
				return nil
			},
		},
		{
			Name: "protocols-registered",
			Check: func() error {
				if len(h.Mux().Protocols()) == 0 {
					return fmt.Errorf("no protocols registered")
				}
				return nil
			},
		},
	})

	// Initialize relay observability (opt-in)
	var relayMetrics *sdk.Metrics
	if cfg.Telemetry.Metrics.Enabled {
		relayMetrics = sdk.NewMetrics(version, runtime.Version())
		slog.Info("telemetry: metrics enabled", "addr", cfg.Telemetry.Metrics.ListenAddress)
	}
	// Wire metrics to Phase 6 components (nil-safe: if metrics disabled, handlers work without them).
	if relayMetrics != nil {
		adminSrv.Metrics = relayMetrics
		remoteAdminHandler.SetMetrics(relayMetrics)
		pairingHandler.Metrics = relayMetrics
		if zkpAuthHandler != nil {
			zkpAuthHandler.Metrics = relayMetrics
		}
		motdHandler.SetMetrics(relayMetrics)
		if unsealHandler != nil {
			unsealHandler.Metrics = relayMetrics
		}

		// Set initial vault seal state gauge.
		if relayVault != nil {
			if relayVault.IsSealed() {
				relayMetrics.VaultSealed.Set(1)
			} else {
				relayMetrics.VaultSealed.Set(0)
			}
		}
	}

	var relayAudit *sdk.AuditLogger
	if cfg.Telemetry.Audit.Enabled {
		relayAudit = sdk.NewAuditLogger(slog.NewJSONHandler(os.Stderr, nil))
		slog.Info("telemetry: audit logging enabled")
	}

	// Wire auth decision callback on relay gater
	if gater != nil && (relayMetrics != nil || relayAudit != nil) {
		gater.SetDecisionCallback(func(peerID, result string) {
			if relayMetrics != nil {
				relayMetrics.AuthDecisionsTotal.WithLabelValues(result).Inc()
			}
			if relayAudit != nil {
				relayAudit.AuthDecision(peerID, "inbound", result)
			}
		})
	}

	// Start /healthz HTTP endpoint if enabled.
	// Security: only exposes operational status (no peer IDs, versions, or protocol lists).
	// Default listen address is 127.0.0.1:9090 (localhost-only), but if configured to
	// bind externally, we validate that the source IP is loopback to prevent information leakage.
	var healthServer *http.Server
	if cfg.Health.Enabled || relayMetrics != nil {
		startTime := time.Now()
		mux := http.NewServeMux()

		if cfg.Health.Enabled {
			mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
				// Reject non-loopback sources when bound to a non-loopback address
				host, _, _ := net.SplitHostPort(r.RemoteAddr)
				if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"status":          "ok",
					"uptime_seconds":  int(time.Since(startTime).Seconds()),
					"connected_peers": len(h.Network().Peers()),
				})
			})
		}

		if relayMetrics != nil {
			mux.Handle("/metrics", relayMetrics.Handler())
		}

		// Use metrics listen address when health is not enabled
		listenAddr := cfg.Health.ListenAddress
		if !cfg.Health.Enabled && relayMetrics != nil {
			listenAddr = cfg.Telemetry.Metrics.ListenAddress
		}

		healthServer = &http.Server{
			Addr:         listenAddr,
			Handler:      mux,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		}
		go func() {
			slog.Info("HTTP endpoint started", "addr", listenAddr)
			if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("HTTP endpoint error", "err", err)
			}
		}()
	}

	fmt.Println()
	fmt.Println("Private relay running.")
	fmt.Println("Press Ctrl+C to stop.")

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)

	// Wait for OS signal or admin-initiated shutdown.
	select {
	case sig := <-ch:
		fmt.Printf("\nReceived %s, shutting down...\n", sig)
	case <-shutdownCh:
		fmt.Println("\nAdmin-initiated shutdown (goodbye sent to peers)...")
	}

	watchdog.Stopping()
	if healthServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		healthServer.Shutdown(shutdownCtx)
		shutdownCancel()
	}
	cancel() // Stop background goroutines
}

// buildRelayResources converts config resource settings into relayv2 types.
func buildRelayResources(rc *config.RelayResourcesConfig) (relayv2.Resources, *relayv2.RelayLimit) {
	// Parse durations (already validated by ValidateRelayServerConfig)
	reservationTTL, _ := time.ParseDuration(rc.ReservationTTL)
	sessionDuration, _ := time.ParseDuration(rc.SessionDuration)
	sessionDataLimit, _ := config.ParseDataSize(rc.SessionDataLimit)

	resources := relayv2.Resources{
		Limit: &relayv2.RelayLimit{
			Duration: sessionDuration,
			Data:     sessionDataLimit,
		},
		ReservationTTL:         reservationTTL,
		MaxReservations:        rc.MaxReservations,
		MaxCircuits:            rc.MaxCircuits,
		BufferSize:             rc.BufferSize,
		MaxReservationsPerPeer: 1,
		MaxReservationsPerIP:   rc.MaxReservationsPerIP,
		MaxReservationsPerASN:  rc.MaxReservationsPerASN,
	}

	limit := &relayv2.RelayLimit{
		Duration: sessionDuration,
		Data:     sessionDataLimit,
	}

	return resources, limit
}

// loadRelayAuthKeysPathErr loads relay config and returns the authorized_keys file path.
func loadRelayAuthKeysPathErr(configFile string) (string, error) {
	cfg, err := config.LoadRelayServerConfig(configFile)
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}
	if cfg.Security.AuthorizedKeysFile == "" {
		return "", fmt.Errorf("no authorized_keys_file configured")
	}
	return cfg.Security.AuthorizedKeysFile, nil
}

func loadRelayAuthKeysPath(configFile string) string {
	path, err := loadRelayAuthKeysPathErr(configFile)
	if err != nil {
		fatal("%v", err)
	}
	return path
}

func doRelayAuthorize(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay authorize", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: shurli relay authorize <peer-id> [comment] [--remote <addr>]")
	}

	peerID := fs.Arg(0)
	comment := ""
	if fs.NArg() > 1 {
		comment = strings.Join(fs.Args()[1:], " ")
	}

	if *remoteFlag != "" {
		client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
		if err != nil {
			return err
		}
		defer cleanup()

		if err := client.AuthorizePeer(peerID, comment); err != nil {
			return fmt.Errorf("failed to authorize peer: %w", err)
		}
		fmt.Fprintf(stdout, "Authorized: %s\n", peerID[:min(16, len(peerID))]+"...")
		if comment != "" {
			fmt.Fprintf(stdout, "Comment:    %s\n", comment)
		}
		fmt.Fprintln(stdout, "Applied immediately (remote admin).")
		return nil
	}

	authKeysPath, err := loadRelayAuthKeysPathErr(configFile)
	if err != nil {
		return err
	}
	if err := auth.AddPeer(authKeysPath, peerID, comment); err != nil {
		return fmt.Errorf("failed to authorize peer: %w", err)
	}

	fmt.Fprintf(stdout, "Authorized: %s\n", peerID[:min(16, len(peerID))]+"...")
	if comment != "" {
		fmt.Fprintf(stdout, "Comment:    %s\n", comment)
	}
	fmt.Fprintf(stdout, "File:       %s\n", authKeysPath)
	fmt.Fprintln(stdout)
	tryRelayAuthReload(configFile, stdout)
	return nil
}

func runRelayAuthorize(args []string, configFile string) {
	if err := doRelayAuthorize(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayDeauthorize(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay deauthorize", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: shurli relay deauthorize <peer-id> [--remote <addr>]")
	}

	peerID := fs.Arg(0)

	if *remoteFlag != "" {
		client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
		if err != nil {
			return err
		}
		defer cleanup()

		if err := client.DeauthorizePeer(peerID); err != nil {
			return fmt.Errorf("failed to deauthorize peer: %w", err)
		}
		fmt.Fprintf(stdout, "Deauthorized: %s\n", peerID[:min(16, len(peerID))]+"...")
		fmt.Fprintln(stdout, "Applied immediately (remote admin).")
		return nil
	}

	authKeysPath, err := loadRelayAuthKeysPathErr(configFile)
	if err != nil {
		return err
	}
	if err := auth.RemovePeer(authKeysPath, peerID); err != nil {
		return fmt.Errorf("failed to deauthorize peer: %w", err)
	}

	fmt.Fprintf(stdout, "Deauthorized: %s\n", peerID[:min(16, len(peerID))]+"...")
	fmt.Fprintln(stdout)
	tryRelayAuthReload(configFile, stdout)
	return nil
}

// tryRelayAuthReload attempts to hot-reload the relay's authorized_keys via admin socket.
// If the relay is not running or reload fails, prints an appropriate message.
func tryRelayAuthReload(configFile string, stdout io.Writer) {
	client, err := relayAdminClient(configFile)
	if err != nil {
		fmt.Fprintln(stdout, "Relay not running. Changes saved, will apply on next start.")
		return
	}
	if err := client.AuthReload(); err != nil {
		fmt.Fprintf(stdout, "Warning: file updated but live reload failed: %v\n", err)
		fmt.Fprintln(stdout, "Restart relay to apply: sudo systemctl restart shurli-relay")
		return
	}
	fmt.Fprintln(stdout, "Applied immediately (live reload).")
}

func runRelayDeauthorize(args []string, configFile string) {
	if err := doRelayDeauthorize(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelaySetAttr(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay set-attr", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() < 3 {
		return fmt.Errorf("usage: shurli relay set-attr <peer-id> <key> <value> [--remote <addr>]")
	}

	peerID := fs.Arg(0)
	key := fs.Arg(1)
	value := fs.Arg(2)

	if *remoteFlag != "" {
		client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
		if err != nil {
			return err
		}
		defer cleanup()

		if err := client.SetPeerAttr(peerID, key, value); err != nil {
			return fmt.Errorf("failed to set attribute: %w", err)
		}
		fmt.Fprintf(stdout, "Set %s=%s on %s\n", key, value, peerID[:min(16, len(peerID))]+"...")
		fmt.Fprintln(stdout, "Applied immediately (remote admin).")
		return nil
	}

	authKeysPath, err := loadRelayAuthKeysPathErr(configFile)
	if err != nil {
		return err
	}
	if err := auth.SetPeerAttr(authKeysPath, peerID, key, value); err != nil {
		return fmt.Errorf("failed to set attribute: %w", err)
	}

	fmt.Fprintf(stdout, "Set %s=%s on %s\n", key, value, peerID[:min(16, len(peerID))]+"...")
	fmt.Fprintf(stdout, "File: %s\n", authKeysPath)
	fmt.Fprintln(stdout)
	tryRelayAuthReload(configFile, stdout)
	return nil
}

func runRelaySetAttr(args []string, configFile string) {
	if err := doRelaySetAttr(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

// termWidth returns the terminal width, or 80 if detection fails.
func termWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return 80
	}
	return w
}

// formatPeerID returns the full peer ID on wide terminals (>=120 cols),
// or a truncated version on narrower terminals.
func formatPeerID(id string, wide bool) string {
	if wide || len(id) <= 20 {
		return id
	}
	return id[:16] + "..."
}

func doRelayListPeers(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay list-peers", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	wide := termWidth() >= 120

	// Try admin client (local socket or remote P2P).
	client, cleanup, clientErr := relayAdminClientOrRemote(*remoteFlag, configFile)
	if clientErr == nil {
		defer cleanup()
		return listPeersViaClient(client, stdout, wide)
	}

	// Admin client unavailable (relay not running). Fall back to direct file read.
	if *remoteFlag != "" {
		return fmt.Errorf("failed to connect to remote relay: %w", clientErr)
	}

	authKeysPath, err := loadRelayAuthKeysPathErr(configFile)
	if err != nil {
		return err
	}
	peers, err := auth.ListPeers(authKeysPath)
	if err != nil {
		return fmt.Errorf("failed to list peers: %w", err)
	}

	apiPeers := make([]relay.AuthorizedPeerInfo, len(peers))
	for i, p := range peers {
		role := p.Role
		if role == "" {
			role = "member"
		}
		apiPeers[i] = relay.AuthorizedPeerInfo{
			PeerID:   p.PeerID.String(),
			Role:     role,
			Comment:  p.Comment,
			Verified: p.Verified,
			Group:    p.Group,
		}
		if !p.ExpiresAt.IsZero() {
			apiPeers[i].ExpiresAt = p.ExpiresAt.Format(time.RFC3339)
		}
	}
	printAuthorizedPeers(stdout, apiPeers, wide)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Connected peers: (relay not running)")
	return nil
}

func listPeersViaClient(client relay.RelayAdminAPI, stdout io.Writer, wide bool) error {
	// Fetch authorized peers.
	authPeers, err := client.ListPeers()
	if err != nil {
		// Non-fatal: public seed may have no authorized_keys configured.
		fmt.Fprintln(stdout, "Authorized peers: (not configured)")
	} else {
		printAuthorizedPeers(stdout, authPeers, wide)
	}

	fmt.Fprintln(stdout)

	// Fetch connected peers.
	connected, err := client.ListConnectedPeers()
	if err != nil {
		fmt.Fprintf(stdout, "Connected peers: error (%v)\n", err)
		return nil
	}

	if len(connected) == 0 {
		fmt.Fprintln(stdout, "Connected peers: (none)")
	} else {
		fmt.Fprintf(stdout, "Connected peers (%d):\n\n", len(connected))
		for _, p := range connected {
			pid := formatPeerID(p.PeerID, wide)
			agent := validate.SanitizeForDisplay(p.AgentVersion)
			if agent == "" {
				agent = "unknown"
			}

			dur := formatDuration(p.DurationSecs)

			authTag := "[unknown]"
			if p.Authorized {
				authTag = "[" + p.Role + "]"
			}

			ip := p.IP
			if ip == "" {
				ip = "-"
			}

			if p.Comment != "" {
				fmt.Fprintf(stdout, "  %-*s  %-24s  %-8s %-5s %-39s %6s  %s  # %s\n",
					peerIDWidth(wide), pid, agent, p.Direction, p.Transport, ip, dur, authTag, validate.SanitizeForDisplay(p.Comment))
			} else {
				fmt.Fprintf(stdout, "  %-*s  %-24s  %-8s %-5s %-39s %6s  %s\n",
					peerIDWidth(wide), pid, agent, p.Direction, p.Transport, ip, dur, authTag)
			}
		}
	}
	return nil
}

func printAuthorizedPeers(stdout io.Writer, peers []relay.AuthorizedPeerInfo, wide bool) {
	if len(peers) == 0 {
		fmt.Fprintln(stdout, "Authorized peers: (none)")
		return
	}

	fmt.Fprintf(stdout, "Authorized peers (%d):\n\n", len(peers))
	for _, p := range peers {
		role := p.Role
		if role == "" {
			role = "member"
		}
		tags := "[" + role + "]"
		if p.Verified != "" {
			tags += " [verified]"
		} else {
			tags += " [UNVERIFIED]"
		}
		pid := formatPeerID(p.PeerID, wide)
		if p.Comment != "" {
			fmt.Fprintf(stdout, "  %s  %s  # %s\n", pid, tags, validate.SanitizeForDisplay(p.Comment))
		} else {
			fmt.Fprintf(stdout, "  %s  %s\n", pid, tags)
		}
	}
}

func peerIDWidth(wide bool) int {
	if wide {
		return 52 // full peer ID
	}
	return 19 // 16 chars + "..."
}

func formatDuration(secs int) string {
	d := time.Duration(secs) * time.Second
	if d < time.Minute {
		return fmt.Sprintf("%ds", secs)
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", secs/60)
	}
	h := secs / 3600
	m := (secs % 3600) / 60
	if h >= 24 {
		days := h / 24
		h = h % 24
		return fmt.Sprintf("%dd%dh", days, h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

func runRelayListPeers(args []string, configFile string) {
	if err := doRelayListPeers(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func runRelayInfo(configFile string) {
	cfg, err := config.LoadRelayServerConfig(configFile)
	if err != nil {
		fatal("Failed to load config: %v", err)
	}

	// Read encrypted SHRL identity key (read-only, don't auto-create).
	relayConfigDir := filepath.Dir(configFile)
	pw, pwErr := resolvePasswordInteractive(relayConfigDir, os.Stdout)
	if pwErr != nil {
		fatal("Identity error: %v", pwErr)
	}
	priv, err := identity.LoadIdentity(cfg.Identity.KeyFile, pw)
	if err != nil {
		fatal("Invalid identity key %s: %v", cfg.Identity.KeyFile, err)
	}
	peerID, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		fatal("Failed to derive peer ID: %v", err)
	}

	fmt.Printf("Peer ID: %s\n", peerID)

	// Connection gating status
	if cfg.Security.EnableConnectionGating {
		fmt.Println("Connection gating: enabled")
	} else {
		fmt.Println("Connection gating: DISABLED")
	}

	// Authorized peers count
	if cfg.Security.AuthorizedKeysFile != "" {
		peers, err := auth.ListPeers(cfg.Security.AuthorizedKeysFile)
		if err == nil {
			fmt.Printf("Authorized peers: %d\n", len(peers))
		}
	}
	fmt.Println()

	// Detect public IPs and construct multiaddrs for all configured transports
	publicIPs := detectPublicIPs()
	multiaddrs := buildPublicMultiaddrs(cfg.Network.ListenAddresses, publicIPs, peerID)

	if len(multiaddrs) > 0 {
		fmt.Println("Multiaddrs:")
		for _, maddr := range multiaddrs {
			fmt.Printf("  %s\n", maddr)
		}

		// Find primary TCP multiaddr (IPv4 preferred) for QR code and quick setup
		primaryAddr := ""
		primaryPort := ""
		for _, maddr := range multiaddrs {
			if strings.Contains(maddr, "/ip4/") && strings.Contains(maddr, "/tcp/") && !strings.Contains(maddr, "/ws") {
				primaryAddr = maddr
				primaryPort = extractTCPPort([]string{maddr})
				break
			}
		}
		if primaryAddr == "" && len(multiaddrs) > 0 {
			primaryAddr = multiaddrs[0]
			primaryPort = extractTCPPort([]string{multiaddrs[0]})
		}

		// QR code (use qrencode if available)
		if primaryAddr != "" {
			if qrPath, err := exec.LookPath("qrencode"); err == nil && qrPath != "" {
				fmt.Println()
				fmt.Println("Scan this QR code during 'shurli init':")
				cmd := exec.Command("qrencode", "-t", "ANSIUTF8", primaryAddr)
				cmd.Stdout = os.Stdout
				_ = cmd.Run()
			}
		}

		fmt.Println()
		fmt.Println("Quick setup:")
		for _, ip := range publicIPs {
			if !strings.Contains(ip, ":") && primaryPort != "" {
				fmt.Printf("  shurli init  →  enter: %s:%s\n", ip, primaryPort)
			}
		}
		fmt.Printf("  Peer ID: %s\n", peerID)
	} else {
		fmt.Println("Multiaddrs: could not detect public IPs")
	}
}

func runRelayServerVersion() {
	fmt.Printf("shurli relay %s (%s) built %s\n", version, commit, buildDate)
	fmt.Printf("Go %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func runRelayServerConfig(args []string, configFile string) {
	if len(args) < 1 {
		fmt.Println("Usage: shurli relay config <command>")
		fmt.Println()
		fmt.Println("Commands:")
		fmt.Println("  show        Show resolved relay config")
		fmt.Println("  validate    Validate relay-server.yaml without starting")
		fmt.Println("  rollback    Restore last-known-good config")
		osExit(1)
	}
	switch args[0] {
	case "show":
		runRelayServerConfigShow(configFile)
	case "validate":
		runRelayServerConfigValidate(configFile)
	case "rollback":
		runRelayServerConfigRollback(configFile)
	default:
		fmt.Fprintf(os.Stderr, "Unknown config command: %s\n", args[0])
		osExit(1)
	}
}

func doRelayServerConfigShow(configFile string, stdout io.Writer) error {
	cfg, err := config.LoadRelayServerConfig(configFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %v", err)
	}
	if err := config.ValidateRelayServerConfig(cfg); err != nil {
		tc.Wyellow(stdout, "WARNING: ")
		fmt.Fprintf(stdout, "config has validation errors: %v\n\n", err)
	}

	tc.Wfaint(stdout, "# Resolved config from %s\n", configFile)
	fmt.Fprintln(stdout)

	// Identity
	tc.Wblue(stdout, "Name:       ")
	if cfg.Name != "" {
		fmt.Fprintln(stdout, cfg.Name)
	} else {
		tc.Wfaint(stdout, "(not set)\n")
	}
	tc.Wblue(stdout, "Key file:   ")
	fmt.Fprintln(stdout, cfg.Identity.KeyFile)
	tc.Wblue(stdout, "Config:     ")
	fmt.Fprintln(stdout, configFile)
	fmt.Fprintln(stdout)

	// Network
	tc.Wblue(stdout, "Listen:     ")
	if len(cfg.Network.ListenAddresses) > 0 {
		fmt.Fprintln(stdout, strings.Join(cfg.Network.ListenAddresses, ", "))
	} else {
		tc.Wfaint(stdout, "(default)\n")
	}
	tc.Wblue(stdout, "Network:    ")
	if cfg.Discovery.Network != "" {
		fmt.Fprintln(stdout, cfg.Discovery.Network)
	} else {
		fmt.Fprintln(stdout, "global (default)")
	}
	fmt.Fprintln(stdout)

	// Security
	tc.Wblue(stdout, "Auth:       ")
	if cfg.Security.EnableConnectionGating {
		tc.Wgreen(stdout, "enabled\n")
	} else {
		tc.Wred(stdout, "disabled\n")
	}
	tc.Wblue(stdout, "Auth keys:  ")
	fmt.Fprintln(stdout, cfg.Security.AuthorizedKeysFile)
	tc.Wblue(stdout, "Data relay: ")
	if cfg.Security.EnableDataRelay {
		tc.Wyellow(stdout, "enabled")
		fmt.Fprintln(stdout, " (all peers can relay data)")
	} else {
		tc.Wgreen(stdout, "disabled")
		fmt.Fprintln(stdout, " (signaling only)")
	}
	tc.Wblue(stdout, "Invite:     ")
	policy := cfg.Security.InvitePolicy
	if policy == "" {
		policy = "admin-only"
	}
	fmt.Fprintln(stdout, policy)
	tc.Wblue(stdout, "Vault:      ")
	if cfg.Security.VaultFile != "" {
		fmt.Fprintln(stdout, cfg.Security.VaultFile)
	} else {
		tc.Wfaint(stdout, "(none)\n")
	}
	tc.Wblue(stdout, "TOTP:       ")
	if cfg.Security.RequireTOTP {
		tc.Wgreen(stdout, "required\n")
	} else {
		tc.Wfaint(stdout, "not required\n")
	}
	if cfg.Security.AutoSealMinutes > 0 {
		tc.Wblue(stdout, "Auto-seal:  ")
		fmt.Fprintf(stdout, "%d minutes\n", cfg.Security.AutoSealMinutes)
	}
	fmt.Fprintln(stdout)

	// Resources
	tc.Wblue(stdout, "Max reservations: ")
	fmt.Fprintln(stdout, cfg.Resources.MaxReservations)
	tc.Wblue(stdout, "Max circuits:     ")
	fmt.Fprintln(stdout, cfg.Resources.MaxCircuits)
	tc.Wblue(stdout, "Session duration: ")
	fmt.Fprintln(stdout, cfg.Resources.SessionDuration)
	tc.Wblue(stdout, "Session data:     ")
	fmt.Fprintln(stdout, cfg.Resources.SessionDataLimit)
	fmt.Fprintln(stdout)

	// Telemetry
	tc.Wblue(stdout, "Metrics:    ")
	if cfg.Telemetry.Metrics.Enabled {
		tc.Wgreen(stdout, "enabled")
		fmt.Fprintf(stdout, " (%s)\n", cfg.Telemetry.Metrics.ListenAddress)
	} else {
		tc.Wfaint(stdout, "disabled\n")
	}
	tc.Wblue(stdout, "Audit log:  ")
	if cfg.Telemetry.Audit.Enabled {
		tc.Wgreen(stdout, "enabled\n")
	} else {
		tc.Wfaint(stdout, "disabled\n")
	}
	tc.Wblue(stdout, "Health:     ")
	if cfg.Health.Enabled {
		tc.Wgreen(stdout, "enabled")
		fmt.Fprintf(stdout, " (%s)\n", cfg.Health.ListenAddress)
	} else {
		tc.Wfaint(stdout, "disabled\n")
	}
	fmt.Fprintln(stdout)

	// Archive
	if config.HasArchive(configFile) {
		tc.Wfaint(stdout, "# Last-known-good archive: %s\n", config.ArchivePath(configFile))
	} else {
		tc.Wfaint(stdout, "# No archive yet (created on next successful serve)\n")
	}
	return nil
}

func runRelayServerConfigShow(configFile string) {
	if err := doRelayServerConfigShow(configFile, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		osExit(1)
	}
}

func doRelayServerConfigValidate(configFile string, stdout io.Writer) error {
	cfg, err := config.LoadRelayServerConfig(configFile)
	if err != nil {
		return fmt.Errorf("FAIL: %v", err)
	}
	if err := config.ValidateRelayServerConfig(cfg); err != nil {
		return fmt.Errorf("FAIL: %v", err)
	}
	fmt.Fprintf(stdout, "OK: %s is valid\n", configFile)
	return nil
}

func runRelayServerConfigValidate(configFile string) {
	if err := doRelayServerConfigValidate(configFile, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		osExit(1)
	}
}

func doRelayServerConfigRollback(configFile string, stdout io.Writer) error {
	if !config.HasArchive(configFile) {
		return fmt.Errorf("no last-known-good archive for %s\nArchives are created automatically on each successful relay startup", configFile)
	}
	if err := config.Rollback(configFile); err != nil {
		return fmt.Errorf("rollback failed: %w", err)
	}
	fmt.Fprintf(stdout, "Restored %s from last-known-good archive\n", configFile)
	fmt.Fprintln(stdout, "Config restored. Restart relay to apply all changes.")
	return nil
}

func runRelayServerConfigRollback(configFile string) {
	if err := doRelayServerConfigRollback(configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

// buildPublicMultiaddrs constructs public multiaddrs from listen addresses by
// replacing bind addresses (0.0.0.0, ::) with detected public IPs.
// Handles all transport types: TCP, QUIC, WebSocket, WebTransport.
func buildPublicMultiaddrs(listenAddrs []string, publicIPs []string, peerID peer.ID) []string {
	var result []string
	for _, listen := range listenAddrs {
		for _, ip := range publicIPs {
			isIPv6 := strings.Contains(ip, ":")
			proto := "ip4"
			if isIPv6 {
				proto = "ip6"
			}
			// Only match listen addresses with the same IP version
			if strings.HasPrefix(listen, "/ip4/") && isIPv6 {
				continue
			}
			if strings.HasPrefix(listen, "/ip6/") && !isIPv6 {
				continue
			}
			// Replace the bind address with the public IP
			maddr := listen
			maddr = strings.Replace(maddr, "/ip4/0.0.0.0", "/"+proto+"/"+ip, 1)
			maddr = strings.Replace(maddr, "/ip6/::", "/"+proto+"/"+ip, 1)
			maddr += "/p2p/" + peerID.String()
			result = append(result, maddr)
		}
	}
	return result
}

// buildRelayAddr constructs a relay multiaddr from the relay server config and a known peer ID.
func buildRelayAddr(cfg *config.RelayServerConfig, pid peer.ID) (string, error) {
	if len(cfg.Network.ListenAddresses) == 0 {
		return "", fmt.Errorf("no listen addresses in relay config")
	}

	// Find a suitable listen address (prefer TCP for invite code encoding).
	var addr string
	for _, a := range cfg.Network.ListenAddresses {
		if strings.Contains(a, "/tcp/") && !strings.Contains(a, "/ws") {
			addr = a
			break
		}
	}
	if addr == "" {
		addr = cfg.Network.ListenAddresses[0]
	}

	// Replace 0.0.0.0 with a detected public IP.
	if strings.Contains(addr, "/0.0.0.0/") {
		publicIPs := detectPublicIPs()
		if len(publicIPs) > 0 {
			addr = strings.Replace(addr, "/0.0.0.0/", "/"+publicIPs[0]+"/", 1)
		} else {
			return "", fmt.Errorf("relay listens on 0.0.0.0 but no public IP detected; specify a public address in config")
		}
	}

	return addr + "/p2p/" + pid.String(), nil
}

// detectPublicIPs returns non-private, non-loopback IP addresses from network interfaces.
func detectPublicIPs() []string {
	var ips []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP
		if !ip.IsGlobalUnicast() {
			continue
		}
		// Skip private IPv4 (10/8, 172.16/12, 192.168/16)
		if ip4 := ip.To4(); ip4 != nil {
			if ip4[0] == 10 ||
				(ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31) ||
				(ip4[0] == 192 && ip4[1] == 168) {
				continue
			}
		}
		// Skip ULA IPv6 (fc00::/7)
		if ip.To4() == nil && len(ip) == net.IPv6len && (ip[0]&0xfe) == 0xfc {
			continue
		}
		ips = append(ips, ip.String())
	}
	return ips
}

// extractTCPPort finds the first TCP port from multiaddr listen addresses.
func extractTCPPort(listenAddresses []string) string {
	for _, addr := range listenAddresses {
		parts := strings.Split(addr, "/")
		for i, part := range parts {
			if part == "tcp" && i+1 < len(parts) {
				return parts[i+1]
			}
		}
	}
	return "7777"
}

func runRelayVerify(args []string, configFile string) {
	if err := doRelayVerify(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayVerify(args []string, configFile string, stdout io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: shurli relay verify <peer-id>\n\nVerify a peer's identity using a Short Authentication String (SAS).\nBoth sides must see the same code for the connection to be authentic.")
	}

	target := args[0]

	// Load relay config.
	cfg, err := config.LoadRelayServerConfig(configFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Resolve target to peer ID.
	targetPeerID, err := peer.Decode(target)
	if err != nil {
		return fmt.Errorf("invalid peer ID: %q\n  Must be a valid peer ID (e.g. 12D3KooW...)", target)
	}

	// Check peer is authorized on this relay.
	authKeysPath := cfg.Security.AuthorizedKeysFile
	if authKeysPath == "" {
		return fmt.Errorf("no authorized_keys_file configured")
	}
	peers, err := auth.ListPeers(authKeysPath)
	if err != nil {
		return fmt.Errorf("failed to list peers: %w", err)
	}

	var displayName string
	found := false
	for _, p := range peers {
		if p.PeerID == targetPeerID {
			found = true
			displayName = p.Comment
			if p.Verified != "" {
				fmt.Fprintf(stdout, "Peer is already verified (fingerprint: %s).\n", p.Verified)
				fmt.Fprint(stdout, "Re-verify? [y/N]: ")
				reader := bufio.NewReader(os.Stdin)
				answer, _ := reader.ReadString('\n')
				answer = strings.TrimSpace(strings.ToLower(answer))
				if answer != "y" && answer != "yes" {
					return nil
				}
			}
			break
		}
	}
	if !found {
		return fmt.Errorf("peer not authorized on this relay: %s...\n  Authorize first: shurli relay authorize %s", targetPeerID.String()[:16], target)
	}

	// Load relay's own identity.
	relayConfigDir := filepath.Dir(configFile)
	pw, pwErr := resolvePasswordInteractive(relayConfigDir, stdout)
	if pwErr != nil {
		return fmt.Errorf("identity error: %w", pwErr)
	}
	ourPeerID, err := identity.PeerIDFromKeyFile(cfg.Identity.KeyFile, pw)
	if err != nil {
		return fmt.Errorf("failed to load relay identity: %w", err)
	}

	// Compute fingerprint.
	emoji, numeric := sdk.ComputeFingerprint(ourPeerID, targetPeerID)
	prefix := sdk.FingerprintPrefix(ourPeerID, targetPeerID)

	// Display.
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "=== Relay Peer Verification ===")
	fmt.Fprintln(stdout)
	if displayName != "" {
		fmt.Fprintf(stdout, "Peer:     %s (%s...)\n", displayName, targetPeerID.String()[:16])
	} else {
		fmt.Fprintf(stdout, "Peer:     %s...\n", targetPeerID.String()[:16])
	}
	fmt.Fprintf(stdout, "Relay ID: %s...\n", ourPeerID.String()[:16])
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "Verification code:  %s\n", emoji)
	fmt.Fprintf(stdout, "Numeric code:       %s\n", numeric)
	fmt.Fprintln(stdout)

	if displayName != "" {
		fmt.Fprintf(stdout, "Compare this with %s over a secure channel\n", displayName)
	} else {
		fmt.Fprintln(stdout, "Compare this with the peer over a secure channel")
	}
	fmt.Fprintln(stdout, "(phone call, in person, trusted messaging).")
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "The peer should run: shurli verify <this-relay-name-or-id>\n")
	fmt.Fprintln(stdout)

	// Prompt for confirmation.
	fmt.Fprint(stdout, "Does the peer see the same code? [y/N]: ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))

	if answer != "y" && answer != "yes" {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Verification cancelled. Peer remains unverified.")
		return nil
	}

	// Write verified attribute.
	if err := auth.SetPeerAttr(authKeysPath, targetPeerID.String(), "verified", prefix); err != nil {
		return fmt.Errorf("failed to mark peer as verified: %w", err)
	}

	fmt.Fprintln(stdout)
	if displayName != "" {
		fmt.Fprintf(stdout, "Peer \"%s\" verified!\n", displayName)
	} else {
		fmt.Fprintln(stdout, "Peer verified!")
	}
	return nil
}

func printRelayServeUsage() {
	fmt.Println("Usage: shurli relay <command> [options]")
	fmt.Println()
	fmt.Println("Client configuration:")
	fmt.Println("  add    <address> [--peer-id <ID>]   Add a relay server address")
	fmt.Println("  list                                List configured relay addresses")
	fmt.Println("  remove <multiaddr>                  Remove a relay server address")
	fmt.Println()
	fmt.Println("Relay server management (local or --remote):")
	fmt.Println("  authorize <peer-id> [comment]       Allow a peer to use this relay")
	fmt.Println("  deauthorize <peer-id>               Remove a peer's access")
	fmt.Println("  set-attr <peer> <key> <value>       Set peer attribute (role, group, etc.)")
	fmt.Println("  grant <peer-id> [--duration 1h]     Grant time-limited data relay access")
	fmt.Println("  grants                              List active data relay grants")
	fmt.Println("  revoke <peer-id>                    Revoke data relay access")
	fmt.Println("  extend <peer-id> --duration 2h      Extend data relay grant")
	fmt.Println("  list-peers                          List authorized peers")
	fmt.Println("  seal                                Seal vault (watch-only mode)")
	fmt.Println("  unseal                              Unseal vault")
	fmt.Println("  seal-status                         Show vault seal status")
	fmt.Println("  invite create [--ttl 1h]            Generate an invite code")
	fmt.Println("  invite list                         List active invites")
	fmt.Println("  invite revoke <id>                  Revoke an invite")
	fmt.Println("  motd <subcommand>                   Manage relay MOTD")
	fmt.Println("  goodbye <subcommand>                Manage goodbye announcements")
	fmt.Println()
	fmt.Println("Relay server management (local only):")
	fmt.Println("  setup                               Initialize relay config (backup/restore)")
	fmt.Println("  serve                               Start the relay server")
	fmt.Println("  info                                Show peer ID, multiaddrs, QR code")
	fmt.Println("  verify <peer-id>                    Verify a peer's identity (SAS)")
	fmt.Println("  show                                Show resolved relay config")
	fmt.Println("  config <subcommand>                 Config management (show/validate/rollback)")
	fmt.Println("  vault init [--totp] [--auto-seal N] Initialize passphrase-sealed vault")
	fmt.Println("  recover                             Recover identity from seed phrase")
	fmt.Println("  version                             Show relay version")
	fmt.Println()
	fmt.Println("All remote-capable commands accept: --remote <multiaddr|name|peer-id>")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  shurli relay add /ip4/203.0.113.50/tcp/7777/p2p/12D3KooW...")
	fmt.Println("  shurli relay serve --config /etc/shurli/relay-server.yaml")
	fmt.Println("  shurli relay authorize 12D3KooW... home-node")
	fmt.Println("  shurli relay list-peers --remote 12D3KooW...")
	fmt.Println("  shurli relay unseal --remote my-relay")
	fmt.Println()
	fmt.Println("Server commands use relay-server.yaml in the working directory by default.")
	fmt.Println("Local commands support --config <path>.")
	fmt.Println()
	fmt.Println("Signaling vs Data Grants:")
	fmt.Println("  By default, relays provide signaling only (peer discovery, ping, hole-punching).")
	fmt.Println("  Data transfer (browse, download, send, proxy) requires an active data grant.")
	fmt.Println("  Use 'shurli relay grant' to enable data relay for specific peers.")
}
