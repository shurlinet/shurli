package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/daemon"
	"github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/internal/validate"
	"github.com/shurlinet/shurli/pkg/sdk"
)

func runAuth(args []string) {
	if len(args) < 1 {
		printAuthUsage()
		osExit(1)
	}

	switch args[0] {
	case "add":
		runAuthAdd(args[1:])
	case "list":
		runAuthList(args[1:])
	case "remove":
		runAuthRemove(args[1:])
	case "validate":
		runAuthValidate(args[1:])
	case "grant":
		runAuthGrant(args[1:])
	case "revoke":
		runAuthRevoke(args[1:])
	case "extend":
		runAuthExtend(args[1:])
	case "grants":
		runAuthGrants(args[1:])
	case "delegate":
		runAuthDelegate(args[1:])
	case "pouch":
		runAuthPouch(args[1:])
	case "set-attr":
		runAuthSetAttr(args[1:])
	case "audit":
		runAuthAudit(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown auth command: %s\n\n", args[0])
		printAuthUsage()
		osExit(1)
	}
}

func printAuthUsage() {
	fmt.Println("Usage: shurli auth <command> [options]")
	fmt.Println()
	fmt.Println("Peer authorization (authorized_keys):")
	fmt.Println("  add      <peer-id> [--comment \"label\"] [--role admin|member]   Authorize a peer")
	fmt.Println("  list                                                          List authorized peers")
	fmt.Println("  remove   <peer-id>                                            Revoke a peer's access")
	fmt.Println("  validate [file]                                               Validate authorized_keys format")
	fmt.Println("  set-attr <peer-id> <key> <value>                              Set peer attribute")
	fmt.Println()
	fmt.Println("Relay data access grants (macaroon capability tokens):")
	fmt.Println("  grant    <peer> --duration 1h [--bandwidth 1GB] [--delegate N]  Grant relay data access")
	fmt.Println("  grants                                                         List active grants")
	fmt.Println("  revoke   <peer>                                                Revoke relay data access")
	fmt.Println("  extend   <peer> --duration 2h                                  Extend a grant")
	fmt.Println("  delegate <peer> --to <target> [--duration 30m] [--delegate N]  Delegate to another peer")
	fmt.Println("  pouch                                                          List received grant tokens")
	fmt.Println("  audit    [--verify] [--tail N]                                 View or verify audit log")
	fmt.Println()
	fmt.Println("Authorization commands support --config <path> and --file <path>.")
	fmt.Println("Grant commands require a running daemon.")
}

// resolveAuthKeysPathErr finds the authorized_keys file path.
// Priority: --file flag > config's security.authorized_keys_file
func resolveAuthKeysPathErr(fileFlag, configFlag string) (string, error) {
	if fileFlag != "" {
		return fileFlag, nil
	}

	cfgFile, err := config.FindConfigFile(configFlag)
	if err != nil {
		return "", fmt.Errorf("config error: %w\nUse --file to specify authorized_keys path directly", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		return "", fmt.Errorf("config error: %w", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	if cfg.Security.AuthorizedKeysFile == "" {
		return "", fmt.Errorf("no authorized_keys_file in config. Use --file to specify path")
	}

	return cfg.Security.AuthorizedKeysFile, nil
}

func runAuthAdd(args []string) {
	if err := doAuthAdd(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doAuthAdd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("auth add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	fileFlag := fs.String("file", "", "path to authorized_keys file (overrides config)")
	commentFlag := fs.String("comment", "", "optional comment for this peer")
	roleFlag := fs.String("role", "member", "peer role: admin or member")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shurli auth add <peer-id> [--comment \"label\"] [--role admin|member]")
	}

	if *roleFlag != auth.RoleAdmin && *roleFlag != auth.RoleMember {
		return fmt.Errorf("invalid role %q: must be \"admin\" or \"member\"", *roleFlag)
	}

	peerIDStr := fs.Arg(0)
	authKeysPath, err := resolveAuthKeysPathErr(*fileFlag, *configFlag)
	if err != nil {
		return err
	}

	if err := auth.AddPeer(authKeysPath, peerIDStr, *commentFlag); err != nil {
		return fmt.Errorf("failed to add peer: %w", err)
	}

	if err := auth.SetPeerRole(authKeysPath, peerIDStr, *roleFlag); err != nil {
		return fmt.Errorf("failed to set role: %w", err)
	}

	termcolor.Green("Authorized peer: %s", peerIDStr[:min(16, len(peerIDStr))]+"...")
	if *commentFlag != "" {
		fmt.Fprintf(stdout, "  Comment: %s\n", *commentFlag)
	}
	fmt.Fprintf(stdout, "  Role: %s\n", *roleFlag)
	fmt.Fprintf(stdout, "  File: %s\n", authKeysPath)

	// Also add name mapping to config if a comment was provided.
	if *commentFlag != "" && *fileFlag == "" {
		cfgFile, cfgErr := config.FindConfigFile(*configFlag)
		if cfgErr == nil {
			name := sanitizeYAMLName(*commentFlag)
			if name != "" {
				updateConfigNames(cfgFile, filepath.Dir(cfgFile), name, peerIDStr)
				fmt.Fprintf(stdout, "  Name: %s (added to config)\n", name)
			}
		}
	}

	// Signal the running daemon to reload authorized_keys so the new peer
	// is recognized immediately (gater update + watchlist update).
	tryDaemonConfigReload()
	return nil
}

// tryDaemonConfigReload attempts to trigger a config reload on the running daemon.
// Silently succeeds if the daemon is not running (name will load on next start).
func tryDaemonConfigReload() {
	c, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return // daemon not running
	}
	c.ConfigReload() // best-effort, ignore errors
}

func runAuthList(args []string) {
	if err := doAuthList(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doAuthList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("auth list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	fileFlag := fs.String("file", "", "path to authorized_keys file (overrides config)")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	authKeysPath, err := resolveAuthKeysPathErr(*fileFlag, *configFlag)
	if err != nil {
		return err
	}

	entries, err := auth.ListPeers(authKeysPath)
	if err != nil {
		return fmt.Errorf("failed to list peers: %w", err)
	}

	if len(entries) == 0 {
		fmt.Fprintln(stdout, "No authorized peers.")
		return nil
	}

	fmt.Fprintf(stdout, "Authorized peers (%d):\n\n", len(entries))
	for i, entry := range entries {
		short := entry.PeerID.String()[:16] + "..."
		full := entry.PeerID.String()

		// Display role badge inline with the peer
		role := entry.Role
		if role == "" {
			role = auth.RoleMember
		}
		roleBadge := "[member]"
		if role == auth.RoleAdmin {
			roleBadge = "[admin]"
		}

		if entry.Comment != "" {
			fmt.Fprintf(stdout, "  %d. %s %s  # %s\n", i+1, short, roleBadge, validate.SanitizeForDisplay(entry.Comment))
		} else {
			fmt.Fprintf(stdout, "  %d. %s %s\n", i+1, short, roleBadge)
		}

		// Show attributes on the detail line.
		attrs := full
		if entry.Group != "" {
			attrs += " [group=" + entry.Group + "]"
		}
		if entry.Verified != "" {
			attrs += " [verified=" + entry.Verified + "]"
		} else {
			attrs += " [UNVERIFIED]"
		}
		if !entry.ExpiresAt.IsZero() {
			attrs += " [expires=" + entry.ExpiresAt.Format("2006-01-02") + "]"
		}
		termcolor.Faint("     %s\n", attrs)
	}
	fmt.Fprintf(stdout, "\nFile: %s\n", authKeysPath)
	return nil
}

func runAuthRemove(args []string) {
	if err := doAuthRemove(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doAuthRemove(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("auth remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	fileFlag := fs.String("file", "", "path to authorized_keys file (overrides config)")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shurli auth remove <peer-id>")
	}

	peerIDStr := fs.Arg(0)
	authKeysPath, err := resolveAuthKeysPathErr(*fileFlag, *configFlag)
	if err != nil {
		return err
	}

	if err := auth.RemovePeer(authKeysPath, peerIDStr); err != nil {
		return fmt.Errorf("failed to remove peer: %w", err)
	}

	termcolor.Green("Revoked peer: %s", peerIDStr[:min(16, len(peerIDStr))]+"...")
	fmt.Fprintf(stdout, "  File: %s\n", authKeysPath)

	// Signal the running daemon to reload authorized_keys so deauthorization
	// takes effect immediately (closes connections + managed circuits via
	// OnWatchlistRemoved callback, R7-C1).
	tryDaemonConfigReload()
	return nil
}

func runAuthValidate(args []string) {
	if err := doAuthValidate(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doAuthValidate(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("auth validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	fileFlag := fs.String("file", "", "path to authorized_keys file (overrides config)")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	// Accept positional arg or resolve from config
	authKeysPath := ""
	if fs.NArg() >= 1 {
		authKeysPath = fs.Arg(0)
	} else {
		var err error
		authKeysPath, err = resolveAuthKeysPathErr(*fileFlag, *configFlag)
		if err != nil {
			return err
		}
	}

	file, err := os.Open(authKeysPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	validCount := 0
	errorCount := 0
	var errors []string

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Extract peer ID (before # comment)
		parts := strings.SplitN(line, "#", 2)
		peerIDStr := strings.TrimSpace(parts[0])

		if peerIDStr == "" {
			continue
		}

		// Validate peer ID
		_, err := peer.Decode(peerIDStr)
		if err != nil {
			errorCount++
			errors = append(errors, fmt.Sprintf("Line %d: invalid peer ID format - %v", lineNum, err))
		} else {
			validCount++
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading file: %w", err)
	}

	if errorCount > 0 {
		termcolor.Red("Validation failed with %d error(s):", errorCount)
		for _, e := range errors {
			fmt.Fprintf(stdout, "  %s\n", e)
		}
		return fmt.Errorf("validation failed with %d error(s)", errorCount)
	}

	termcolor.Green("Validation passed")
	fmt.Fprintf(stdout, "  Valid peer IDs: %d\n", validCount)
	fmt.Fprintf(stdout, "  File: %s\n", authKeysPath)
	return nil
}

func runAuthSetAttr(args []string) {
	if err := doAuthSetAttr(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doAuthSetAttr(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("auth set-attr", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	fileFlag := fs.String("file", "", "path to authorized_keys file (overrides config)")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	if fs.NArg() != 3 {
		return fmt.Errorf("usage: shurli auth set-attr <peer-id> <key> <value>")
	}

	peerIDStr := fs.Arg(0)
	key := fs.Arg(1)
	value := fs.Arg(2)

	allowed := map[string]bool{
		"role":             true,
		"group":            true,
		"verified":         true,
		"bandwidth_budget": true,
	}
	if !allowed[key] {
		return fmt.Errorf("attribute %q not allowed (allowed: role, group, verified, bandwidth_budget)", key)
	}

	// Validate bandwidth_budget values parse correctly.
	if key == "bandwidth_budget" {
		if _, err := sdk.ParseByteSize(value); err != nil {
			return fmt.Errorf("invalid bandwidth_budget value %q: %w", value, err)
		}
	}

	authKeysPath, err := resolveAuthKeysPathErr(*fileFlag, *configFlag)
	if err != nil {
		return err
	}

	if err := auth.SetPeerAttr(authKeysPath, peerIDStr, key, value); err != nil {
		return fmt.Errorf("failed to set attribute: %w", err)
	}

	// Read back stored value (may differ from input due to sanitization).
	stored := auth.GetPeerAttr(authKeysPath, peerIDStr, key)
	if stored == "" {
		stored = value
	}
	short := peerIDStr
	if len(short) > 16 {
		short = short[:16] + "..."
	}
	termcolor.Green("Set %s=%s on peer %s", key, stored, short)
	fmt.Fprintf(stdout, "  File: %s\n", authKeysPath)
	fmt.Fprintln(stdout)
	termcolor.Wfaint(stdout, "This modifies LOCAL authorized_keys only.\n")
	termcolor.Wfaint(stdout, "It only affects connections handled by THIS node.\n")
	termcolor.Wfaint(stdout, "To set on a relay: shurli relay set-attr %s %s %s --remote <relay-addr>\n", peerIDStr, key, value)
	return nil
}
