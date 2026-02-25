package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/shurlinet/shurli/internal/config"
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

	peerID, err := p2pnet.PeerIDFromKeyFile(cfg.Identity.KeyFile)
	if err != nil {
		return fmt.Errorf("failed to load identity: %w", err)
	}

	fmt.Fprintln(stdout, peerID.String())
	return nil
}
