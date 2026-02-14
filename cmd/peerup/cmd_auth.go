package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/satindergrewal/peer-up/internal/auth"
	"github.com/satindergrewal/peer-up/internal/config"
)

// reorderFlagsFirst moves flag arguments (--foo val) before positional args,
// so Go's flag package can parse them regardless of argument order.
func reorderFlagsFirst(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			// If this flag has a value argument (next arg is not a flag), include it
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
		} else {
			positional = append(positional, args[i])
		}
	}
	return append(flags, positional...)
}

func runAuth(args []string) {
	if len(args) < 1 {
		printAuthUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "add":
		runAuthAdd(args[1:])
	case "list":
		runAuthList(args[1:])
	case "remove":
		runAuthRemove(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown auth command: %s\n\n", args[0])
		printAuthUsage()
		os.Exit(1)
	}
}

func printAuthUsage() {
	fmt.Println("Usage: peerup auth <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  add    <peer-id> [--comment \"label\"]   Authorize a peer")
	fmt.Println("  list                                   List authorized peers")
	fmt.Println("  remove <peer-id>                       Revoke a peer's access")
	fmt.Println()
	fmt.Println("All commands support --config <path> and --file <path>.")
}

// resolveAuthKeysPath finds the authorized_keys file path.
// Priority: --file flag > config's security.authorized_keys_file
func resolveAuthKeysPath(fileFlag, configFlag string) string {
	if fileFlag != "" {
		return fileFlag
	}

	cfgFile, err := config.FindConfigFile(configFlag)
	if err != nil {
		log.Fatalf("Config error: %v\nUse --file to specify authorized_keys path directly.", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	if cfg.Security.AuthorizedKeysFile == "" {
		log.Fatalf("No authorized_keys_file in config. Use --file to specify path.")
	}

	return cfg.Security.AuthorizedKeysFile
}

func runAuthAdd(args []string) {
	fs := flag.NewFlagSet("auth add", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fileFlag := fs.String("file", "", "path to authorized_keys file (overrides config)")
	commentFlag := fs.String("comment", "", "optional comment for this peer")
	fs.Parse(reorderFlagsFirst(args))

	if fs.NArg() != 1 {
		fmt.Println("Usage: peerup auth add <peer-id> [--comment \"label\"]")
		os.Exit(1)
	}

	peerIDStr := fs.Arg(0)
	authKeysPath := resolveAuthKeysPath(*fileFlag, *configFlag)

	if err := auth.AddPeer(authKeysPath, peerIDStr, *commentFlag); err != nil {
		log.Fatalf("Failed to add peer: %v", err)
	}

	color.Green("Authorized peer: %s", peerIDStr[:min(16, len(peerIDStr))]+"...")
	if *commentFlag != "" {
		fmt.Printf("  Comment: %s\n", *commentFlag)
	}
	fmt.Printf("  File: %s\n", authKeysPath)
}

func runAuthList(args []string) {
	fs := flag.NewFlagSet("auth list", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fileFlag := fs.String("file", "", "path to authorized_keys file (overrides config)")
	fs.Parse(reorderFlagsFirst(args))

	authKeysPath := resolveAuthKeysPath(*fileFlag, *configFlag)

	entries, err := auth.ListPeers(authKeysPath)
	if err != nil {
		log.Fatalf("Failed to list peers: %v", err)
	}

	if len(entries) == 0 {
		fmt.Println("No authorized peers.")
		return
	}

	fmt.Printf("Authorized peers (%d):\n\n", len(entries))
	for i, entry := range entries {
		short := entry.PeerID.String()[:16] + "..."
		full := entry.PeerID.String()
		if entry.Comment != "" {
			fmt.Printf("  %d. %s  # %s\n", i+1, short, entry.Comment)
		} else {
			fmt.Printf("  %d. %s\n", i+1, short)
		}
		color.New(color.Faint).Printf("     %s\n", full)
	}
	fmt.Printf("\nFile: %s\n", authKeysPath)
}

func runAuthRemove(args []string) {
	fs := flag.NewFlagSet("auth remove", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fileFlag := fs.String("file", "", "path to authorized_keys file (overrides config)")
	fs.Parse(reorderFlagsFirst(args))

	if fs.NArg() != 1 {
		fmt.Println("Usage: peerup auth remove <peer-id>")
		os.Exit(1)
	}

	peerIDStr := fs.Arg(0)
	authKeysPath := resolveAuthKeysPath(*fileFlag, *configFlag)

	if err := auth.RemovePeer(authKeysPath, peerIDStr); err != nil {
		log.Fatalf("Failed to remove peer: %v", err)
	}

	color.Green("Revoked peer: %s", peerIDStr[:min(16, len(peerIDStr))]+"...")
	fmt.Printf("  File: %s\n", authKeysPath)
}
