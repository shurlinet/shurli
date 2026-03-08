package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tc "github.com/shurlinet/shurli/internal/termcolor"
)

func runShare(args []string) {
	if len(args) == 0 {
		printShareUsage()
		osExit(1)
	}

	switch args[0] {
	case "add":
		runShareAdd(args[1:])
	case "remove", "rm":
		runShareRemove(args[1:])
	case "list", "ls":
		runShareList(args[1:])
	case "help", "--help", "-h":
		printShareUsage()
	default:
		// Bare path: treat as "share add <path>"
		runShareAdd(args)
	}
}

func runShareAdd(args []string) {
	fs := flag.NewFlagSet("share add", flag.ExitOnError)
	peersFlag := fs.String("peers", "", "comma-separated peer IDs (empty = all authorized)")
	persistFlag := fs.Bool("persist", false, "survive daemon restart")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: shurli share add <path> [--peers id1,id2] [--persist]")
		osExit(1)
	}

	path := remaining[0]
	absPath, err := filepath.Abs(path)
	if err != nil {
		fatal("Invalid path: %v", err)
	}

	if _, err := os.Stat(absPath); err != nil {
		fatal("Cannot access path: %v", err)
	}

	var peers []string
	if *peersFlag != "" {
		peers = strings.Split(*peersFlag, ",")
	}

	client := tryDaemonClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with: shurli daemon")
		osExit(1)
	}

	if err := client.ShareAdd(absPath, peers, *persistFlag); err != nil {
		fatal("Share failed: %v", err)
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]string{"status": "shared", "path": absPath})
		return
	}

	tc.Wgreen(os.Stdout, "Shared: %s\n", absPath)
	if len(peers) > 0 {
		tc.Wfaint(os.Stdout, "  Restricted to %d peer(s)\n", len(peers))
	} else {
		tc.Wfaint(os.Stdout, "  Visible to all authorized peers\n")
	}
}

func runShareRemove(args []string) {
	fs := flag.NewFlagSet("share remove", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: shurli share remove <path>")
		osExit(1)
	}

	path := remaining[0]
	absPath, err := filepath.Abs(path)
	if err != nil {
		fatal("Invalid path: %v", err)
	}

	client := tryDaemonClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with: shurli daemon")
		osExit(1)
	}

	if err := client.ShareRemove(absPath); err != nil {
		fatal("Unshare failed: %v", err)
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]string{"status": "unshared", "path": absPath})
		return
	}

	tc.Wgreen(os.Stdout, "Unshared: %s\n", absPath)
}

func runShareList(args []string) {
	fs := flag.NewFlagSet("share list", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(args)

	client := tryDaemonClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with: shurli daemon")
		osExit(1)
	}

	if *jsonFlag {
		shares, err := client.ShareList()
		if err != nil {
			fatal("List shares failed: %v", err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(shares)
		return
	}

	text, err := client.ShareListText()
	if err != nil {
		fatal("List shares failed: %v", err)
	}

	if text == "" {
		tc.Wfaint(os.Stdout, "No paths currently shared.\n")
		tc.Wfaint(os.Stdout, "Share a path: shurli share add /path/to/file\n")
		return
	}

	fmt.Print(text)
}

func printShareUsage() {
	fmt.Println("Usage: shurli share <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  add <path> [--peers id1,id2] [--persist]   Share a file or directory")
	fmt.Println("  remove <path>                               Stop sharing a path")
	fmt.Println("  list                                        List shared paths")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  shurli share add ~/Photos                   # share with all authorized peers")
	fmt.Println("  shurli share add ~/secret.txt --peers 12D3KooW...")
	fmt.Println("  shurli share remove ~/Photos")
	fmt.Println("  shurli share list")
}
