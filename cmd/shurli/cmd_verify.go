package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/identity"
	"github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/pkg/sdk"
)

func runVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fs.Parse(reorderFlags(fs, args))

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: shurli verify <peer-name-or-id> [--config path]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Verify a peer's identity using a Short Authentication String (SAS).")
		fmt.Fprintln(os.Stderr, "Both sides must see the same code for the connection to be authentic.")
		osExit(1)
	}

	target := fs.Arg(0)

	// Load config.
	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		fatal("Config error: %v\nRun 'shurli init' first.", err)
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
	pw, _ := resolvePassword(filepath.Dir(cfgFile))
	ourPeerID, err := identity.PeerIDFromKeyFile(cfg.Identity.KeyFile, pw)
	if err != nil {
		fatal("Failed to load identity: %v", err)
	}

	// Compute fingerprint.
	emoji, numeric := sdk.ComputeFingerprint(ourPeerID, targetPeerID)
	prefix := sdk.FingerprintPrefix(ourPeerID, targetPeerID)

	// Display.
	fmt.Println()
	termcolor.Wblue(os.Stdout, "=== Peer Verification ===")
	fmt.Println()
	fmt.Println()
	termcolor.Wblue(os.Stdout, "Peer:    ")
	if displayName != "" {
		fmt.Printf("%s (%s...)\n", displayName, targetPeerID.String()[:16])
	} else {
		fmt.Printf("%s...\n", targetPeerID.String()[:16])
	}
	termcolor.Wblue(os.Stdout, "Your ID: ")
	fmt.Printf("%s...\n", ourPeerID.String()[:16])
	fmt.Println()
	termcolor.Wblue(os.Stdout, "Verification code:  ")
	termcolor.Wgreen(os.Stdout, "%s", emoji)
	fmt.Println()
	termcolor.Wblue(os.Stdout, "Numeric code:       ")
	termcolor.Wgreen(os.Stdout, "%s", numeric)
	fmt.Println()
	fmt.Println()

	if displayName != "" {
		fmt.Printf("Compare this with %s over a secure channel\n", displayName)
	} else {
		fmt.Println("Compare this with the other peer over a secure channel")
	}
	termcolor.Faint("(phone call, in person, trusted messaging).")
	fmt.Println()
	fmt.Println()

	// Prompt for confirmation.
	fmt.Print("Does the other side see the same code? [y/N]: ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))

	if answer != "y" && answer != "yes" {
		fmt.Println()
		termcolor.Yellow("Verification cancelled. Peer remains unverified.")
		return
	}

	// Write verified attribute.
	if err := auth.SetPeerAttr(cfg.Security.AuthorizedKeysFile, targetPeerID.String(), "verified", prefix); err != nil {
		fatal("Failed to mark peer as verified: %v", err)
	}

	fmt.Println()
	if displayName != "" {
		termcolor.Green("Peer \"%s\" verified!", displayName)
	} else {
		termcolor.Green("Peer verified!")
	}
}
