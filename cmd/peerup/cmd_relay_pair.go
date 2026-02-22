package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/internal/identity"
	"github.com/satindergrewal/peer-up/internal/invite"
	"github.com/satindergrewal/peer-up/internal/relay"
)

func runRelayPair(args []string, serverConfigFile string) {
	if len(args) > 0 && args[0] == "--list" {
		runRelayPairList()
		return
	}
	if len(args) > 0 && args[0] == "--revoke" {
		if len(args) < 2 {
			fatal("Usage: peerup relay pair --revoke <group-id>")
		}
		runRelayPairRevoke(args[1])
		return
	}

	runRelayPairCreate(args, serverConfigFile)
}

func runRelayPairCreate(args []string, serverConfigFile string) {
	fs := flag.NewFlagSet("relay pair", flag.ExitOnError)
	countFlag := fs.Int("count", 1, "number of pairing codes to generate")
	ttlFlag := fs.Duration("ttl", time.Hour, "how long codes are valid")
	expiresFlag := fs.Duration("expires", 0, "authorization expiry for joined peers (0 = never)")
	nsFlag := fs.String("namespace", "", "DHT namespace (default: from config)")
	fs.Parse(args)

	// Load relay config.
	cfg, err := config.LoadRelayServerConfig(serverConfigFile)
	if err != nil {
		fatal("Failed to load relay config: %v", err)
	}

	// Build relay multiaddr from config.
	relayAddr, err := buildRelayAddrFromConfig(cfg)
	if err != nil {
		fatal("%v", err)
	}

	// Create token store (ephemeral for this command).
	store := relay.NewTokenStore()

	ns := *nsFlag
	if ns == "" {
		ns = cfg.Discovery.Network
	}

	tokens, groupID, err := store.CreateGroup(*countFlag, *ttlFlag, ns, *expiresFlag)
	if err != nil {
		fatal("Failed to create pairing group: %v", err)
	}

	// Encode tokens into v2 invite codes.
	codes := make([]string, len(tokens))
	for i, tok := range tokens {
		code, err := invite.EncodeV2(tok, relayAddr, ns)
		if err != nil {
			fatal("Failed to encode invite code: %v", err)
		}
		codes[i] = code
	}

	// Display.
	fmt.Println()
	if *countFlag == 1 {
		fmt.Printf("Pairing code generated (expires in %s):\n\n", *ttlFlag)
		fmt.Printf("  %s\n\n", codes[0])
		fmt.Println("Share this code with the person joining your network.")
	} else {
		fmt.Printf("Pairing codes generated (expire in %s):\n\n", *ttlFlag)
		for i, code := range codes {
			fmt.Printf("  Code %d:  %s\n", i+1, code)
		}
		fmt.Println()
		fmt.Println("Share one code with each person.")
		fmt.Println("Each code can only be used once.")
	}

	if *expiresFlag > 0 {
		fmt.Printf("\nAuthorization expires after %s.\n", *expiresFlag)
	}

	fmt.Printf("\nGroup ID: %s\n", groupID)
}

func runRelayPairList() {
	// Token store is in-memory within the relay serve process.
	fmt.Println("Active pairing groups:")
	fmt.Println("  (token store is in-memory; query the running relay daemon)")
}

func runRelayPairRevoke(groupID string) {
	fmt.Printf("Revoke group %s: requires running relay daemon.\n", groupID)
	fmt.Println("  The relay serve process holds the token store in memory.")
}

// buildRelayAddrFromConfig constructs a relay multiaddr from the relay server config.
func buildRelayAddrFromConfig(cfg *config.RelayServerConfig) (string, error) {
	if len(cfg.Network.ListenAddresses) == 0 {
		return "", fmt.Errorf("no listen addresses in relay config")
	}

	// Load peer ID from key file.
	pid, err := identity.PeerIDFromKeyFile(cfg.Identity.KeyFile)
	if err != nil {
		return "", fmt.Errorf("failed to load relay identity: %w", err)
	}

	// Find a suitable listen address (prefer TCP for invite code encoding).
	var addr string
	for _, a := range cfg.Network.ListenAddresses {
		if strings.Contains(a, "/tcp/") && !strings.Contains(a, "/ws") {
			addr = a
			break
		}
	}
	if addr == "" {
		addr = cfg.Network.ListenAddresses[0]
	}

	// Replace 0.0.0.0 with a detected public IP.
	if strings.Contains(addr, "/0.0.0.0/") {
		publicIPs := detectPublicIPs()
		if len(publicIPs) > 0 {
			addr = strings.Replace(addr, "/0.0.0.0/", "/"+publicIPs[0]+"/", 1)
		} else {
			return "", fmt.Errorf("relay listens on 0.0.0.0 but no public IP detected; specify a public address in config")
		}
	}

	return addr + "/p2p/" + pid.String(), nil
}
