package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/satindergrewal/peer-up/internal/auth"
	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/internal/identity"
	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

func runVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: peerup verify <peer-name-or-id> [--config path]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Verify a peer's identity using a Short Authentication String (SAS).")
		fmt.Fprintln(os.Stderr, "Both sides must see the same code for the connection to be authentic.")
		osExit(1)
	}

	target := fs.Arg(0)

	// Load config.
	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		fatal("Config error: %v\nRun 'peerup init' first.", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		fatal("Config error: %v", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	// Resolve target to peer ID.
	var targetPeerID peer.ID
	var displayName string

	// Try names first.
	if cfg.Names != nil {
		if pidStr, ok := cfg.Names[target]; ok {
			targetPeerID, err = peer.Decode(pidStr)
			if err != nil {
				fatal("Invalid peer ID for name %q: %v", target, err)
			}
			displayName = target
		}
	}

	// Try as raw peer ID.
	if targetPeerID == "" {
		targetPeerID, err = peer.Decode(target)
		if err != nil {
			fatal("Unknown peer: %q (not in names and not a valid peer ID)", target)
		}
		// Look up name by peer ID.
		for name, pidStr := range cfg.Names {
			if pidStr == targetPeerID.String() {
				displayName = name
				break
			}
		}
	}

	// Load our own identity.
	ourPeerID, err := identity.PeerIDFromKeyFile(cfg.Identity.KeyFile)
	if err != nil {
		fatal("Failed to load identity: %v", err)
	}

	// Compute fingerprint.
	emoji, numeric := p2pnet.ComputeFingerprint(ourPeerID, targetPeerID)
	prefix := p2pnet.FingerprintPrefix(ourPeerID, targetPeerID)

	// Display.
	fmt.Println()
	fmt.Println("=== Peer Verification ===")
	fmt.Println()
	if displayName != "" {
		fmt.Printf("Peer:    %s (%s...)\n", displayName, targetPeerID.String()[:16])
	} else {
		fmt.Printf("Peer:    %s...\n", targetPeerID.String()[:16])
	}
	fmt.Printf("Your ID: %s...\n", ourPeerID.String()[:16])
	fmt.Println()
	fmt.Printf("Verification code:  %s\n", emoji)
	fmt.Printf("Numeric code:       %s\n", numeric)
	fmt.Println()

	if displayName != "" {
		fmt.Printf("Compare this with %s over a secure channel\n", displayName)
	} else {
		fmt.Println("Compare this with the other peer over a secure channel")
	}
	fmt.Println("(phone call, in person, trusted messaging).")
	fmt.Println()

	// Prompt for confirmation.
	fmt.Print("Does the other side see the same code? [y/N]: ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))

	if answer != "y" && answer != "yes" {
		fmt.Println()
		fmt.Println("Verification cancelled. Peer remains unverified.")
		return
	}

	// Write verified attribute.
	if err := auth.SetPeerAttr(cfg.Security.AuthorizedKeysFile, targetPeerID.String(), "verified", prefix); err != nil {
		fatal("Failed to mark peer as verified: %v", err)
	}

	fmt.Println()
	if displayName != "" {
		fmt.Printf("Peer \"%s\" verified!\n", displayName)
	} else {
		fmt.Println("Peer verified!")
	}
}
