package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	tc "github.com/shurlinet/shurli/internal/termcolor"
)

func runBrowse(args []string) {
	fs := flag.NewFlagSet("browse", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	pathFlag := fs.String("path", "", "browse within a shared directory")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: shurli browse <peer> [--path /sub/dir] [--json]")
		fmt.Println()
		fmt.Println("Browse files shared by a remote peer.")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  --path <dir>  Browse within a specific shared directory")
		fmt.Println("  --json        Output as JSON")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  shurli browse home-server")
		fmt.Println("  shurli browse home-server --path /home/user/Photos")
		fmt.Println("  shurli browse 12D3KooW... --json")
		osExit(1)
	}

	peer := remaining[0]

	client := tryDaemonClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with: shurli daemon")
		osExit(1)
	}

	if *jsonFlag {
		resp, err := client.Browse(peer, *pathFlag)
		if err != nil {
			fatal("Browse failed: %v", err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
		return
	}

	text, err := client.BrowseText(peer, *pathFlag)
	if err != nil {
		fatal("Browse failed: %v", err)
	}

	if text == "" {
		tc.Wfaint(os.Stdout, "No shared files available from %s.\n", peer)
		return
	}

	fmt.Printf("Shared files from %s:\n", peer)
	fmt.Print(text)
}
