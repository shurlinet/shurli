package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/identity"
	"github.com/shurlinet/shurli/internal/qr"
	tc "github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/internal/validate"
)

// promptRelayAddress runs the interactive relay address prompt and returns
// a validated full multiaddr string.
func promptRelayAddress(reader *bufio.Reader, stdout io.Writer) (string, error) {
	fmt.Fprintln(stdout, "Enter relay server address")
	fmt.Fprintln(stdout, "  Full multiaddr:  /ip4/<IP>/tcp/<PORT>/p2p/<PEER_ID>")
	fmt.Fprintln(stdout, "  Or just:         <IP>:<PORT>  or  <IP>  (default port: 7777)")
	fmt.Fprint(stdout, "> ")
	relayInput, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read input: %w", err)
	}
	relayInput = strings.TrimSpace(relayInput)
	if relayInput == "" {
		return "", fmt.Errorf("relay address is required")
	}

	if isFullMultiaddr(relayInput) {
		if _, err := ma.NewMultiaddr(relayInput); err != nil {
			return "", fmt.Errorf("invalid multiaddr: %w", err)
		}
		return relayInput, nil
	}

	ip, port, err := parseRelayHostPort(relayInput)
	if err != nil {
		return "", fmt.Errorf("invalid relay address: %w", err)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Enter the relay server's Peer ID")
	fmt.Fprintln(stdout, "  (shown in the relay server's setup output)")
	fmt.Fprint(stdout, "> ")
	peerIDStr, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read input: %w", err)
	}
	peerIDStr = strings.TrimSpace(peerIDStr)
	if peerIDStr == "" {
		return "", fmt.Errorf("relay Peer ID is required")
	}
	if err := validatePeerID(peerIDStr); err != nil {
		return "", fmt.Errorf("invalid Peer ID: %w", err)
	}
	addr := buildRelayMultiaddr(ip, port, peerIDStr)
	fmt.Fprintf(stdout, "Relay: %s\n", addr)
	return addr, nil
}

