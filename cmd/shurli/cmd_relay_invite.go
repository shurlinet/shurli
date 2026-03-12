package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

func runRelayInvite(args []string, configFile string) {
	if len(args) < 1 {
		printRelayInviteUsage()
		osExit(1)
	}
	switch args[0] {
	case "create":
		runRelayInviteCreate(args[1:], configFile)
	case "list":
		runRelayInviteList(args[1:], configFile)
	case "revoke":
		runRelayInviteRevoke(args[1:], configFile)
	default:
		fmt.Fprintf(os.Stderr, "Unknown invite command: %s\n\n", args[0])
		printRelayInviteUsage()
		osExit(1)
	}
}

func runRelayInviteCreate(args []string, configFile string) {
	if err := doRelayInviteCreate(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayInviteCreate(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay invite create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	ttlFlag := fs.Duration("ttl", time.Hour, "how long the invite code is valid")
	expiresFlag := fs.Duration("expires", 0, "authorization expiry for joined peer (0 = never)")
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(reorderFlags(fs, args)); err != nil {
		return err
	}

	client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
	if err != nil {
		return err
	}
	defer cleanup()

	ttlSec := int(ttlFlag.Seconds())
	expiresSec := int(expiresFlag.Seconds())

	resp, err := client.CreateGroup(1, ttlSec, expiresSec, "")
	if err != nil {
		return fmt.Errorf("create invite failed: %w", err)
	}

	code := resp.Codes[0]
	fmt.Fprintf(stdout, "\nInvite code generated (expires in %s):\n\n", *ttlFlag)
	fmt.Fprintf(stdout, "  %s\n\n", code)
	if *expiresFlag > 0 {
		fmt.Fprintf(stdout, "Authorization expires after %s.\n\n", *expiresFlag)
	}
	fmt.Fprintf(stdout, "Group ID: %s\n\n", resp.GroupID)
	fmt.Fprintln(stdout, "--- Send this to the joining peer ---")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Install shurli: https://shurli.net/install")
	fmt.Fprintln(stdout, "Then run:")
	fmt.Fprintln(stdout, "  shurli init")
	fmt.Fprintf(stdout, "  shurli join %s\n", code)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "---")
	return nil
}

func runRelayInviteList(args []string, configFile string) {
	if err := doRelayInviteList(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayInviteList(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay invite list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	fs.Parse(reorderFlags(fs, args))

	client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
	if err != nil {
		return err
	}
	defer cleanup()

	groups, err := client.ListGroups()
	if err != nil {
		return fmt.Errorf("list invites failed: %w", err)
	}

	if len(groups) == 0 {
		fmt.Fprintln(stdout, "No active invites.")
		return nil
	}

	fmt.Fprintf(stdout, "Active invites (%d):\n\n", len(groups))
	for _, g := range groups {
		remaining := time.Until(g.ExpiresAt).Truncate(time.Second)
		status := "active"
		if remaining <= 0 {
			status = "expired"
			remaining = 0
		}
		fmt.Fprintf(stdout, "  %s  %d/%d used  %s (%s remaining)\n",
			g.ID, g.Used, g.Total, status, remaining)
	}
	return nil
}

func runRelayInviteRevoke(args []string, configFile string) {
	if err := doRelayInviteRevoke(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayInviteRevoke(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay invite revoke", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	fs.Parse(reorderFlags(fs, args))

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: shurli relay invite revoke <group-id> [--remote <addr>]")
	}
	id := fs.Arg(0)

	client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
	if err != nil {
		if *remoteFlag == "" {
			return fmt.Errorf("%w\n\nHint: use --remote <relay-addr> to revoke on a remote relay", err)
		}
		return err
	}
	defer cleanup()

	if err := client.RevokeGroup(id); err != nil {
		return fmt.Errorf("revoke failed: %w", err)
	}

	fmt.Fprintf(stdout, "Invite %s revoked.\n", id)
	return nil
}

func truncateID(s string) string {
	if len(s) > 16 {
		return s[:16] + "..."
	}
	return s
}

func printRelayInviteUsage() {
	fmt.Println("Usage: shurli relay invite <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  create  [--ttl 1h] [--expires 24h]   Generate a single-use invite code")
	fmt.Println("  list                                  List active invites")
	fmt.Println("  revoke  <group-id>                    Revoke an invite")
	fmt.Println()
	fmt.Println("All commands accept: --remote <multiaddr|name|peer-id>")
	fmt.Println()
	fmt.Println("The joining peer uses: shurli join <code>")
}
