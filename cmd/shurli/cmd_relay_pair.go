package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/identity"
	"github.com/shurlinet/shurli/internal/relay"
)

func runRelayPair(args []string, serverConfigFile string) {
	if len(args) > 0 && args[0] == "--list" {
		runRelayPairList(serverConfigFile)
		return
	}
	if len(args) > 0 && args[0] == "--revoke" {
		if len(args) < 2 {
			fatal("Usage: shurli relay pair --revoke <group-id>")
		}
		runRelayPairRevoke(args[1], serverConfigFile)
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

	client := connectRelayAdmin(serverConfigFile)

	ttlSec := int(ttlFlag.Seconds())
	expiresSec := int(expiresFlag.Seconds())

	resp, err := client.CreateGroup(*countFlag, ttlSec, expiresSec, *nsFlag)
	if err != nil {
		fatal("Failed to create pairing group: %v", err)
	}

	// Display.
	fmt.Println()
	if *countFlag == 1 {
		fmt.Printf("Pairing code generated (expires in %s):\n\n", *ttlFlag)
		fmt.Printf("  %s\n\n", resp.Codes[0])
		fmt.Println("Share this code with the person joining your network.")
	} else {
		fmt.Printf("Pairing codes generated (expire in %s):\n\n", *ttlFlag)
		for i, code := range resp.Codes {
			fmt.Printf("  Code %d:  %s\n", i+1, code)
		}
		fmt.Println()
		fmt.Println("Share one code with each person.")
		fmt.Println("Each code can only be used once.")
	}

	if *expiresFlag > 0 {
		fmt.Printf("\nAuthorization expires after %s.\n", *expiresFlag)
	}

	fmt.Printf("\nGroup ID: %s\n", resp.GroupID)
}

func runRelayPairList(serverConfigFile string) {
	client := connectRelayAdmin(serverConfigFile)

	groups, err := client.ListGroups()
	if err != nil {
		fatal("Failed to list pairing groups: %v", err)
	}

	if len(groups) == 0 {
		fmt.Println("No active pairing groups.")
		return
	}

	fmt.Printf("Active pairing groups (%d):\n\n", len(groups))
	for _, g := range groups {
		remaining := time.Until(g.ExpiresAt).Truncate(time.Second)
		status := "active"
		if remaining <= 0 {
			status = "expired"
			remaining = 0
		}
		fmt.Printf("  %s  %d/%d used  %s (%s remaining)\n", g.ID, g.Used, g.Total, status, remaining)
	}
}

func runRelayPairRevoke(groupID, serverConfigFile string) {
	client := connectRelayAdmin(serverConfigFile)

	if err := client.RevokeGroup(groupID); err != nil {
		fatal("Failed to revoke group: %v", err)
	}

	fmt.Printf("Pairing group %s revoked.\n", groupID)
}

// connectRelayAdmin creates an AdminClient connected to the running relay.
func connectRelayAdmin(serverConfigFile string) *relay.AdminClient {
	socketPath := filepath.Join(filepath.Dir(serverConfigFile), ".relay-admin.sock")
	cookiePath := filepath.Join(filepath.Dir(serverConfigFile), ".relay-admin.cookie")

	client, err := relay.NewAdminClient(socketPath, cookiePath)
	if err != nil {
		fatal("Relay is not running. Start with: shurli relay serve\n  (%v)", err)
	}
	return client
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
