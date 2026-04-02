package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/grants"
	"github.com/shurlinet/shurli/internal/relay"
	"github.com/shurlinet/shurli/internal/termcolor"
)

func runRelayGrant(args []string, configFile string) {
	if err := doRelayGrant(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayGrant(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay grant", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	duration := fs.String("duration", "1h", "grant duration (e.g. 1h, 7d, 30m)")
	services := fs.String("services", "", "comma-separated service names (empty = all)")
	permanent := fs.Bool("permanent", false, "grant permanent access (no expiry)")
	dataFlag := fs.String("data", "", "per-peer data budget (e.g. 500MB, 2GB, unlimited; empty = relay default)")
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shurli relay grant <peer-id> [--duration 1h] [--services ...] [--permanent] [--data 500MB] [--remote <addr>]")
	}

	peerID := fs.Arg(0)

	// Permanent grants require confirmation (E4 mitigation).
	if *permanent {
		fmt.Fprint(stdout, "Permanent grants cannot be auto-expired. Are you sure? [y/N] ")
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Fprintln(stdout, "Cancelled.")
			return nil
		}
	}

	// Parse duration.
	dur, err := grants.ParseDurationExtended(*duration)
	if err != nil && !*permanent {
		return fmt.Errorf("invalid duration %q: %w", *duration, err)
	}
	durationSecs := int(dur.Seconds())
	if !*permanent && durationSecs <= 0 {
		return fmt.Errorf("duration must be positive")
	}

	var svcList []string
	if *services != "" {
		for _, s := range strings.Split(*services, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				svcList = append(svcList, s)
			}
		}
	}

	client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
	if err != nil {
		return err
	}
	defer cleanup()

	info, err := client.RelayGrant(peerID, durationSecs, svcList, *permanent, *dataFlag)
	if err != nil {
		return err
	}

	termcolor.Green("Granted relay data access")
	fmt.Fprintf(stdout, "  Peer: %s\n", info.PeerID)
	if len(info.Services) > 0 {
		fmt.Fprintf(stdout, "  Services: %s\n", strings.Join(info.Services, ", "))
	} else {
		fmt.Fprintln(stdout, "  Services: all")
	}
	if info.Permanent {
		fmt.Fprintln(stdout, "  Duration: permanent")
	} else {
		fmt.Fprintf(stdout, "  Expires: %s (%s remaining)\n", info.ExpiresAt, formatRemainingTime(info.RemainingSec))
	}
	if info.DataBudgetStr != "" {
		fmt.Fprintf(stdout, "  Data budget: %s\n", info.DataBudgetStr)
	}
	return nil
}

func runRelayGrants(args []string, configFile string) {
	if err := doRelayGrants(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayGrants(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay grants", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
	if err != nil {
		return err
	}
	defer cleanup()

	grantList, err := client.RelayGrants()
	if err != nil {
		return err
	}

	if len(grantList) == 0 {
		fmt.Fprintln(stdout, "No active relay data grants.")
		fmt.Fprintln(stdout, "\nTip: use 'shurli relay grant <peer-id> --duration 1h' to grant relay data access.")
		return nil
	}

	// Build peer ID -> name map from authorized_keys (local relay only).
	// When using --remote, we can't access the remote relay's authorized_keys.
	peerNames := make(map[string]string)
	if *remoteFlag == "" && configFile != "" {
		if relayCfg, loadErr := config.LoadRelayServerConfig(configFile); loadErr == nil {
			config.ResolveRelayConfigPaths(relayCfg, filepath.Dir(configFile))
			if peers, listErr := auth.ListPeers(relayCfg.Security.AuthorizedKeysFile); listErr == nil {
				for _, p := range peers {
					if p.Comment != "" {
						peerNames[p.PeerID.String()] = p.Comment
					}
				}
			}
		}
	}

	fmt.Fprintf(stdout, "Relay data grants (%d):\n\n", len(grantList))
	for _, g := range grantList {
		printRelayGrantInfo(stdout, g, peerNames)
	}
	return nil
}

func printRelayGrantInfo(stdout io.Writer, g relay.RelayGrantInfo, peerNames map[string]string) {
	pid := g.PeerID
	if len(pid) > 16 {
		pid = pid[:16] + "..."
	}

	// Show peer name from authorized_keys if available.
	name := peerNames[g.PeerID]

	scope := "[all]"
	if len(g.Services) > 0 {
		scope = "[" + strings.Join(g.Services, ",") + "]"
	}

	budgetStr := ""
	if g.DataBudgetStr != "" && g.DataBudgetStr != "default" {
		budgetStr = "  data:" + g.DataBudgetStr
	}

	if g.Permanent {
		fmt.Fprintf(stdout, "  %s  %s  permanent%s", pid, scope, budgetStr)
	} else {
		fmt.Fprintf(stdout, "  %s  %s  %s remaining%s", pid, scope, formatRemainingTime(g.RemainingSec), budgetStr)
	}
	if name != "" {
		termcolor.Wfaint(stdout, "  # %s", name)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "    Full ID: %s\n", g.PeerID)
}

func runRelayRevoke(args []string, configFile string) {
	if err := doRelayRevoke(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayRevoke(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay revoke", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shurli relay revoke <peer-id> [--remote <addr>]")
	}

	peerID := fs.Arg(0)

	client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := client.RelayRevoke(peerID); err != nil {
		return err
	}

	termcolor.Green("Revoked relay data access for %s", peerID[:min(16, len(peerID))]+"...")
	fmt.Fprintln(stdout, "All circuits for this peer have been terminated.")
	return nil
}

func runRelayExtend(args []string, configFile string) {
	if err := doRelayExtend(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayExtend(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay extend", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	duration := fs.String("duration", "", "new duration from now (e.g. 2h, 1d)")
	dataFlag := fs.String("data", "", "update data budget (e.g. 1GB, unlimited; empty = keep current)")
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shurli relay extend <peer-id> --duration 2h [--remote <addr>]")
	}
	if *duration == "" {
		return fmt.Errorf("--duration is required")
	}

	peerID := fs.Arg(0)

	dur, err := grants.ParseDurationExtended(*duration)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", *duration, err)
	}
	durationSecs := int(dur.Seconds())
	if durationSecs <= 0 {
		return fmt.Errorf("duration must be positive")
	}

	client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := client.RelayExtend(peerID, durationSecs, *dataFlag); err != nil {
		return err
	}

	termcolor.Green("Extended relay data access for %s", peerID[:min(16, len(peerID))]+"...")
	fmt.Fprintf(stdout, "  New duration: %s from now\n", *duration)
	return nil
}

// formatRemainingTime formats seconds into a human-readable remaining time string.
// Wraps formatDuration (cmd_relay_serve.go) with an "expired" fallback for <= 0.
func formatRemainingTime(secs int) string {
	if secs <= 0 {
		return "expired"
	}
	return formatDuration(secs)
}
