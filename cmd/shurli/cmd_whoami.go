package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/identity"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

func runWhoami(args []string) {
	if err := doWhoami(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doWhoami(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	pw, _ := resolvePassword(filepath.Dir(cfgFile))
	priv, err := p2pnet.LoadOrCreateIdentity(cfg.Identity.KeyFile, pw)
	if err != nil {
		return fmt.Errorf("failed to load identity: %w", err)
	}

	masterID, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("failed to derive peer ID: %w", err)
	}

	// If a namespace is configured, show the namespace-specific peer ID
	// that the node actually uses on the network.
	ns := cfg.Discovery.Network
	if ns != "" {
		nsKey, err := identity.DeriveNamespaceKey(priv, ns)
		if err != nil {
			return fmt.Errorf("failed to derive namespace identity: %w", err)
		}
		nsID, err := peer.IDFromPrivateKey(nsKey)
		if err != nil {
			return fmt.Errorf("failed to derive namespace peer ID: %w", err)
		}
		fmt.Fprintf(stdout, "%s  (network: %s)\n", nsID.String(), ns)
		fmt.Fprintf(stdout, "Master ID: %s\n", masterID.String())
	} else {
		fmt.Fprintln(stdout, masterID.String())
	}

	return nil
}
