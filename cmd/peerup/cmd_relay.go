package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/internal/termcolor"
)

func runRelay(args []string) {
	if len(args) < 1 {
		printRelayUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "add":
		runRelayAdd(args[1:])
	case "list":
		runRelayList(args[1:])
	case "remove":
		runRelayRemove(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown relay command: %s\n\n", args[0])
		printRelayUsage()
		os.Exit(1)
	}
}

func printRelayUsage() {
	fmt.Println("Usage: peerup relay <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  add    <address> [--peer-id <ID>]   Add a relay server address")
	fmt.Println("  list                                List configured relay addresses")
	fmt.Println("  remove <multiaddr>                  Remove a relay server address")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  peerup relay add /ip4/203.0.113.50/tcp/7777/p2p/12D3KooW...")
	fmt.Println("  peerup relay add 203.0.113.50:7777 --peer-id 12D3KooW...")
	fmt.Println("  peerup relay add 203.0.113.50 --peer-id 12D3KooW...  (default port: 7777)")
	fmt.Println("  peerup relay list")
	fmt.Println("  peerup relay remove /ip4/203.0.113.50/tcp/7777/p2p/12D3KooW...")
	fmt.Println()
	fmt.Println("All commands support --config <path>.")
}

func resolveConfigFile(configFlag string) (string, *config.NodeConfig) {
	cfgFile, err := config.FindConfigFile(configFlag)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))
	return cfgFile, cfg
}

func runRelayAdd(args []string) {
	fs := flag.NewFlagSet("relay add", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	peerIDFlag := fs.String("peer-id", "", "relay server's peer ID (when using IP:PORT format)")
	fs.Parse(reorderArgs(args, nil))

	if fs.NArg() < 1 {
		fmt.Println("Usage: peerup relay add <address> [--peer-id <PEER_ID>]")
		fmt.Println()
		fmt.Println("Address formats:")
		fmt.Println("  /ip4/<IP>/tcp/<PORT>/p2p/<PEER_ID>   Full multiaddr")
		fmt.Println("  <IP>:<PORT> --peer-id <PEER_ID>      IP with port")
		fmt.Println("  <IP> --peer-id <PEER_ID>             IP (default port: 7777)")
		os.Exit(1)
	}

	cfgFile, cfg := resolveConfigFile(*configFlag)

	// Resolve addresses — handle both full multiaddr and IP:PORT + --peer-id
	var resolvedAddrs []string
	for _, arg := range fs.Args() {
		if isFullMultiaddr(arg) {
			// Validate multiaddr format
			if _, err := ma.NewMultiaddr(arg); err != nil {
				log.Fatalf("Invalid multiaddr: %s\n  Error: %v", arg, err)
			}
			resolvedAddrs = append(resolvedAddrs, arg)
		} else {
			// Short format — needs --peer-id
			if *peerIDFlag == "" {
				log.Fatalf("Short address format requires --peer-id flag.\n  Example: peerup relay add %s --peer-id 12D3KooW...", arg)
			}
			ip, port, err := parseRelayHostPort(arg)
			if err != nil {
				log.Fatalf("Invalid address: %s\n  Error: %v", arg, err)
			}
			if err := validatePeerID(*peerIDFlag); err != nil {
				log.Fatalf("Invalid peer ID: %v", err)
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
		fmt.Println("No new relay addresses to add.")
		return
	}

	// Read config file and insert new addresses
	data, err := os.ReadFile(cfgFile)
	if err != nil {
		log.Fatalf("Failed to read config: %v", err)
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
		// Fallback: addresses section not found in expected format
		log.Fatalf("Could not find relay.addresses section in config file.\nPlease add manually to: %s", cfgFile)
	}

	if err := os.WriteFile(cfgFile, []byte(strings.Join(result, "\n")), 0600); err != nil {
		log.Fatalf("Failed to write config: %v", err)
	}

	for _, addr := range toAdd {
		termcolor.Green("Added relay: %s", truncateAddr(addr))
	}
	fmt.Printf("Config: %s\n", cfgFile)
}

func runRelayList(args []string) {
	fs := flag.NewFlagSet("relay list", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fs.Parse(reorderArgs(args, nil))

	cfgFile, cfg := resolveConfigFile(*configFlag)

	if len(cfg.Relay.Addresses) == 0 {
		fmt.Println("No relay addresses configured.")
		return
	}

	fmt.Printf("Relay addresses (%d):\n\n", len(cfg.Relay.Addresses))
	for i, addr := range cfg.Relay.Addresses {
		fmt.Printf("  %d. %s\n", i+1, addr)
	}
	fmt.Printf("\nConfig: %s\n", cfgFile)
}

func runRelayRemove(args []string) {
	fs := flag.NewFlagSet("relay remove", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fs.Parse(reorderArgs(args, nil))

	if fs.NArg() != 1 {
		fmt.Println("Usage: peerup relay remove <multiaddr>")
		os.Exit(1)
	}

	target := fs.Arg(0)
	cfgFile, cfg := resolveConfigFile(*configFlag)

	// Check it exists
	found := false
	for _, addr := range cfg.Relay.Addresses {
		if addr == target {
			found = true
			break
		}
	}
	if !found {
		log.Fatalf("Relay address not found in config: %s", truncateAddr(target))
	}

	if len(cfg.Relay.Addresses) == 1 {
		log.Fatalf("Cannot remove the last relay address. At least one relay is required.")
	}

	// Remove from config file
	data, err := os.ReadFile(cfgFile)
	if err != nil {
		log.Fatalf("Failed to read config: %v", err)
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
		log.Fatalf("Could not find relay address line in config file.\nPlease remove manually from: %s", cfgFile)
	}

	if err := os.WriteFile(cfgFile, []byte(strings.Join(result, "\n")), 0600); err != nil {
		log.Fatalf("Failed to write config: %v", err)
	}

	termcolor.Green("Removed relay: %s", truncateAddr(target))
	fmt.Printf("Config: %s\n", cfgFile)
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
