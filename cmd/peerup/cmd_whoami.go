package main

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"

	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

func runWhoami(args []string) {
	fs := flag.NewFlagSet("whoami", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fs.Parse(args)

	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	peerID, err := p2pnet.PeerIDFromKeyFile(cfg.Identity.KeyFile)
	if err != nil {
		log.Fatalf("Failed to load identity: %v", err)
	}

	fmt.Println(peerID.String())
}
