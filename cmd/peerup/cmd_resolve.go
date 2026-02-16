package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

func runResolve(args []string) {
	fs := flag.NewFlagSet("resolve", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: peerup resolve [--config <path>] [--json] <name>")
		os.Exit(1)
	}

	name := remaining[0]

	// Load configuration
	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	// Create a minimal name resolver (no P2P host needed)
	resolver := p2pnet.NewNameResolver()
	if cfg.Names != nil {
		if err := resolver.LoadFromMap(cfg.Names); err != nil {
			log.Fatalf("Failed to load names: %v", err)
		}
	}

	// Resolve
	peerID, err := resolver.Resolve(name)
	source := "local_config"
	if err != nil {
		log.Fatalf("Cannot resolve %q: %v", name, err)
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
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
		return
	}

	fmt.Printf("%s → %s\n", name, peerID.String())
}
