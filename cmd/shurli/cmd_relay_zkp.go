package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"

	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/identity"
	"github.com/shurlinet/shurli/internal/relay"
	"github.com/shurlinet/shurli/internal/zkp"
)

// runRelayZKPSetup derives PLONK proving and verifying keys from the unified
// BIP39 seed phrase (same seed as identity and vault). One-time operation:
// the seed derives a deterministic SRS, which produces keys cached to disk.
// The seed phrase is never stored on disk.
//
// Usage: shurli relay zkp-setup [--keys-dir <path>] [--force]
// Prompts for seed phrase with hidden input (no echo).
//        shurli relay zkp-setup [--keys-dir <path>]  (interactive prompt)
func runRelayZKPSetup(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay zkp-setup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	keysDir := fs.String("keys-dir", "", "output directory for proving/verifying keys (default: ~/.shurli/zkp/)")
	force := fs.Bool("force", false, "overwrite existing keys")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("usage: shurli relay zkp-setup [--keys-dir <path>] [--force]")
	}

	// Resolve keys directory.
	resolvedKeysDir := *keysDir
	if resolvedKeysDir == "" {
		home, _ := os.UserHomeDir()
		resolvedKeysDir = fmt.Sprintf("%s/.shurli/zkp", home)
	}

	// Check for existing keys.
	if zkp.KeysExist(resolvedKeysDir) && !*force {
		return fmt.Errorf("keys already exist at %s (use --force to overwrite)", resolvedKeysDir)
	}
	if *force && zkp.KeysExist(resolvedKeysDir) {
		os.Remove(filepath.Join(resolvedKeysDir, "provingKey.bin"))
		os.Remove(filepath.Join(resolvedKeysDir, "verifyingKey.bin"))
		fmt.Fprintln(stdout, "Removed existing keys.")
	}

	// Read seed phrase with hidden input (no echo, no ps aux exposure).
	mnemonic, err := readSeedPhrase(stdout)
	if err != nil {
		return err
	}

	if err := identity.ValidateMnemonic(mnemonic); err != nil {
		return fmt.Errorf("invalid seed phrase: %w", err)
	}

	fmt.Fprintln(stdout, "Deriving PLONK keys from seed phrase...")
	fmt.Fprintln(stdout, "(Same seed as identity and vault - one backup covers everything)")
	start := time.Now()
	if err := zkp.SetupKeysFromSeed(resolvedKeysDir, mnemonic); err != nil {
		return fmt.Errorf("key setup: %w", err)
	}
	fmt.Fprintf(stdout, "  Keys saved to %s (%s)\n", resolvedKeysDir, time.Since(start).Round(time.Millisecond))
	fmt.Fprintln(stdout, "Done. Relay and clients using this seed will produce compatible proofs.")
	return nil
}

