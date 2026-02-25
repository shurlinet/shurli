package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	ma "github.com/multiformats/go-multiaddr"

	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/qr"
	"github.com/shurlinet/shurli/internal/validate"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

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
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Validate network namespace if provided
	if *networkFlag != "" {
		if err := validate.NetworkName(*networkFlag); err != nil {
			return fmt.Errorf("invalid --network value: %w", err)
		}
	}

	fmt.Fprintln(stdout, "Welcome to Shurli!")
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

	// Prompt for relay address
	reader := bufio.NewReader(stdin)
	fmt.Fprintln(stdout, "Enter relay server address")
	fmt.Fprintln(stdout, "  Full multiaddr:  /ip4/<IP>/tcp/<PORT>/p2p/<PEER_ID>")
	fmt.Fprintln(stdout, "  Or just:         <IP>:<PORT>  or  <IP>  (default port: 7777)")
	fmt.Fprint(stdout, "> ")
	relayInput, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}
	relayInput = strings.TrimSpace(relayInput)
	if relayInput == "" {
		return fmt.Errorf("relay address is required")
	}

	var relayAddr string
	if isFullMultiaddr(relayInput) {
		// Validate the multiaddr before embedding in config YAML.
		// A malformed string with quotes or newlines would corrupt the config.
		if _, err := ma.NewMultiaddr(relayInput); err != nil {
			return fmt.Errorf("invalid multiaddr: %w", err)
		}
		relayAddr = relayInput
	} else {
		ip, port, err := parseRelayHostPort(relayInput)
		if err != nil {
			return fmt.Errorf("invalid relay address: %w", err)
		}
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Enter the relay server's Peer ID")
		fmt.Fprintln(stdout, "  (shown in the relay server's setup output)")
		fmt.Fprint(stdout, "> ")
		peerIDStr, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read input: %w", err)
		}
		peerIDStr = strings.TrimSpace(peerIDStr)
		if peerIDStr == "" {
			return fmt.Errorf("relay Peer ID is required")
		}
		if err := validatePeerID(peerIDStr); err != nil {
			return fmt.Errorf("invalid Peer ID: %w", err)
		}
		relayAddr = buildRelayMultiaddr(ip, port, peerIDStr)
		fmt.Fprintf(stdout, "Relay: %s\n", relayAddr)
	}
	fmt.Fprintln(stdout)

	// Generate identity
	keyFile := filepath.Join(configDir, "identity.key")
	fmt.Fprintln(stdout, "Generating identity...")
	peerID, err := p2pnet.PeerIDFromKeyFile(keyFile)
	if err != nil {
		return fmt.Errorf("failed to generate identity: %w", err)
	}
	fmt.Fprintf(stdout, "Your Peer ID: %s\n", peerID)
	fmt.Fprintln(stdout, "(Share this with peers who need to authorize you)")
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
	configContent := nodeConfigTemplate(relayAddr, "shurli init", *networkFlag)

	if err := os.WriteFile(configFile, []byte(configContent), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	fmt.Fprintf(stdout, "Config written to:  %s\n", configFile)
	fmt.Fprintf(stdout, "Identity saved to:  %s\n", keyFile)
	fmt.Fprintln(stdout)

	// Show peer ID as QR for easy sharing
	fmt.Fprintln(stdout, "Your Peer ID (scan to share):")
	fmt.Fprintln(stdout)
	if q, err := qr.New(peerID.String(), qr.Medium); err == nil {
		fmt.Fprint(stdout, q.ToSmallString(false))
	}

	fmt.Fprintln(stdout, "Next steps:")
	fmt.Fprintln(stdout, "  1. Run as server:  shurli daemon")
	fmt.Fprintln(stdout, "  2. Invite a peer:  shurli invite --name home")
	fmt.Fprintln(stdout, "  3. Or connect:     shurli proxy <target> <service> <port>")
	return nil
}
