package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/internal/termcolor"
)

func runRelay(args []string) {
	if len(args) < 1 {
		printRelayServeUsage()
		osExit(1)
	}

	// Extract --config flag for server-side commands (relay serve, authorize, etc.)
	// These use relay-server.yaml, not peerup.yaml.
	serverConfigFile := relayConfigFile
	for i, arg := range args {
		if (arg == "--config" || arg == "-config") && i+1 < len(args) {
			serverConfigFile = args[i+1]
			break
		}
		if strings.HasPrefix(arg, "--config=") {
			serverConfigFile = strings.TrimPrefix(arg, "--config=")
			break
		}
	}

	switch args[0] {
	// Client-side relay configuration (manages peerup.yaml)
	case "add":
		runRelayAdd(args[1:])
	case "list":
		runRelayList(args[1:])
	case "remove":
		runRelayRemove(args[1:])

	// Relay server management (manages relay-server.yaml)
	case "serve":
		runRelayServe(args[1:])
	case "authorize":
		runRelayAuthorize(args[1:], serverConfigFile)
	case "deauthorize":
		runRelayDeauthorize(args[1:], serverConfigFile)
	case "list-peers":
		runRelayListPeers(serverConfigFile)
	case "info":
		runRelayInfo(serverConfigFile)
	case "config":
		runRelayServerConfig(args[1:], serverConfigFile)
	case "version":
		runRelayServerVersion()

	default:
		fmt.Fprintf(os.Stderr, "Unknown relay command: %s\n\n", args[0])
		printRelayServeUsage()
		osExit(1)
	}
}


