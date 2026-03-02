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

func runRecover(args []string) {
	fs := flag.NewFlagSet("recover", flag.ExitOnError)
	relayFlag := fs.Bool("relay", false, "also recover relay vault and ZKP keys")
	dirFlag := fs.String("dir", "", "config directory (default: auto-detect)")
	fs.Parse(args)

	// Resolve config directory.
	var configDir string
	if *dirFlag != "" {
		configDir = *dirFlag
	} else {
		cfgFile, err := config.FindConfigFile("")
		if err != nil {
			// No config yet - use default config dir.
			home, _ := os.UserHomeDir()
			configDir = filepath.Join(home, ".config", "shurli")
		} else {
			configDir = filepath.Dir(cfgFile)
		}
	}

	// Read seed phrase with hidden input (no echo, no ps aux exposure).
	mnemonic, err := readSeedPhrase(os.Stdout)
	if err != nil {
		fatal("Failed to read seed phrase: %v", err)
	}

	// Validate.
	if err := identity.ValidateMnemonic(mnemonic); err != nil {
		fatal("Invalid seed phrase: %v", err)
	}

	// Convert to seed bytes.
	seedBytes, err := identity.SeedFromMnemonic(mnemonic)
	if err != nil {
		fatal("Failed to decode seed: %v", err)
	}
	defer zeroBytes(seedBytes)

	// Derive identity key.
	privKey, err := identity.DeriveIdentityKey(seedBytes)
	if err != nil {
		fatal("Failed to derive identity key: %v", err)
	}

	peerID, err := peer.IDFromPrivateKey(privKey)
	if err != nil {
		fatal("Failed to derive peer ID: %v", err)
	}

	// Get new identity password (interactive).
	password, err := readPasswordConfirm(
		"New identity password: ",
		"Confirm identity password: ",
		os.Stdout,
	)
	if err != nil {
		fatal("Password error: %v", err)
	}

	// Ensure config directory exists.
	if err := os.MkdirAll(configDir, 0700); err != nil {
		fatal("Failed to create config directory: %v", err)
	}

	// Save encrypted identity.key.
	keyPath := filepath.Join(configDir, "identity.key")
	if err := identity.SaveIdentity(keyPath, privKey, password); err != nil {
		fatal("Failed to save identity key: %v", err)
	}

	fmt.Println()
	termcolor.Green("Identity recovered!")
	fmt.Printf("Peer ID: %s\n", peerID)
	fmt.Printf("Key file: %s\n", keyPath)

	// Create session token for auto-unlock.
	if err := identity.CreateSession(configDir, password); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to create session token: %v\n", err)
	} else {
		fmt.Println("Session token created (auto-unlock enabled).")
	}

	// Relay recovery.
	if *relayFlag {
		recoverRelay(configDir, seedBytes, mnemonic)
	}
}

// recoverRelay recovers the relay vault from the same seed.
func recoverRelay(configDir string, seedBytes []byte, mnemonic string) {
	fmt.Println()
	fmt.Println("--- Relay Recovery ---")

	// Prompt for vault password (can be different from identity password).
	vaultPassword, err := readPasswordConfirm(
		"New vault password: ",
		"Confirm vault password: ",
		os.Stdout,
	)
	if err != nil {
		fatal("Vault password error: %v", err)
	}

	// Find vault file path. Check relay server config if it exists.
	vaultPath := filepath.Join(configDir, "vault.json")
	relayConfigFile := filepath.Join(configDir, "relay-server.yaml")
	if _, err := os.Stat(relayConfigFile); err == nil {
		cfg, err := config.LoadRelayServerConfig(relayConfigFile)
		if err == nil && cfg.Security.VaultFile != "" {
			vaultPath = cfg.Security.VaultFile
		}
	}

	// Recover vault from seed.
	v, err := vault.RecoverFromSeed(mnemonic, vaultPassword, false, 30)
	if err != nil {
		fatal("Failed to recover vault: %v", err)
	}

	if err := v.Save(vaultPath); err != nil {
		fatal("Failed to save vault: %v", err)
	}

	v.Seal()

	termcolor.Green("Vault recovered!")
	fmt.Printf("Vault file: %s\n", vaultPath)
	fmt.Println("Vault is sealed. Use 'shurli relay vault unseal' to unseal.")
}
