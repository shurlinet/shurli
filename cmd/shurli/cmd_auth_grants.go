package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shurlinet/shurli/internal/daemon"
	"github.com/shurlinet/shurli/internal/termcolor"
)

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
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shurli auth grant <peer> [--duration 1h] [--services file-transfer,...] [--permanent]")
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
		Peer:      peerName,
		Duration:  *duration,
		Services:  svcList,
		Permanent: *permanent,
	}

	info, err := client.GrantCreate(req)
	if err != nil {
		return err
	}

	termcolor.Green("Granted data access to %s", info.Peer)
	if len(info.Services) > 0 {
		fmt.Fprintf(stdout, "  Services: %s\n", strings.Join(info.Services, ", "))
	} else {
		fmt.Fprintln(stdout, "  Services: all")
	}
	if info.Permanent {
		fmt.Fprintln(stdout, "  Duration: permanent")
	} else {
		fmt.Fprintf(stdout, "  Expires:  %s (%s remaining)\n", info.ExpiresAt, info.Remaining)
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
		fmt.Fprintf(stdout, "  %d. %s  [%s]  %s\n", i+1, g.Peer, svc, dur)
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
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shurli auth extend <peer> --duration 2h")
	}
	if *duration == "" {
		return fmt.Errorf("--duration is required")
	}

	peerName := fs.Arg(0)

	client, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return err
	}

	if err := client.GrantExtend(peerName, *duration); err != nil {
		return err
	}

	termcolor.Green("Extended data access grant for %s by %s", peerName, *duration)
	return nil
}
