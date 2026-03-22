package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shurlinet/shurli/internal/daemon"
	"github.com/shurlinet/shurli/internal/termcolor"
)

// formatDelegation returns a human-readable delegation mode string.
func formatDelegation(maxDelegations int) string {
	switch {
	case maxDelegations == 0:
		return "disabled"
	case maxDelegations == -1:
		return "unlimited"
	default:
		return fmt.Sprintf("%d hops", maxDelegations)
	}
}

// formatEffectiveDur returns a human-readable duration (e.g. "4h" not "4h0m0s").
func formatEffectiveDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if d >= 24*time.Hour {
		days := h / 24
		h = h % 24
		if h > 0 {
			return fmt.Sprintf("%dd%dh", days, h)
		}
		return fmt.Sprintf("%dd", days)
	}
	if m > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dh", h)
}

// parseDelegateFlag converts the --delegate flag value to an int.
// Accepts: "0" (none), positive integers (limited hops), "unlimited" or "-1" (unlimited).
func parseDelegateFlag(s string) (int, error) {
	if s == "unlimited" {
		return -1, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --delegate value %q: use a number or \"unlimited\"", s)
	}
	if v < -1 {
		return 0, fmt.Errorf("invalid --delegate value %d: minimum is -1 (unlimited)", v)
	}
	return v, nil
}

func runAuthGrant(args []string) {
	if err := doAuthGrant(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doAuthGrant(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("auth grant", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	duration := fs.String("duration", "1h", "grant duration (e.g. 1h, 7d, 30m)")
	services := fs.String("services", "", "comma-separated service names (empty = all)")
	permanent := fs.Bool("permanent", false, "grant permanent access (no expiry)")
	delegateStr := fs.String("delegate", "0", "delegation hops: 0=none (default), N=limited, unlimited")
	autoRefresh := fs.Bool("auto-refresh", false, "enable automatic token refresh before expiry")
	maxRefreshes := fs.Int("max-refreshes", 3, "max number of refreshes (requires --auto-refresh)")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shurli auth grant <peer> [--duration 1h] [--services file-transfer,...] [--permanent] [--delegate N|unlimited] [--auto-refresh] [--max-refreshes N]")
	}

	delegateVal, err := parseDelegateFlag(*delegateStr)
	if err != nil {
		return err
	}

	if *autoRefresh && *permanent {
		return fmt.Errorf("--auto-refresh and --permanent are mutually exclusive (permanent grants never expire)")
	}

	peerName := fs.Arg(0)

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

	var svcList []string
	if *services != "" {
		for _, s := range strings.Split(*services, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				svcList = append(svcList, s)
			}
		}
	}

	client, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return err
	}

	req := daemon.GrantRequest{
		Peer:           peerName,
		Duration:       *duration,
		Services:       svcList,
		Permanent:      *permanent,
		MaxDelegations: delegateVal,
		AutoRefresh:    *autoRefresh,
		MaxRefreshes:   *maxRefreshes,
	}

	info, err := client.GrantCreate(req)
	if err != nil {
		return err
	}

	termcolor.Green("Granted data access to %s", info.Peer)
	if len(info.Services) > 0 {
		fmt.Fprintf(stdout, "  Services:   %s\n", strings.Join(info.Services, ", "))
	} else {
		fmt.Fprintln(stdout, "  Services:   all")
	}
	if info.Permanent {
		fmt.Fprintln(stdout, "  Duration:   permanent")
	} else {
		fmt.Fprintf(stdout, "  Expires:    %s (%s remaining)\n", info.ExpiresAt, info.Remaining)
	}
	fmt.Fprintf(stdout, "  Delegation: %s\n", formatDelegation(info.MaxDelegations))
	if *autoRefresh {
		effectiveDur, _ := time.ParseDuration(*duration)
		effectiveMax := effectiveDur * time.Duration(*maxRefreshes+1)
		fmt.Fprintf(stdout, "  Refresh:    auto-refresh: %d refreshes, %s effective max duration\n", *maxRefreshes, formatEffectiveDur(effectiveMax))
	}
	return nil
}

