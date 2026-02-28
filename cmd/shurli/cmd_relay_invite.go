package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shurlinet/shurli/internal/relay"
	"github.com/shurlinet/shurli/internal/termcolor"
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
	case "modify":
		runRelayInviteModify(args[1:], configFile)
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
	caveatFlag := fs.String("caveat", "", "comma-separated caveats (e.g. 'service=proxy,action=connect')")
	ttlFlag := fs.Int("ttl", 0, "deposit TTL in seconds (0 = never expires)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, err := inviteAdminClient(configFile)
	if err != nil {
		return err
	}

	var caveats []string
	if *caveatFlag != "" {
		// Split by semicolons to allow multiple caveats
		caveats = strings.Split(*caveatFlag, ";")
	}

	resp, err := client.CreateInvite(caveats, *ttlFlag)
	if err != nil {
		return fmt.Errorf("create invite failed: %w", err)
	}

	termcolor.Green("Invite deposit created!")
	fmt.Fprintf(stdout, "  ID:       %s\n", resp["id"])
	fmt.Fprintf(stdout, "  Macaroon: %s\n", resp["macaroon"])
	if exp := resp["expires_at"]; exp != "" {
		fmt.Fprintf(stdout, "  Expires:  %s\n", exp)
	}
	if len(caveats) > 0 {
		fmt.Fprintf(stdout, "  Caveats:  %s\n", strings.Join(caveats, ", "))
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Share the macaroon token with the joining peer.")
	fmt.Fprintln(stdout, "Permissions can be restricted (but not widened) before consumption.")

	return nil
}

func runRelayInviteList(args []string, configFile string) {
	if err := doRelayInviteList(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayInviteList(_ []string, configFile string, stdout io.Writer) error {
	client, err := inviteAdminClient(configFile)
	if err != nil {
		return err
	}

	invites, err := client.ListInvites()
	if err != nil {
		return fmt.Errorf("list invites failed: %w", err)
	}

	if len(invites) == 0 {
		fmt.Fprintln(stdout, "No invite deposits.")
		return nil
	}

	fmt.Fprintf(stdout, "Invite deposits (%d):\n\n", len(invites))
	for _, inv := range invites {
		id, _ := inv["id"].(string)
		status, _ := inv["status"].(string)
		createdAt, _ := inv["created_at"].(string)
		caveats, _ := inv["caveats"].(float64)

		statusStr := status
		switch status {
		case "pending":
			statusStr = "[pending]"
		case "consumed":
			consumedBy, _ := inv["consumed_by"].(string)
			statusStr = fmt.Sprintf("[consumed by %s]", truncateID(consumedBy))
		case "revoked":
			statusStr = "[revoked]"
		case "expired":
			statusStr = "[expired]"
		}

		fmt.Fprintf(stdout, "  %s  %s  caveats=%d  created=%s\n",
			id, statusStr, int(caveats), createdAt)
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
	if len(args) < 1 {
		return fmt.Errorf("usage: shurli relay invite revoke <id>")
	}
	id := args[0]

	client, err := inviteAdminClient(configFile)
	if err != nil {
		return err
	}

	if err := client.RevokeInvite(id); err != nil {
		return fmt.Errorf("revoke failed: %w", err)
	}

	termcolor.Green("Invite %s revoked.", id)
	fmt.Fprintln(stdout)
	return nil
}

func runRelayInviteModify(args []string, configFile string) {
	if err := doRelayInviteModify(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayInviteModify(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay invite modify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addCaveat := fs.String("add-caveat", "", "caveat to add (semicolon-separated for multiple)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: shurli relay invite modify <id> --add-caveat <k=v>")
	}
	id := fs.Arg(0)

	if *addCaveat == "" {
		return fmt.Errorf("--add-caveat is required")
	}

	caveats := strings.Split(*addCaveat, ";")

	client, err := inviteAdminClient(configFile)
	if err != nil {
		return err
	}

	if err := client.ModifyInvite(id, caveats); err != nil {
		return fmt.Errorf("modify failed: %w", err)
	}

	termcolor.Green("Invite %s modified: added %d caveat(s).", id, len(caveats))
	fmt.Fprintln(stdout)
	return nil
}

func inviteAdminClient(configFile string) (*relay.AdminClient, error) {
	dir := filepath.Dir(configFile)
	socketPath := filepath.Join(dir, ".relay-admin.sock")
	cookiePath := filepath.Join(dir, ".relay-admin.cookie")
	return relay.NewAdminClient(socketPath, cookiePath)
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
	fmt.Println("  create  [--caveat <k=v;...>] [--ttl N]   Create macaroon-backed invite")
	fmt.Println("  list                                      List all invite deposits")
	fmt.Println("  revoke  <id>                              Revoke a pending invite")
	fmt.Println("  modify  <id> --add-caveat <k=v>           Add restrictions to invite")
	fmt.Println()
	fmt.Println("Invite deposits are async: the joining peer does not need to be online")
	fmt.Println("at creation time. Permissions can be restricted (never widened) before")
	fmt.Println("consumption. Permissions are attenuation-only: restrict or revoke, never widen.")
}
