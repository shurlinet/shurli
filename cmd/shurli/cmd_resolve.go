package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

func runResolve(args []string) {
	if err := doResolve(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doResolve(args []string, stdout io.Writer) error {
	args = reorderArgs(args, map[string]bool{"json": true})

	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	remaining := fs.Args()
	if len(remaining) < 1 {
		return fmt.Errorf("usage: shurli resolve [--config <path>] [--json] <name>")
	}

	name := remaining[0]

	// Load configuration
	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	// Create a minimal name resolver (no P2P host needed)
	resolver := p2pnet.NewNameResolver()
	if cfg.Names != nil {
		if err := resolver.LoadFromMap(cfg.Names); err != nil {
			return fmt.Errorf("failed to load names: %w", err)
		}
	}

	// Resolve
	peerID, err := resolver.Resolve(name)
	source := "local_config"
	if err != nil {
		return fmt.Errorf("cannot resolve %q: %w", name, err)
	}

	// Check if input was already a peer ID
	if _, parseErr := peer.Decode(name); parseErr == nil {
		source = "peer_id"
	}

	if *jsonFlag {
		resp := struct {
			Name   string `json:"name"`
			PeerID string `json:"peer_id"`
			Source string `json:"source"`
		}{
			Name:   name,
			PeerID: peerID.String(),
			Source:  source,
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
		return nil
	}

	fmt.Fprintf(stdout, "%s → %s\n", name, peerID.String())
	return nil
}
