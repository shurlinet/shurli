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
		fmt.Println("Usage: shurli browse <peer> [<path>] [--path /sub/dir] [--json]")
		fmt.Println()
		fmt.Println("Browse files shared by a remote peer.")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  <path>        Browse path (positional, alternative to --path)")
		fmt.Println("  --path <dir>  Browse within a specific shared directory")
		fmt.Println("  --json        Output as JSON")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  shurli browse home-server")
		fmt.Println("  shurli browse home-server share-abc123/subdir")
		fmt.Println("  shurli browse home-server --path share-abc123/subdir")
		fmt.Println("  shurli browse 12D3KooW... --json")
		osExit(1)
	}

	if len(remaining) > 2 {
		fatal("Too many arguments. Usage: shurli browse <peer> [<path>]")
	}

	peer := remaining[0]

	// Accept optional second positional argument as browse path.
	browsePath := *pathFlag
	if len(remaining) > 1 {
		if browsePath != "" {
			fatal("Specify path as positional argument or --path flag, not both")
		}
		browsePath = remaining[1]
	}

	client := tryDaemonClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with: shurli daemon")
		osExit(1)
	}

	if *jsonFlag {
		resp, err := client.Browse(peer, browsePath)
		if err != nil {
			fatal("Browse failed: %v", err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
		return
	}

	text, err := client.BrowseText(peer, browsePath)
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
