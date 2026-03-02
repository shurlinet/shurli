package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/identity"
	"github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/internal/vault"
)

// runRelayRecover recovers a relay's identity and vault from a BIP39 seed phrase.
// One command, one seed, recovers everything. Assumes the server blew up and
// nothing remains except the seed phrase.
//
// Usage: shurli relay recover
func runRelayRecover(args []string, configFile string) {
	fs := flag.NewFlagSet("relay recover", flag.ExitOnError)
	fs.Parse(args)

	// Read seed phrase with hidden input (no echo, no ps aux exposure).
	mnemonic, err := readSeedPhrase(os.Stdout)
	if err != nil {
		fatal("Failed to read seed phrase: %v", err)
	}

	// Validate seed.
	if err := identity.ValidateMnemonic(mnemonic); err != nil {
		fatal("Invalid seed phrase: %v", err)
	}

	seedBytes, err := identity.SeedFromMnemonic(mnemonic)
	if err != nil {
		fatal("Failed to decode seed: %v", err)
	}
	defer zeroBytes(seedBytes)

	// Resolve relay directory from config file location.
	relayDir := filepath.Dir(configFile)

	// Identity password (interactive).
	fmt.Println("Set a password to protect the relay identity:")
	password, err := readPasswordConfirm("Password: ", "Confirm: ", os.Stdout)
	if err != nil {
		fatal("Password error: %v", err)
	}

	// Derive identity key from seed.
	privKey, err := identity.DeriveIdentityKey(seedBytes)
	if err != nil {
		fatal("Failed to derive identity key: %v", err)
	}

	peerID, err := peer.IDFromPrivateKey(privKey)
	if err != nil {
		fatal("Failed to derive peer ID: %v", err)
	}

	// Resolve key file path from config if possible.
	keyFile := filepath.Join(relayDir, "relay_node.key")
	if cfg, loadErr := config.LoadRelayServerConfig(configFile); loadErr == nil {
		keyFile = cfg.Identity.KeyFile
		if !filepath.IsAbs(keyFile) {
			keyFile = filepath.Join(relayDir, keyFile)
		}
	}

	// Save encrypted identity key.
	if err := identity.SaveIdentity(keyFile, privKey, password); err != nil {
		fatal("Failed to save identity key: %v", err)
	}

	fmt.Println()
	termcolor.Green("Relay identity recovered!")
	fmt.Printf("Peer ID:  %s\n", peerID)
	fmt.Printf("Key file: %s\n", keyFile)

	// Recover vault from the same seed (same password by default).
	vaultPath := filepath.Join(relayDir, "vault.json")
	if cfg, loadErr := config.LoadRelayServerConfig(configFile); loadErr == nil && cfg.Security.VaultFile != "" {
		vaultPath = cfg.Security.VaultFile
		if !filepath.IsAbs(vaultPath) {
			vaultPath = filepath.Join(relayDir, vaultPath)
		}
	}

	v, err := vault.RecoverFromSeed(mnemonic, password, false, 30)
	if err != nil {
		fatal("Failed to recover vault: %v", err)
	}

	if err := v.Save(vaultPath); err != nil {
		fatal("Failed to save vault: %v", err)
	}
	v.Seal()

	termcolor.Green("Vault recovered!")
	fmt.Printf("Vault:    %s\n", vaultPath)
	fmt.Println()
	fmt.Println("Next: start relay with 'shurli relay serve'")
}