func resolveConfigFile(configFlag string) (string, *config.NodeConfig) {
	cfgFile, err := config.FindConfigFile(configFlag)
	if err != nil {
		fatal("Config error: %v", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		fatal("Config error: %v", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))
	return cfgFile, cfg
}

// resolveConfigFileErr is the error-returning version of resolveConfigFile,
// used by doXxx functions that return errors instead of calling fatal.
func resolveConfigFileErr(configFlag string) (string, *config.NodeConfig, error) {
	cfgFile, err := config.FindConfigFile(configFlag)
	if err != nil {
		return "", nil, fmt.Errorf("config error: %w", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		return "", nil, fmt.Errorf("config error: %w", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))
	return cfgFile, cfg, nil
}

func runRelayAdd(args []string) {
	if err := doRelayAdd(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayAdd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	peerIDFlag := fs.String("peer-id", "", "relay server's peer ID (when using IP:PORT format)")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: peerup relay add <address> [--peer-id <PEER_ID>]")
	}

	cfgFile, cfg, err := resolveConfigFileErr(*configFlag)
	if err != nil {
		return err
	}

	// Resolve addresses  - handle both full multiaddr and IP:PORT + --peer-id
	var resolvedAddrs []string
	for _, arg := range fs.Args() {
		if isFullMultiaddr(arg) {
			// Validate multiaddr format
			if _, err := ma.NewMultiaddr(arg); err != nil {
				return fmt.Errorf("invalid multiaddr: %s\n  Error: %v", arg, err)
			}
			resolvedAddrs = append(resolvedAddrs, arg)
		} else {
			// Short format  - needs --peer-id
			if *peerIDFlag == "" {
				return fmt.Errorf("short address format requires --peer-id flag.\n  Example: peerup relay add %s --peer-id 12D3KooW...", arg)
			}
			ip, port, err := parseRelayHostPort(arg)
			if err != nil {
				return fmt.Errorf("invalid address: %s\n  Error: %v", arg, err)
			}
			if err := validatePeerID(*peerIDFlag); err != nil {
				return fmt.Errorf("invalid peer ID: %v", err)
			}
			resolvedAddrs = append(resolvedAddrs, buildRelayMultiaddr(ip, port, *peerIDFlag))
		}
	}

	// Validate and collect new addresses
	var toAdd []string
	existing := make(map[string]bool)
	for _, addr := range cfg.Relay.Addresses {
		existing[addr] = true
	}

	for _, addr := range resolvedAddrs {
		if existing[addr] {
			termcolor.Yellow("Already configured: %s", truncateAddr(addr))
			continue
		}
		toAdd = append(toAdd, addr)
		existing[addr] = true
	}

	if len(toAdd) == 0 {
		fmt.Fprintln(stdout, "No new relay addresses to add.")
		return nil
	}

	// Read config file and insert new addresses
	data, err := os.ReadFile(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	content := string(data)
	lines := strings.Split(content, "\n")
	var result []string
	added := false

	for i, line := range lines {
		result = append(result, line)
		// Find the last `- "..."` line under relay.addresses and insert after it
		if !added && strings.TrimSpace(line) == "addresses:" {
			// Scan forward to find the last `- ` entry in this list
			insertIdx := len(result) - 1
			for j := i + 1; j < len(lines); j++ {
				trimmed := strings.TrimSpace(lines[j])
				if strings.HasPrefix(trimmed, "- ") {
					insertIdx = len(result) + (j - i - 1)
				} else if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
					break
				}
			}
			// We'll mark where to insert; process remaining lines up to insertIdx
			for k := i + 1; k < len(lines); k++ {
				result = append(result, lines[k])
				if len(result)-1 == insertIdx {
					// Insert new addresses here
					for _, addr := range toAdd {
						result = append(result, fmt.Sprintf("    - \"%s\"", addr))
					}
					added = true
				}
			}
			break // We've processed the rest of the file
		}
	}

	if !added {
		return fmt.Errorf("could not find relay.addresses section in config file.\nPlease add manually to: %s", cfgFile)
	}

	if err := os.WriteFile(cfgFile, []byte(strings.Join(result, "\n")), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	for _, addr := range toAdd {
		termcolor.Green("Added relay: %s", truncateAddr(addr))
	}
	fmt.Fprintf(stdout, "Config: %s\n", cfgFile)
	return nil
}

func runRelayList(args []string) {
	if err := doRelayList(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	cfgFile, cfg, err := resolveConfigFileErr(*configFlag)
	if err != nil {
		return err
	}

	if len(cfg.Relay.Addresses) == 0 {
		fmt.Fprintln(stdout, "No relay addresses configured.")
		return nil
	}

	fmt.Fprintf(stdout, "Relay addresses (%d):\n\n", len(cfg.Relay.Addresses))
	for i, addr := range cfg.Relay.Addresses {
		fmt.Fprintf(stdout, "  %d. %s\n", i+1, addr)
	}
	fmt.Fprintf(stdout, "\nConfig: %s\n", cfgFile)
	return nil
}

func runRelayRemove(args []string) {
	if err := doRelayRemove(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayRemove(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: peerup relay remove <multiaddr>")
	}

	target := fs.Arg(0)
	cfgFile, cfg, err := resolveConfigFileErr(*configFlag)
	if err != nil {
		return err
	}

	// Check it exists
	found := false
	for _, addr := range cfg.Relay.Addresses {
		if addr == target {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("relay address not found in config: %s", truncateAddr(target))
	}

	if len(cfg.Relay.Addresses) == 1 {
		return fmt.Errorf("cannot remove the last relay address. At least one relay is required")
	}

	// Remove from config file
	data, err := os.ReadFile(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	var result []string
	removed := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Match lines like:  - "/ip4/..." or  - '/ip4/...' or  - /ip4/...
		if !removed && strings.HasPrefix(trimmed, "- ") {
			// Extract the value (strip quotes)
			val := strings.TrimPrefix(trimmed, "- ")
			val = strings.Trim(val, "\"'")
			if val == target {
				removed = true
				continue // skip this line
			}
		}
		result = append(result, line)
	}

	if !removed {
		return fmt.Errorf("could not find relay address line in config file.\nPlease remove manually from: %s", cfgFile)
	}

	if err := os.WriteFile(cfgFile, []byte(strings.Join(result, "\n")), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	termcolor.Green("Removed relay: %s", truncateAddr(target))
	fmt.Fprintf(stdout, "Config: %s\n", cfgFile)
	return nil
}

// truncateAddr shortens a multiaddr for display by showing IP and truncating the peer ID.
func truncateAddr(addr string) string {
	if len(addr) > 60 {
		// Find the /p2p/ part and truncate the peer ID
		if idx := strings.Index(addr, "/p2p/"); idx >= 0 {
			peerPart := addr[idx+5:]
			if len(peerPart) > 16 {
				return addr[:idx+5] + peerPart[:16] + "..."
			}
		}
	}
	return addr
}
