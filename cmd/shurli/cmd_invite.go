package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/daemon"
	"github.com/shurlinet/shurli/internal/qr"
	"github.com/shurlinet/shurli/internal/termcolor"
)

func runInvite(args []string) {
	fs := flag.NewFlagSet("invite", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	nameFlag := fs.String("as", "", "your node's name on the network (e.g., \"home-server\")")
	ttlFlag := fs.Duration("ttl", 24*time.Hour, "invite code expiry duration")
	countFlag := fs.Int("count", 1, "number of invite codes to generate")
	remoteFlag := fs.String("remote", "", "relay address (multiaddr, name, or peer ID)")
	nonInteractive := fs.Bool("non-interactive", false, "machine-friendly output (no QR, bare code to stdout)")
	fs.Parse(reorderFlags(fs, args))

	// If a daemon is running, delegate to it
	if client := tryDaemonClient(); client != nil {
		runInviteViaDaemon(client, *nameFlag, *ttlFlag, *countFlag, *nonInteractive)
		return
	}

	// Standalone mode: connect to relay admin and create invite group directly
	runInviteStandalone(*configFlag, *nameFlag, *ttlFlag, *countFlag, *remoteFlag, *nonInteractive)
}

// runInviteStandalone creates an invite by calling the relay admin's CreateGroup.
// This is async: the invite is stored on the relay. No need to stay online.
func runInviteStandalone(configFlag, name string, ttl time.Duration, count int, remoteAddr string, nonInteractive bool) {
	out := fmt.Printf
	outln := fmt.Println
	if nonInteractive {
		out = func(format string, a ...any) (int, error) { return fmt.Fprintf(os.Stderr, format, a...) }
		outln = func(a ...any) (int, error) { return fmt.Fprintln(os.Stderr, a...) }
	}

	// Resolve config and relay address
	cfgFile, cfg := resolveConfigFile(configFlag)
	if remoteAddr == "" {
		if len(cfg.Relay.Addresses) == 0 {
			fatal("No relay addresses in config. Use --remote or add relay addresses to config.")
		}
		remoteAddr = cfg.Relay.Addresses[0]
	}

	out("Connecting to relay to create invite...\n")

	conn, err := connectRemoteRelay(remoteAddr)
	if err != nil {
		fatal("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	ttlSec := int(ttl.Seconds())
	resp, err := conn.client.CreateGroup(count, ttlSec, 0, "")
	if err != nil {
		fatal("Failed to create invite: %v", err)
	}

	if len(resp.Codes) == 0 {
		fatal("Relay returned no invite codes")
	}

	// Record group membership locally so the peer-notify handler accepts
	// introductions for this group when the daemon starts.
	authKeysPath := filepath.Join(filepath.Dir(cfgFile), "authorized_keys")
	if err := auth.SetPeerAttr(authKeysPath, conn.relayPeerID.String(), "group", resp.GroupID); err != nil {
		slog.Warn("invite: failed to record group on relay entry", "err", err)
	}

	printInviteCodes(resp.Codes, ttl, nonInteractive)

	outln()
	out("Invite is stored on the relay (group: %s, expires: %s).\n", resp.GroupID, resp.ExpiresAt)
	out("You can close this terminal. The joiner can use the code any time before it expires.\n")
	outln()
	out("To revoke:  shurli relay invite revoke %s --remote %s\n", resp.GroupID, remoteAddr)
	_ = name // name is for future use (peer-notify introduction)
}

// runInviteViaDaemon delegates the invite flow to a running daemon.
func runInviteViaDaemon(client *daemon.Client, name string, ttl time.Duration, count int, nonInteractive bool) {
	out := fmt.Printf
	outln := fmt.Println
	if nonInteractive {
		out = func(format string, a ...any) (int, error) { return fmt.Fprintf(os.Stderr, format, a...) }
		outln = func(a ...any) (int, error) { return fmt.Fprintln(os.Stderr, a...) }
	}

	resp, err := client.InviteCreate(name, int(ttl.Seconds()), count)
	if err != nil {
		fatal("Failed to create invite: %v", err)
	}

	if len(resp.Codes) == 0 {
		fatal("Daemon returned no invite codes")
	}

	printInviteCodes(resp.Codes, ttl, nonInteractive)

	outln()
	out("Invite is stored on the relay (group: %s, expires: %s).\n", resp.GroupID, resp.ExpiresAt)
	out("You can close this terminal. The joiner can use the code any time before it expires.\n")
	outln()
	out("To revoke:  shurli relay invite revoke %s --remote <relay-addr>\n", resp.GroupID)
}

// printInviteCodes displays one or more invite codes with optional QR.
func printInviteCodes(codes []string, ttl time.Duration, nonInteractive bool) {
	if nonInteractive {
		for _, code := range codes {
			fmt.Println(code)
		}
		return
	}

	fmt.Println()
	termcolor.Green("=== Invite Code%s (expires in %s) ===", plural(len(codes)), ttl)
	fmt.Println()

	for i, code := range codes {
		if len(codes) > 1 {
			fmt.Printf("Code %d:\n", i+1)
		}
		termcolor.Wgreen(os.Stdout, "%s", code)
		fmt.Println()
		fmt.Println()

		// Show QR code for the first code only
		if i == 0 {
			q, err := qr.New(code, qr.Medium)
			if err == nil {
				fmt.Println("Scan this QR code to join:")
				fmt.Println()
				fmt.Print(q.ToSmallString(false))
			}
		}
	}

	termcolor.Faint("--- Send this to the joining peer ---")
	fmt.Println()
	fmt.Println()
	fmt.Println("Install shurli: https://shurli.net/install")
	fmt.Println("Then run:")
	fmt.Println("  shurli init")
	fmt.Printf("  shurli join %s --as <your-device-name>\n", codes[0])
	fmt.Println()
	termcolor.Faint("---")
	fmt.Println()
}

// plural returns "s" for count > 1.
func plural(n int) string {
	if n > 1 {
		return "s"
	}
	return ""
}