func runAuthGrants(args []string) {
	if err := doAuthGrants(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doAuthGrants(args []string, stdout io.Writer) error {
	client, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return err
	}

	resp, err := client.GrantList()
	if err != nil {
		return err
	}

	if len(resp.Grants) == 0 {
		fmt.Fprintln(stdout, "No active data access grants.")
		fmt.Fprintln(stdout, "\nTip: use 'shurli auth grant <peer> --duration 1h' to grant relay data access.")
		return nil
	}

	fmt.Fprintf(stdout, "Active data access grants (%d):\n\n", len(resp.Grants))
	for i, g := range resp.Grants {
		svc := "all"
		if len(g.Services) > 0 {
			svc = strings.Join(g.Services, ",")
		}
		dur := g.Remaining
		if g.Permanent {
			dur = "permanent"
		}
		delStr := ""
		if g.MaxDelegations != 0 {
			delStr = "  delegate:" + formatDelegation(g.MaxDelegations)
		}
		refreshStr := ""
		if g.AutoRefresh {
			refreshStr = fmt.Sprintf("  refresh:%d/%d", g.RefreshesUsed, g.MaxRefreshes)
		}
		fmt.Fprintf(stdout, "  %d. %s  [%s]  %s%s%s\n", i+1, g.Peer, svc, dur, delStr, refreshStr)
		termcolor.Faint("     %s\n", g.PeerID)
	}
	return nil
}

func runAuthRevoke(args []string) {
	if err := doAuthRevoke(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doAuthRevoke(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("auth revoke", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shurli auth revoke <peer>")
	}

	peerName := fs.Arg(0)

	client, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return err
	}

	if err := client.GrantRevoke(peerName); err != nil {
		return err
	}

	termcolor.Green("Revoked data access grant for %s", peerName)
	fmt.Fprintln(stdout, "  All connections to this peer have been closed.")
	return nil
}

func runAuthDelegate(args []string) {
	if err := doAuthDelegate(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doAuthDelegate(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("auth delegate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	to := fs.String("to", "", "target peer to delegate to (required)")
	duration := fs.String("duration", "", "optional shorter duration (e.g. 30m)")
	services := fs.String("services", "", "optional comma-separated service names")
	delegateStr := fs.String("delegate", "0", "further delegation hops for target (0=none, N, unlimited)")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shurli auth delegate <peer> --to <target> [--duration 30m] [--services file-browse] [--delegate N|unlimited]")
	}
	if *to == "" {
		return fmt.Errorf("--to is required (target peer for delegation)")
	}

	delegateVal, err := parseDelegateFlag(*delegateStr)
	if err != nil {
		return err
	}

	peerName := fs.Arg(0)

	var svcList []string
	if *services != "" {
		for _, s := range strings.Split(*services, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				svcList = append(svcList, s)
			}
		}
	}

	client, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return err
	}

	req := daemon.GrantDelegateRequest{
		Peer:           peerName,
		To:             *to,
		Duration:       *duration,
		Services:       svcList,
		MaxDelegations: delegateVal,
	}

	result, err := client.GrantDelegate(req)
	if err != nil {
		return err
	}

	status := result["status"]
	target := result["target"]
	if status == "queued" {
		termcolor.Green("Delegated grant to %s (queued for delivery - peer offline)", target)
	} else {
		termcolor.Green("Delegated grant to %s (delivered)", target)
	}
	fmt.Fprintf(stdout, "  From: %s's grant\n", peerName)
	if *duration != "" {
		fmt.Fprintf(stdout, "  Duration: %s\n", *duration)
	}
	if len(svcList) > 0 {
		fmt.Fprintf(stdout, "  Services: %s\n", strings.Join(svcList, ", "))
	}
	fmt.Fprintf(stdout, "  Delegation: %s\n", formatDelegation(delegateVal))
	return nil
}

func runAuthExtend(args []string) {
	if err := doAuthExtend(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doAuthExtend(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("auth extend", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	duration := fs.String("duration", "", "new duration from now (e.g. 2h, 1d)")
	maxRefreshes := fs.Int("max-refreshes", -1, "update max refresh count (-1 = no change)")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shurli auth extend <peer> --duration 2h [--max-refreshes N]")
	}
	if *duration == "" && *maxRefreshes < 0 {
		return fmt.Errorf("--duration or --max-refreshes is required")
	}

	peerName := fs.Arg(0)

	client, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return err
	}

	req := daemon.GrantExtendRequest{
		Peer:     peerName,
		Duration: *duration,
	}
	if *maxRefreshes >= 0 {
		v := *maxRefreshes
		req.MaxRefreshes = &v
	}

	if err := client.GrantExtendFull(req); err != nil {
		return err
	}

	termcolor.Green("Extended data access grant for %s", peerName)
	if *duration != "" {
		fmt.Fprintf(stdout, "  Duration: %s from now\n", *duration)
	}
	if *maxRefreshes >= 0 {
		fmt.Fprintf(stdout, "  Max refreshes: %d\n", *maxRefreshes)
	}
	return nil
}
