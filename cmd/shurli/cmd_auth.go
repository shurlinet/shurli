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
	"github.com/shurlinet/shurli/internal/termcolor"
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
	default:
		fmt.Fprintf(os.Stderr, "Unknown auth command: %s\n\n", args[0])
		printAuthUsage()
		osExit(1)
	}
}

func printAuthUsage() {
	fmt.Println("Usage: shurli auth <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  add      <peer-id> [--comment \"label\"] [--role admin|member]   Authorize a peer")
	fmt.Println("  list                                                          List authorized peers")
	fmt.Println("  remove   <peer-id>                                            Revoke a peer's access")
	fmt.Println("  validate [file]                                               Validate authorized_keys format")
	fmt.Println()
	fmt.Println("All commands support --config <path> and --file <path>.")
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
	return nil
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
			fmt.Fprintf(stdout, "  %d. %s %s  # %s\n", i+1, short, roleBadge, entry.Comment)
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