func runInit(args []string) {
	if err := doInit(args, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doInit(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dirFlag := fs.String("dir", "", "config directory (default: ~/.config/shurli)")
	networkFlag := fs.String("network", "", "DHT network namespace for private networks (e.g., \"my-crew\")")
	skipSeedConfirm := fs.Bool("skip-seed-confirm", false, "skip seed backup confirmation quiz (automation only)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Validate network namespace if provided
	if *networkFlag != "" {
		if err := validate.NetworkName(*networkFlag); err != nil {
			return fmt.Errorf("invalid --network value: %w", err)
		}
	}

	tc.Wgreen(stdout, "Welcome to Shurli!\n")
	fmt.Fprintln(stdout)

	// Determine config directory
	configDir := *dirFlag
	if configDir == "" {
		d, err := config.DefaultConfigDir()
		if err != nil {
			return fmt.Errorf("cannot determine config directory: %w", err)
		}
		configDir = d
	}

	// Check if config already exists
	configFile := filepath.Join(configDir, "config.yaml")
	if _, err := os.Stat(configFile); err == nil {
		return fmt.Errorf("config already exists: %s\nDelete it first if you want to reinitialize", configFile)
	}

	// Create config directory
	fmt.Fprintf(stdout, "Creating config directory: %s\n", configDir)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	fmt.Fprintln(stdout)

	// Network setup: public Shurli network (default) or custom relay
	reader := bufio.NewReader(stdin)
	fmt.Fprintln(stdout, "Network setup:")
	fmt.Fprintln(stdout, "  1. Join the Shurli public network (default)")
	fmt.Fprintln(stdout, "     Uses public seed nodes for peer discovery and direct connections.")
	fmt.Fprintln(stdout, "     NOTE: Seed nodes enable discovery only, NOT data relay.")
	fmt.Fprintln(stdout, "     Data transfers happen directly between your devices.")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "  2. Use my own relay server")
	fmt.Fprintln(stdout, "     Use a self-hosted relay for full data relay capability.")
	fmt.Fprintln(stdout)
	fmt.Fprint(stdout, "Choice [1]: ")

	choice, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}
	choice = strings.TrimSpace(choice)
	if choice == "" {
		choice = "1"
	}

	var relayAddrs []string
	switch choice {
	case "1":
		relayAddrs = HardcodedSeeds
		fmt.Fprintln(stdout)
		fmt.Fprintf(stdout, "Using %d public Shurli seed nodes for discovery.\n", len(SeedPeerIDs()))
		fmt.Fprintln(stdout, "These enable peer discovery and direct connections only.")
		fmt.Fprintln(stdout, "For full data relay, deploy your own: https://shurli.io/docs/relay-setup/")
	case "2":
		relayAddr, err := promptRelayAddress(reader, stdout)
		if err != nil {
			return err
		}
		relayAddrs = []string{relayAddr}
	default:
		return fmt.Errorf("invalid choice: %s (enter 1 or 2)", choice)
	}
	fmt.Fprintln(stdout)

	// Generate BIP39 seed
	fmt.Fprintln(stdout, "Generating identity...")
	fmt.Fprintln(stdout)

	mnemonic, entropy, err := identity.GenerateSeed()
	if err != nil {
		return fmt.Errorf("failed to generate seed: %w", err)
	}
	words := strings.Fields(mnemonic)

	tc.Wyellow(stdout, "=== SEED PHRASE ===\n")
	tc.Wyellow(stdout, "Write this down and store it securely. This is the ONLY way to\n")
	tc.Wyellow(stdout, "recover your identity if you lose this device.\n")
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "  %s\n", mnemonic)
	fmt.Fprintln(stdout)
	tc.Wyellow(stdout, "===========================\n")
	fmt.Fprintln(stdout)

	// Seed backup confirmation quiz.
	if err := confirmSeedBackup(stdout, reader, words, *skipSeedConfirm); err != nil {
		return fmt.Errorf("seed backup: %w", err)
	}
	if !*skipSeedConfirm {
		fmt.Fprintln(stdout, "Seed backup confirmed.")
		fmt.Fprintln(stdout)
	}

	// Derive identity key from seed.
	privKey, err := identity.DeriveIdentityKey(entropy)
	if err != nil {
		return fmt.Errorf("failed to derive identity key: %w", err)
	}

	// Set identity password (interactive).
	fmt.Fprintln(stdout, "Set a password to protect your identity:")
	password, pwErr := readPasswordConfirm("Password: ", "Confirm: ", stdout)
	if pwErr != nil {
		return pwErr
	}
	fmt.Fprintln(stdout)

	// Save encrypted identity.key.
	keyFile := filepath.Join(configDir, "identity.key")
	if err := identity.SaveIdentity(keyFile, privKey, password); err != nil {
		return fmt.Errorf("failed to save identity: %w", err)
	}

	// Create session token for auto-start.
	if err := identity.CreateSession(configDir, password); err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}

	peerID, err := peer.IDFromPrivateKey(privKey)
	if err != nil {
		return fmt.Errorf("failed to derive peer ID: %w", err)
	}
	tc.Wblue(stdout, "Your Peer ID: ")
	fmt.Fprintf(stdout, "%s\n", peerID)
	tc.Wfaint(stdout, "(Share this with peers who need to authorize you)\n")
	fmt.Fprintln(stdout)

	// Create authorized_keys file
	authKeysFile := filepath.Join(configDir, "authorized_keys")
	if _, err := os.Stat(authKeysFile); os.IsNotExist(err) {
		authContent := "# authorized_keys - Add peer IDs here (one per line)\n# Format: <peer_id> # optional comment\n"
		if err := os.WriteFile(authKeysFile, []byte(authContent), 0600); err != nil {
			return fmt.Errorf("failed to create authorized_keys: %w", err)
		}
	}

	// Write config file
	configContent := nodeConfigTemplate(relayAddrs, "shurli init", *networkFlag)

	if err := os.WriteFile(configFile, []byte(configContent), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	tc.Wgreen(stdout, "Config written to:  %s\n", configFile)
	tc.Wgreen(stdout, "Identity saved to:  %s\n", keyFile)
	fmt.Fprintln(stdout)

	// Show peer ID as QR for easy sharing
	fmt.Fprintln(stdout, "Your Peer ID (scan to share):")
	fmt.Fprintln(stdout)
	if q, err := qr.New(peerID.String(), qr.Medium); err == nil {
		fmt.Fprint(stdout, q.ToSmallString(false))
	}

	// Install shell completions and man page.
	setupShellEnvironment(stdout)

	tc.Wblue(stdout, "Next steps:\n")
	fmt.Fprintln(stdout, "  1. Run as server:  shurli daemon")
	fmt.Fprintln(stdout, "  2. Invite a peer:  shurli invite --name home")
	fmt.Fprintln(stdout, "  3. Or connect:     shurli proxy <target> <service> <port>")
	fmt.Fprintln(stdout)
	tc.Wfaint(stdout, "If anything looks wrong later, run: shurli doctor\n")
	return nil
}
