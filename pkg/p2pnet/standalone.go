package p2pnet

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/config"
)

// StandaloneConfig holds parameters for creating a standalone P2P host
// for one-shot CLI commands (ping, traceroute, proxy) that operate
// without a running daemon.
type StandaloneConfig struct {
	// ConfigPath is the explicit config file path (empty = auto-detect).
	ConfigPath string

	// Password is the identity key password (from session token or prompt).
	Password string

	// UserAgent is the libp2p user agent string (e.g., "shurli/1.0.0").
	UserAgent string
}

// StandaloneResult holds the outputs of NewStandaloneHost.
type StandaloneResult struct {
	// Network is the P2P network instance. Caller must defer Network.Close().
	Network *Network

	// NodeConfig is the loaded and resolved node configuration.
	NodeConfig *config.NodeConfig

	// ConfigDir is the directory containing the config file,
	// useful for resolving relative paths.
	ConfigDir string
}

// NewStandaloneHost creates a P2P Network from a config file for standalone
// CLI commands. It loads configuration, creates the libp2p host, and loads
// peer names. The caller is responsible for closing the returned Network.
func NewStandaloneHost(cfg StandaloneConfig) (*StandaloneResult, error) {
	cfgFile, err := config.FindConfigFile(cfg.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("config error: %w", err)
	}
	nodeCfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		return nil, fmt.Errorf("config error: %w", err)
	}
	cfgDir := filepath.Dir(cfgFile)
	config.ResolveConfigPaths(nodeCfg, cfgDir)

	net, err := New(&Config{
		KeyFile:               nodeCfg.Identity.KeyFile,
		KeyPassword:           cfg.Password,
		Config:                &config.Config{Network: nodeCfg.Network},
		UserAgent:             cfg.UserAgent,
		Namespace:             nodeCfg.Discovery.Network,
		EnableRelay:           true,
		RelayAddrs:            nodeCfg.Relay.Addresses,
		ForcePrivate:          nodeCfg.Network.ForcePrivateReachability,
		EnableNATPortMap:      true,
		EnableHolePunching:    true,
		ResourceLimitsEnabled: true,
	})
	if err != nil {
		return nil, fmt.Errorf("P2P network error: %w", err)
	}

	if nodeCfg.Names != nil {
		net.LoadNames(nodeCfg.Names)
	}

	return &StandaloneResult{
		Network:    net,
		NodeConfig: nodeCfg,
		ConfigDir:  cfgDir,
	}, nil
}

// ResolveAndConnect resolves a target name to a peer ID, bootstraps the DHT,
// and connects to the target. This consolidates the repeated boilerplate in
// standalone CLI commands (ping, traceroute) into a single library call.
//
// After this returns, the caller can immediately use the host to communicate
// with the target peer. The caller is still responsible for closing the Network.
func (r *StandaloneResult) ResolveAndConnect(ctx context.Context, target string) (peer.ID, error) {
	targetPeerID, err := r.Network.ResolveName(target)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %q: %w", target, err)
	}

	cfg := r.NodeConfig
	bootstrapCfg := BootstrapConfig{
		Namespace:      cfg.Discovery.Network,
		BootstrapPeers: cfg.Discovery.BootstrapPeers,
		RelayAddrs:     cfg.Relay.Addresses,
	}

	if err := BootstrapAndConnect(ctx, r.Network.Host(), r.Network, targetPeerID, bootstrapCfg); err != nil {
		return "", fmt.Errorf("connect failed: %w", err)
	}

	return targetPeerID, nil
}
