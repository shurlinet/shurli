package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/identity"
	"github.com/shurlinet/shurli/internal/relay"
	"github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/internal/vault"
	"github.com/shurlinet/shurli/pkg/p2pnet"
	"golang.org/x/term"
)

func runRelayVault(args []string, configFile string) {
	if len(args) < 1 {
		printRelayVaultUsage()
		osExit(1)
	}
	switch args[0] {
	case "init":
		runRelayVaultInit(args[1:], configFile)
	case "seal":
		runRelaySeal(args[1:], configFile)
	case "unseal":
		runRelayUnseal(args[1:], configFile)
	case "status":
		runRelaySealStatus(args[1:], configFile)
	case "change-password":
		runRelayVaultChangePassword(args[1:], configFile)
	default:
		fmt.Fprintf(os.Stderr, "Unknown vault command: %s\n\n", args[0])
		printRelayVaultUsage()
		osExit(1)
	}
}

func runRelayVaultInit(args []string, configFile string) {
	if err := doRelayVaultInit(args, configFile, os.Stdout, os.Stdin); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayVaultInit(args []string, configFile string, stdout io.Writer, _ io.Reader) error {
	fs := flag.NewFlagSet("relay vault init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	enableTOTP := fs.Bool("totp", false, "enable TOTP 2FA for unseal")
	autoSealMins := fs.Int("auto-seal", 30, "auto-seal timeout in minutes (0 = manual only)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Vault init is local-only. Seed material never leaves this machine.
	client, err := relayAdminClient(configFile)
	if err != nil {
		return err
	}

	// Check if already initialized.
	status, err := client.SealStatus()
	if err != nil {
		return fmt.Errorf("failed to get seal status: %w", err)
	}
	if status.Initialized {
		return fmt.Errorf("vault is already initialized")
	}

	// Read seed phrase interactively with hidden input.
	mnemonic, err := readSeedPhrase(stdout)
	if err != nil {
		return err
	}

	// Validate the mnemonic.
	if err := identity.ValidateMnemonic(mnemonic); err != nil {
		return fmt.Errorf("invalid seed phrase: %w", err)
	}

	// Convert mnemonic to seed bytes.
	seedBytes, err := identity.SeedFromMnemonic(mnemonic)
	if err != nil {
		return fmt.Errorf("failed to decode seed: %w", err)
	}
	defer zeroBytes(seedBytes)

	// Read vault password (with confirmation).
	password, err := readVaultPasswordConfirm(stdout)
	if err != nil {
		return err
	}

	resp, err := client.InitVault(seedBytes, mnemonic, password, *enableTOTP, *autoSealMins)
	if err != nil {
		return fmt.Errorf("vault init failed: %w", err)
	}

	fmt.Fprintln(stdout)
	termcolor.Green("Vault initialized successfully!")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "The vault root key was derived from your seed phrase.")
	fmt.Fprintln(stdout, "Your seed phrase backup covers both identity and vault recovery.")

	if resp.TOTPUri != "" {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "TOTP provisioning URI (scan with authenticator app):")
		fmt.Fprintf(stdout, "  %s\n", resp.TOTPUri)
	}

	if *autoSealMins > 0 {
		fmt.Fprintf(stdout, "\nAuto-seal: %d minutes after unseal\n", *autoSealMins)
	} else {
		fmt.Fprintln(stdout, "\nAuto-seal: disabled (manual seal only)")
	}

	return nil
}

func runRelaySeal(args []string, configFile string) {
	if err := doRelaySeal(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelaySeal(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay seal", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := client.Seal(); err != nil {
		return fmt.Errorf("seal failed: %w", err)
	}

	termcolor.Green("Vault sealed.")
	fmt.Fprintln(stdout, "Relay is now in watch-only mode.")
	return nil
}

func runRelayUnseal(args []string, configFile string) {
	if err := doRelayUnseal(args, configFile, os.Stdout, os.Stdin); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayUnseal(args []string, configFile string, stdout io.Writer, stdin io.Reader) error {
	fs := flag.NewFlagSet("relay unseal", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteAddr := fs.String("remote", "", "relay multiaddr for remote P2P unseal")
	totpFlag := fs.Bool("totp", false, "prompt for TOTP code")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Remote P2P unseal mode
	if *remoteAddr != "" {
		return doRemoteUnseal(*remoteAddr, *totpFlag, configFile, stdout, stdin)
	}

	// Local Unix socket unseal mode
	client, err := relayAdminClient(configFile)
	if err != nil {
		return err
	}

	// Check current status first
	status, err := client.SealStatus()
	if err != nil {
		return fmt.Errorf("failed to get seal status: %w", err)
	}
	if !status.Initialized {
		return fmt.Errorf("vault not initialized: run 'shurli relay vault init' first")
	}
	if !status.Sealed {
		fmt.Fprintln(stdout, "Vault is already unsealed.")
		return nil
	}

	// Read vault password.
	passphrase, err := readVaultPassword(stdout, "Vault password: ")
	if err != nil {
		return err
	}

	var totpCode string
	if status.TOTPEnabled {
		fmt.Fprint(stdout, "TOTP code: ")
		reader := bufio.NewReader(stdin)
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read TOTP code: %w", err)
		}
		totpCode = strings.TrimSpace(line)
	}

	if err := client.Unseal(passphrase, totpCode); err != nil {
		return fmt.Errorf("unseal failed: %w", err)
	}

	termcolor.Green("Vault unsealed.")
	if status.AutoSealMins > 0 {
		fmt.Fprintf(stdout, "Auto-seal in %d minutes.\n", status.AutoSealMins)
	}
	return nil
}

// resolveRelayAddr resolves a relay address from a name, peer ID, or full multiaddr.
// Accepts:
//   - Full multiaddr: /ip4/203.0.113.50/tcp/4001/p2p/12D3KooW... (used directly)
//   - Short name: my-relay (resolved via config names, then matched to relay address)
//   - Raw peer ID: 12D3KooW... (matched to relay address in config)
func resolveRelayAddr(input string, cfg *config.NodeConfig) (string, error) {
	// Full multiaddr: use directly
	if isFullMultiaddr(input) {
		return input, nil
	}

	// Resolve name or peer ID
	resolver := p2pnet.NewNameResolver()
	if cfg.Names != nil {
		if err := resolver.LoadFromMap(cfg.Names); err != nil {
			return "", fmt.Errorf("failed to load names: %w", err)
		}
	}

	peerID, err := resolver.Resolve(input)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %q: not a multiaddr, configured name, or valid peer ID", input)
	}

	// Find matching relay address from config
	peerIDStr := peerID.String()
	for _, addr := range cfg.Relay.Addresses {
		if strings.Contains(addr, "/p2p/"+peerIDStr) {
			return addr, nil
		}
	}

	return "", fmt.Errorf("resolved %q to peer %s but no matching relay address in config", input, peerIDStr)
}

// doRemoteUnseal unseals a relay vault over P2P using the dedicated
// /shurli/relay-unseal/1.0.0 protocol. This protocol has its own iOS-style
// escalating lockout and binary wire format, separate from the generic admin proxy.
func doRemoteUnseal(relayAddr string, promptTOTP bool, _ string, stdout io.Writer, stdin io.Reader) error {
	// Read vault password.
	password, err := readVaultPassword(stdout, "Vault password: ")
	if err != nil {
		return err
	}

	var totpCode string
	if promptTOTP {
		fmt.Fprint(stdout, "TOTP code: ")
		reader := bufio.NewReader(stdin)
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read TOTP code: %w", err)
		}
		totpCode = strings.TrimSpace(line)
	}

	conn, err := connectRemoteRelay(relayAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Open a stream on the dedicated unseal protocol (not the admin proxy).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := conn.network.Host().NewStream(ctx, conn.relayPeerID, protocol.ID(relay.UnsealProtocol))
	if err != nil {
		return fmt.Errorf("failed to open unseal stream: %w", err)
	}
	defer s.Close()

	s.SetDeadline(time.Now().Add(30 * time.Second))

	// Read the challenge nonce from the relay (replay protection).
	nonce, err := relay.ReadUnsealChallenge(s)
	if err != nil {
		return fmt.Errorf("failed to read unseal challenge: %w", err)
	}

	// Send the binary unseal request with nonce echo.
	if _, err := s.Write(relay.EncodeUnsealRequest(nonce, password, totpCode)); err != nil {
		return fmt.Errorf("failed to send unseal request: %w", err)
	}
	s.CloseWrite()

	// Read the response.
	ok, msg, err := relay.ReadUnsealResponse(s)
	if err != nil {
		return fmt.Errorf("failed to read unseal response: %w", err)
	}
	if !ok {
		return fmt.Errorf("remote unseal failed: %s", msg)
	}

	termcolor.Green("Vault unsealed remotely.")
	if msg != "" && msg != "unsealed" {
		fmt.Fprintln(stdout, msg)
	}
	return nil
}

func runRelaySealStatus(args []string, configFile string) {
	if err := doRelaySealStatus(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelaySealStatus(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay seal-status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
	if err != nil {
		return err
	}
	defer cleanup()

	status, err := client.SealStatus()
	if err != nil {
		return fmt.Errorf("failed to get seal status: %w", err)
	}

	if !status.Initialized {
		fmt.Fprintln(stdout, "Vault: not initialized")
		return nil
	}

	if status.Sealed {
		termcolor.Yellow("Vault: SEALED (watch-only mode)")
	} else {
		termcolor.Green("Vault: UNSEALED (full operations)")
	}

	fmt.Fprintf(stdout, "TOTP:      %v\n", status.TOTPEnabled)
	if status.AutoSealMins > 0 {
		fmt.Fprintf(stdout, "Auto-seal: %d minutes\n", status.AutoSealMins)
	} else {
		fmt.Fprintln(stdout, "Auto-seal: disabled")
	}
	return nil
}

// relayAdminClient creates an AdminClient for the relay admin socket.
func relayAdminClient(configFile string) (*relay.AdminClient, error) {
	dir := filepath.Dir(configFile)
	socketPath := filepath.Join(dir, ".relay-admin.sock")
	cookiePath := filepath.Join(dir, ".relay-admin.cookie")
	return relay.NewAdminClient(socketPath, cookiePath)
}

// readVaultPassword reads a vault password from the terminal without echo.
func readVaultPassword(w io.Writer, prompt string) (string, error) {
	fmt.Fprint(w, prompt)
	passBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(w) // newline after hidden input
	if err != nil {
		return "", fmt.Errorf("failed to read password: %w", err)
	}
	return string(passBytes), nil
}

// readVaultPasswordConfirm reads and confirms a vault password.
func readVaultPasswordConfirm(w io.Writer) (string, error) {
	pass1, err := readVaultPassword(w, "Enter vault password: ")
	if err != nil {
		return "", err
	}
	if len(pass1) < 8 {
		return "", fmt.Errorf("vault password must be at least 8 characters")
	}
	pass2, err := readVaultPassword(w, "Confirm vault password: ")
	if err != nil {
		return "", err
	}
	if pass1 != pass2 {
		return "", fmt.Errorf("passwords do not match")
	}
	return pass1, nil
}

// runRelayVaultChangePassword changes the vault password directly on disk.
func runRelayVaultChangePassword(args []string, configFile string) {
	fs := flag.NewFlagSet("relay vault change-password", flag.ExitOnError)
	totpFlag := fs.Bool("totp", false, "prompt for TOTP code")
	fs.Parse(args)

	// Load the relay server config to find vault path.
	cfg, err := config.LoadRelayServerConfig(configFile)
	if err != nil {
		fatal("Failed to load relay config: %v", err)
	}

	vaultPath := cfg.Security.VaultFile
	if vaultPath == "" {
		fatal("No vault file configured in relay config")
	}

	// Load vault from disk (sealed state).
	v, err := vault.Load(vaultPath)
	if err != nil {
		fatal("Failed to load vault: %v", err)
	}

	// Read current password and unseal.
	oldPassword, err := readVaultPassword(os.Stdout, "Current vault password: ")
	if err != nil {
		fatal("Failed to read password: %v", err)
	}

	var totpCode string
	if *totpFlag || v.TOTPEnabled() {
		fmt.Print("TOTP code: ")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		totpCode = strings.TrimSpace(line)
	}

	if err := v.Unseal(oldPassword, totpCode); err != nil {
		fatal("Failed to unseal vault: %v", err)
	}

	// Read new password.
	newPassword, err := readVaultPasswordConfirm(os.Stdout)
	if err != nil {
		fatal("Failed to read new password: %v", err)
	}

	// Change password and save.
	if err := v.ChangePassword(oldPassword, newPassword); err != nil {
		fatal("Failed to change password: %v", err)
	}

	// Save with new encryption.
	if err := v.Save(vaultPath); err != nil {
		fatal("Failed to save vault: %v", err)
	}

	v.Seal()
	termcolor.Green("Vault password changed successfully.")
}

func printRelayVaultUsage() {
	fmt.Println("Usage: shurli relay vault <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  init              [--totp] [--auto-seal N]                  Initialize vault from seed")
	fmt.Println("  seal                                                       Seal the vault (watch-only)")
	fmt.Println("  unseal            [--remote <multiaddr>]                    Unseal the vault")
	fmt.Println("  status                                                     Show vault seal status")
	fmt.Println("  change-password   [--totp]                                 Change vault password")
	fmt.Println()
	fmt.Println("The vault protects the relay's root key material. When sealed,")
	fmt.Println("the relay routes traffic but cannot authorize new peers.")
	fmt.Println("The vault root key is derived from your seed phrase (same seed as identity).")
}
