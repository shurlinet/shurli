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

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/relay"
	"github.com/shurlinet/shurli/internal/termcolor"
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

func doRelayVaultInit(args []string, configFile string, stdout io.Writer, stdin io.Reader) error {
	fs := flag.NewFlagSet("relay vault init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	enableTOTP := fs.Bool("totp", false, "enable TOTP 2FA for unseal")
	autoSealMins := fs.Int("auto-seal", 30, "auto-seal timeout in minutes (0 = manual only)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Connect to running relay
	client, err := relayAdminClient(configFile)
	if err != nil {
		return err
	}

	// Check if already initialized
	status, err := client.SealStatus()
	if err != nil {
		return fmt.Errorf("failed to get seal status: %w", err)
	}
	if status.Initialized {
		return fmt.Errorf("vault is already initialized")
	}

	// Read passphrase (with confirmation)
	passphrase, err := readPassphraseConfirm(stdout, stdin)
	if err != nil {
		return err
	}

	resp, err := client.InitVault(passphrase, *enableTOTP, *autoSealMins)
	if err != nil {
		return fmt.Errorf("vault init failed: %w", err)
	}

	fmt.Fprintln(stdout)
	termcolor.Green("Vault initialized successfully!")
	fmt.Fprintln(stdout)

	// Display seed phrase prominently
	fmt.Fprintln(stdout, "=== RECOVERY SEED PHRASE ===")
	fmt.Fprintln(stdout, "Write this down and store it securely. It is the ONLY way to")
	fmt.Fprintln(stdout, "recover your vault if you forget the passphrase.")
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "  %s\n", resp.SeedPhrase)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "============================")

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
	if err := doRelaySeal(configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelaySeal(configFile string, stdout io.Writer) error {
	client, err := relayAdminClient(configFile)
	if err != nil {
		return err
	}

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

	// Read passphrase
	passphrase, err := readPassphrase(stdout, "Passphrase: ")
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

// doRemoteUnseal unseals a relay vault over P2P using the /shurli/relay-unseal/1.0.0 protocol.
func doRemoteUnseal(relayAddr string, promptTOTP bool, _ string, stdout io.Writer, stdin io.Reader) error {
	// Read passphrase
	passphrase, err := readPassphrase(stdout, "Passphrase: ")
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

	// Build request payload
	reqData := relay.EncodeUnsealRequest(passphrase, totpCode)

	// Load config (needed for identity, relay addresses, and name resolution)
	_, cfg := resolveConfigFile("")

	// Resolve the relay address: accepts a full multiaddr, a name, or a raw peer ID.
	resolvedAddr, err := resolveRelayAddr(relayAddr, cfg)
	if err != nil {
		return err
	}

	// Use standalone host to connect to relay and open unseal stream
	fmt.Fprintf(stdout, "Connecting to relay: %s\n", truncateAddr(resolvedAddr))

	p2pNetwork, err := p2pnet.New(&p2pnet.Config{
		KeyFile:            cfg.Identity.KeyFile,
		Config:             &config.Config{Network: cfg.Network},
		UserAgent:          "shurli/" + version,
		EnableRelay:        true,
		RelayAddrs:         cfg.Relay.Addresses,
		ForcePrivate:       cfg.Network.ForcePrivateReachability,
		EnableNATPortMap:   true,
		EnableHolePunching: true,
	})
	if err != nil {
		return fmt.Errorf("failed to create P2P host: %w", err)
	}
	defer p2pNetwork.Close()

	// Parse the resolved multiaddr to get peer info
	peerInfo, err := peer.AddrInfoFromString(resolvedAddr)
	if err != nil {
		return fmt.Errorf("invalid relay address: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := p2pNetwork.Host().Connect(ctx, *peerInfo); err != nil {
		return fmt.Errorf("failed to connect to relay: %w", err)
	}

	stream, err := p2pNetwork.Host().NewStream(ctx, peerInfo.ID, protocol.ID(relay.UnsealProtocol))
	if err != nil {
		return fmt.Errorf("failed to open unseal stream: %w", err)
	}
	defer stream.Close()

	if _, err := stream.Write(reqData); err != nil {
		return fmt.Errorf("failed to send unseal request: %w", err)
	}
	stream.CloseWrite()

	ok, msg, err := relay.ReadUnsealResponse(stream)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if ok {
		termcolor.Green("Vault unsealed remotely.")
		if msg != "" {
			fmt.Fprintf(stdout, "  %s\n", msg)
		}
	} else {
		return fmt.Errorf("remote unseal failed: %s", msg)
	}

	return nil
}

func runRelaySealStatus(args []string, configFile string) {
	if err := doRelaySealStatus(configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelaySealStatus(configFile string, stdout io.Writer) error {
	client, err := relayAdminClient(configFile)
	if err != nil {
		return err
	}

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

// readPassphrase reads a passphrase from the terminal without echo.
func readPassphrase(w io.Writer, prompt string) (string, error) {
	fmt.Fprint(w, prompt)
	passBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(w) // newline after hidden input
	if err != nil {
		return "", fmt.Errorf("failed to read passphrase: %w", err)
	}
	return string(passBytes), nil
}

// readPassphraseConfirm reads and confirms a passphrase.
func readPassphraseConfirm(w io.Writer, _ io.Reader) (string, error) {
	pass1, err := readPassphrase(w, "Enter passphrase: ")
	if err != nil {
		return "", err
	}
	if len(pass1) < 8 {
		return "", fmt.Errorf("passphrase must be at least 8 characters")
	}
	pass2, err := readPassphrase(w, "Confirm passphrase: ")
	if err != nil {
		return "", err
	}
	if pass1 != pass2 {
		return "", fmt.Errorf("passphrases do not match")
	}
	return pass1, nil
}

func printRelayVaultUsage() {
	fmt.Println("Usage: shurli relay vault <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  init     [--totp] [--auto-seal N]   Initialize a new vault")
	fmt.Println("  seal                                 Seal the vault (watch-only mode)")
	fmt.Println("  unseal   [--remote <multiaddr>]       Unseal the vault (local or remote P2P)")
	fmt.Println("  status                               Show vault seal status")
	fmt.Println()
	fmt.Println("The vault protects the relay's root key material. When sealed,")
	fmt.Println("the relay routes traffic but cannot authorize new peers.")
}