func runRelayZKPTest(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay zkp-test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	authKeysPath := fs.String("auth-keys", "", "path to relay's authorized_keys (required)")
	keysDir := fs.String("keys-dir", "", "ZKP keys directory (default: ~/.shurli/zkp/)")
	relayAddr := fs.String("relay", "", "relay multiaddr (default: from config)")
	roleRequired := fs.Uint64("role", 0, "role to prove: 0=any, 1=admin, 2=member")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("usage: shurli relay zkp-test --auth-keys <path> [--relay <multiaddr>] [--role 0|1|2]")
	}

	if *authKeysPath == "" {
		return fmt.Errorf("--auth-keys is required (path to relay's authorized_keys file)")
	}

	// Resolve default keys dir (same default as SetupKeys/NewProver).
	resolvedKeysDir := *keysDir
	if resolvedKeysDir == "" {
		home, _ := os.UserHomeDir()
		resolvedKeysDir = fmt.Sprintf("%s/.shurli/zkp", home)
	}

	// Prompt for seed phrase with hidden input (optional for zkp-test).
	fmt.Fprintln(stdout, "Enter seed phrase for deterministic SRS (or press Enter to use random SRS):")
	mnemonic, _ := readSeedPhrase(stdout)

	// Step 1: Bootstrap PLONK keys.
	fmt.Fprintln(stdout, "Bootstrapping PLONK circuit keys...")
	setupStart := time.Now()
	if mnemonic != "" {
		fmt.Fprintln(stdout, "  Using deterministic SRS from seed phrase")
		if err := zkp.SetupKeysFromSeed(resolvedKeysDir, mnemonic); err != nil {
			return fmt.Errorf("setting up keys from seed: %w", err)
		}
	} else {
		if err := zkp.SetupKeys(resolvedKeysDir); err != nil {
			return fmt.Errorf("setting up keys: %w", err)
		}
	}
	fmt.Fprintf(stdout, "  Keys ready (%s)\n", time.Since(setupStart).Round(time.Millisecond))

	// Step 2: Build Merkle tree from authorized_keys.
	fmt.Fprintln(stdout, "Building Merkle tree...")
	tree, err := zkp.BuildMerkleTree(*authKeysPath)
	if err != nil {
		return fmt.Errorf("building tree: %w", err)
	}
	fmt.Fprintf(stdout, "  %d leaves, depth %d, root %x\n", tree.LeafCount(), tree.Depth, tree.Root[:8])

	// Step 3: Create prover.
	fmt.Fprintln(stdout, "Loading prover...")
	proverStart := time.Now()
	prover, err := zkp.NewProver(resolvedKeysDir)
	if err != nil {
		return fmt.Errorf("creating prover: %w", err)
	}
	fmt.Fprintf(stdout, "  Prover ready (%s)\n", time.Since(proverStart).Round(time.Millisecond))

	// Step 4: Load identity and create host.
	cfgFile, err := config.FindConfigFile("")
	if err != nil {
		return fmt.Errorf("finding config: %w", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	keyFile := cfg.Identity.KeyFile
	if !filepath.IsAbs(keyFile) {
		keyFile = filepath.Join(filepath.Dir(cfgFile), keyFile)
	}
	pw, _ := resolvePassword(filepath.Dir(cfgFile))
	priv, err := identity.LoadOrCreateIdentity(keyFile, pw)
	if err != nil {
		return fmt.Errorf("loading identity: %w", err)
	}

	h, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
	)
	if err != nil {
		return fmt.Errorf("creating host: %w", err)
	}
	defer h.Close()

	localPeerID := h.ID()
	fmt.Fprintf(stdout, "Local peer: %s\n", localPeerID.String()[:16]+"...")

	// Step 5: Connect to relay.
	var relayPeerID peer.ID
	if *relayAddr != "" {
		ai, err := peer.AddrInfoFromString(*relayAddr)
		if err != nil {
			return fmt.Errorf("parsing relay addr: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := h.Connect(ctx, *ai); err != nil {
			cancel()
			return fmt.Errorf("connecting to relay: %w", err)
		}
		cancel()
		relayPeerID = ai.ID
	} else {
		// Use first relay from config.
		if len(cfg.Relay.Addresses) == 0 {
			return fmt.Errorf("no relay configured and --relay not specified")
		}
		ai, err := peer.AddrInfoFromString(cfg.Relay.Addresses[0])
		if err != nil {
			return fmt.Errorf("parsing configured relay addr: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := h.Connect(ctx, *ai); err != nil {
			cancel()
			return fmt.Errorf("connecting to relay: %w", err)
		}
		cancel()
		relayPeerID = ai.ID
	}
	fmt.Fprintf(stdout, "Connected to relay %s\n", relayPeerID.String()[:16]+"...")

	// Step 6: Authenticate via ZKP.
	client := &relay.ZKPAuthClient{
		Host:   h,
		Prover: prover,
		Tree:   tree,
	}

	roleName := "any"
	if *roleRequired == 1 {
		roleName = "admin"
	} else if *roleRequired == 2 {
		roleName = "member"
	}
	fmt.Fprintf(stdout, "Authenticating (role=%s)...\n", roleName)

	authStart := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = client.Authenticate(ctx, relayPeerID, *roleRequired)
	authDur := time.Since(authStart)

	if err != nil {
		fmt.Fprintf(stdout, "  FAILED: %v (%s)\n", err, authDur.Round(time.Millisecond))
		return err
	}

	fmt.Fprintf(stdout, "  AUTHORIZED (%s)\n", authDur.Round(time.Millisecond))
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "ZKP anonymous auth successful.")
	return nil
}
