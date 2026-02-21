package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/satindergrewal/peer-up/internal/auth"
	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

func runStatus(args []string) {
	if err := doStatus(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doStatus(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Version
	fmt.Fprintf(stdout, "peerup %s (%s) built %s\n", version, commit, buildDate)
	fmt.Fprintln(stdout)

	// Find and load config
	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		fmt.Fprintf(stdout, "Config:   not found (%v)\n", err)
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Run 'peerup init' to create a configuration.")
		return fmt.Errorf("config not found: %w", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	// Peer ID
	peerID, err := p2pnet.PeerIDFromKeyFile(cfg.Identity.KeyFile)
	if err != nil {
		fmt.Fprintf(stdout, "Peer ID:  error (%v)\n", err)
	} else {
		fmt.Fprintf(stdout, "Peer ID:  %s\n", peerID)
	}
	fmt.Fprintf(stdout, "Config:   %s\n", cfgFile)
	fmt.Fprintf(stdout, "Key file: %s\n", cfg.Identity.KeyFile)
	if cfg.Discovery.Network != "" {
		fmt.Fprintf(stdout, "Network:  %s\n", cfg.Discovery.Network)
	} else {
		fmt.Fprintf(stdout, "Network:  global (default)\n")
	}
	fmt.Fprintln(stdout)

	// Relay addresses
	if len(cfg.Relay.Addresses) > 0 {
		fmt.Fprintln(stdout, "Relay addresses:")
		for _, addr := range cfg.Relay.Addresses {
			fmt.Fprintf(stdout, "  %s\n", addr)
		}
	} else {
		fmt.Fprintln(stdout, "Relay addresses: (none configured)")
	}
	fmt.Fprintln(stdout)

	// Authorized peers
	if cfg.Security.AuthorizedKeysFile != "" {
		peers, err := auth.ListPeers(cfg.Security.AuthorizedKeysFile)
		if err != nil {
			fmt.Fprintf(stdout, "Authorized peers: error (%v)\n", err)
		} else if len(peers) == 0 {
			fmt.Fprintln(stdout, "Authorized peers: (none)")
		} else {
			fmt.Fprintf(stdout, "Authorized peers (%d):\n", len(peers))
			for _, p := range peers {
				short := p.PeerID
				if len(short) > 16 {
					short = short[:16] + "..."
				}
				if p.Comment != "" {
					fmt.Fprintf(stdout, "  %s  # %s\n", short, p.Comment)
				} else {
					fmt.Fprintf(stdout, "  %s\n", short)
				}
			}
		}
	} else {
		fmt.Fprintln(stdout, "Authorized peers: connection gating disabled")
	}
	fmt.Fprintln(stdout)

	// Services
	if cfg.Services != nil && len(cfg.Services) > 0 {
		fmt.Fprintln(stdout, "Services:")
		for name, svc := range cfg.Services {
			state := "enabled"
			if !svc.Enabled {
				state = "disabled"
			}
			fmt.Fprintf(stdout, "  %-12s -> %-20s (%s)\n", name, svc.LocalAddress, state)
		}
	} else {
		fmt.Fprintln(stdout, "Services: (none configured)")
	}
	fmt.Fprintln(stdout)

	// Names
	if cfg.Names != nil && len(cfg.Names) > 0 {
		fmt.Fprintln(stdout, "Names:")
		for name, peerIDStr := range cfg.Names {
			short := peerIDStr
			if len(short) > 16 {
				short = short[:16] + "..."
			}
			fmt.Fprintf(stdout, "  %-12s -> %s\n", name, short)
		}
	} else {
		fmt.Fprintln(stdout, "Names: (none configured)")
	}
	return nil
}
