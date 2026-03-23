package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/libp2p/go-libp2p/core/crypto"
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
	dirFlag := fs.String("dir", "", "config directory (default: /etc/shurli)")
	userFlag := fs.Bool("user", false, "install config in ~/.config/shurli/ instead of /etc/shurli/")
	networkFlag := fs.String("network", "", "DHT network namespace for private networks (e.g., \"my-crew\")")
	skipSeedConfirm := fs.Bool("skip-seed-confirm", false, "skip seed backup confirmation quiz (automation only)")
	if err := fs.Parse(reorderFlags(fs, args)); err != nil {
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
		var d string
		var err error
		if *userFlag {
			d, err = config.UserConfigDir()
		} else {
			d, err = config.DefaultConfigDir()
		}
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
	fmt.Fprintf(stdout, "Config directory: %s\n", configDir)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		// Needs sudo for /etc/shurli/
		if !*userFlag && *dirFlag == "" {
			fmt.Fprintln(stdout, "Creating system config directory (requires sudo)...")
			user := os.Getenv("USER")
			if user == "" {
				user = "root"
			}
			if e := sudoRun("mkdir", "-p", configDir); e != nil {
				return fmt.Errorf("failed to create directory: %w", e)
			}
			if e := sudoRun("chown", "-R", user+":"+user, configDir); e != nil {
				return fmt.Errorf("failed to set ownership: %w", e)
			}
			if e := sudoRun("chmod", "700", configDir); e != nil {
				return fmt.Errorf("failed to set permissions: %w", e)
			}
		} else {
			return fmt.Errorf("failed to create directory: %w", err)
		}
	}
	fmt.Fprintln(stdout)

	// Identity setup: new or recover
	reader := bufio.NewReader(stdin)
	fmt.Fprintln(stdout, "Identity:")
	fmt.Fprintln(stdout, "  1. Create a new identity (default)")
	fmt.Fprintln(stdout, "  2. Recover from an existing seed phrase")
	fmt.Fprintln(stdout)
	fmt.Fprint(stdout, "Choice [1]: ")

	idChoice, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}
	idChoice = strings.TrimSpace(idChoice)
	if idChoice == "" {
		idChoice = "1"
	}

	var recoverMode bool
	switch idChoice {
	case "1":
		// New identity - handled below
	case "2":
		recoverMode = true
	default:
		return fmt.Errorf("invalid choice: %s (enter 1 or 2)", idChoice)
	}
	fmt.Fprintln(stdout)

	// Identity: generate new or recover from seed phrase.
	// This runs BEFORE network setup so the user confirms their identity first.
	var privKey crypto.PrivKey
	if recoverMode {
		fmt.Fprintln(stdout, "Enter your seed phrase to recover your identity.")
		fmt.Fprintln(stdout)
		mnemonic, err := readSeedPhrase(stdout)
		if err != nil {
			return fmt.Errorf("failed to read seed phrase: %w", err)
		}
		if err := identity.ValidateMnemonic(mnemonic); err != nil {
			return fmt.Errorf("invalid seed phrase: %w", err)
		}
		entropy, err := identity.SeedFromMnemonic(mnemonic)
		if err != nil {
			return fmt.Errorf("failed to decode seed: %w", err)
		}
		privKey, err = identity.DeriveIdentityKey(entropy)
		if err != nil {
			return fmt.Errorf("failed to derive identity key: %w", err)
		}
		peerID, _ := peer.IDFromPrivateKey(privKey)
		tc.Wgreen(stdout, "Seed phrase accepted.\n")
		fmt.Fprintf(stdout, "Recovered Peer ID: %s\n", peerID)
		fmt.Fprintln(stdout)
	} else {
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
		fmt.Fprint(stdout, formatSeedGrid(words))
		fmt.Fprintln(stdout)
		tc.Wyellow(stdout, "===========================\n")
		fmt.Fprintln(stdout)

		if err := confirmSeedBackup(stdout, reader, words, *skipSeedConfirm); err != nil {
			return fmt.Errorf("seed backup: %w", err)
		}
		if !*skipSeedConfirm {
			fmt.Fprintln(stdout, "Seed backup confirmed.")
			fmt.Fprintln(stdout)
		}

		privKey, err = identity.DeriveIdentityKey(entropy)
		if err != nil {
			return fmt.Errorf("failed to derive identity key: %w", err)
		}
	}

	// Network setup: own relay (recommended) or public seed nodes
	fmt.Fprintln(stdout, "Network setup:")
	fmt.Fprintln(stdout, "  1. Use my own relay server (recommended)")
	fmt.Fprintln(stdout, "     Full capability: data relay, file transfer, service proxy.")
	fmt.Fprintln(stdout, "     Your relay, your rules. Setup: https://shurli.io/docs/relay-setup/")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "  2. Use public seed nodes (limited - discovery only)")
	fmt.Fprintln(stdout, "     Seed nodes handle peer DISCOVERY only. They do NOT relay data.")
	fmt.Fprintln(stdout, "     You cannot: relay files, proxy services, or pass any data through seeds.")
	fmt.Fprintln(stdout, "     You can: discover peers and make direct connections (hole-punching).")
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
	var usedSeeds bool
	switch choice {
	case "1":
		relayAddr, err := promptRelayAddress(reader, stdout)
		if err != nil {
			return err
		}
		relayAddrs = []string{relayAddr}
	case "2":
		usedSeeds = true
		relayAddrs = HardcodedSeeds
		fmt.Fprintln(stdout)
		fmt.Fprintf(stdout, "Using %d public seed nodes for DISCOVERY ONLY.\n", len(SeedPeerIDs()))
		fmt.Fprintln(stdout, "  - No file transfer through seeds")
		fmt.Fprintln(stdout, "  - No service proxy through seeds")
		fmt.Fprintln(stdout, "  - No data circuits of any kind")
		fmt.Fprintln(stdout, "  Direct connections still work when both peers are online.")
		fmt.Fprintln(stdout, "  Deploy your own relay for full capability: https://shurli.io/docs/relay-setup/")
	default:
		return fmt.Errorf("invalid choice: %s (enter 1 or 2)", choice)
	}
	fmt.Fprintln(stdout)

	// Set identity password (interactive).
	fmt.Fprintln(stdout, "Set a password to protect your identity:")
	fmt.Fprintf(stdout, "  Requirements: %d+ characters, at least 3 of: uppercase, lowercase, digit, symbol\n", validate.MinPasswordLen)
	fmt.Fprintln(stdout)

	var password string
	const maxPasswordAttempts = 3
	for attempt := 1; attempt <= maxPasswordAttempts; attempt++ {
		pw, pwErr := readPasswordConfirm("Password: ", "Confirm: ", stdout)
		if pwErr == nil {
			password = pw
			break
		}
		fmt.Fprintf(stdout, "  %v\n", pwErr)
		if attempt < maxPasswordAttempts {
			fmt.Fprintf(stdout, "  Try again (%d of %d)\n\n", attempt+1, maxPasswordAttempts)
		} else {
			return fmt.Errorf("password setup failed after %d attempts", maxPasswordAttempts)
		}
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
	fmt.Fprintln(stdout, "  2. Invite a peer:  shurli invite --as home")
	fmt.Fprintln(stdout, "  3. Or connect:     shurli proxy <target> <service> <port>")
	if usedSeeds {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "  Tip: Deploy your own relay for full capability:")
		fmt.Fprintln(stdout, "       https://shurli.io/docs/relay-setup/")
	}
	fmt.Fprintln(stdout)
	tc.Wfaint(stdout, "If anything looks wrong later, run: shurli doctor\n")
	return nil
}
