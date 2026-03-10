package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	tc "github.com/shurlinet/shurli/internal/termcolor"
)

func runDownload(args []string) {
	fs := flag.NewFlagSet("download", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	destFlag := fs.String("dest", "", "local directory to save into (default: configured receive dir)")
	followFlag := fs.Bool("follow", false, "follow transfer progress inline")
	quietFlag := fs.Bool("quiet", false, "show only a single progress bar")
	silentFlag := fs.Bool("silent", false, "no progress output")
	multiPeerFlag := fs.Bool("multi-peer", false, "download from multiple peers using RaptorQ fountain codes")
	extraPeersFlag := fs.String("peers", "", "comma-separated extra peer names/IDs for multi-peer download")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: shurli download <peer>:<path> [--dest /local/dir] [--follow] [--json]")
		fmt.Println()
		fmt.Println("Download a file from a peer's shared files.")
		fmt.Println()
		fmt.Println("The <peer>:<path> argument specifies the remote peer and the path to download.")
		fmt.Println("Use 'shurli browse <peer>' to discover available shared files first.")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  --dest <dir>       Local directory to save into (default: configured receive dir)")
		fmt.Println("  --follow           Follow transfer progress (Ctrl+C detaches, transfer continues)")
		fmt.Println("  --quiet            Show only a single progress bar")
		fmt.Println("  --silent           No progress output at all")
		fmt.Println("  --json             Output as JSON")
		fmt.Println("  --multi-peer       Download from multiple peers simultaneously (RaptorQ)")
		fmt.Println("  --peers <list>     Comma-separated extra peer names/IDs for multi-peer")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  shurli download home-server:/home/user/Photos/vacation.jpg")
		fmt.Println("  shurli download home-server:/home/user/docs/report.pdf --dest ~/Documents")
		fmt.Println("  shurli download 12D3KooW...:/shared/file.txt --follow")
		fmt.Println("  shurli download home-server:/shared/bigfile.tar --multi-peer --peers laptop,nas")
		fmt.Println()
		fmt.Println("Browse first, then download:")
		fmt.Println("  shurli browse home-server")
		fmt.Println("  shurli download home-server:/path/shown/in/browse")
		osExit(1)
	}

	// Parse peer:path format (split on first colon after peer identifier).
	arg := remaining[0]
	peer, remotePath := parsePeerPath(arg)
	if peer == "" || remotePath == "" {
		fatal("Invalid format. Use: <peer>:<path>\n  Example: home-server:/home/user/file.txt")
	}

	client := tryDaemonClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with: shurli daemon")
		osExit(1)
	}

	if !*jsonFlag && !*silentFlag {
		tc.Wfaint(os.Stdout, "Downloading %s from %s...\n", remotePath, peer)
	}

	// Parse extra peers for multi-peer download.
	var extraPeers []string
	if *extraPeersFlag != "" {
		for _, p := range strings.Split(*extraPeersFlag, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				extraPeers = append(extraPeers, p)
			}
		}
	}

	resp, err := client.Download(peer, remotePath, *destFlag, *multiPeerFlag, extraPeers)
	if err != nil {
		fatal("Download failed: %v", err)
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
		return
	}

	tc.Wgreen(os.Stdout, "Download started")
	fmt.Printf(" [%s] %s (%s)\n", resp.TransferID, resp.FileName, humanSize(resp.FileSize))

	if !*followFlag || *silentFlag {
		if !*silentFlag {
			tc.Wfaint(os.Stdout, "Transfer continues in daemon. Check: shurli transfers\n")
		}
		return
	}

	pollTransfer(client, resp.TransferID, *quietFlag)
}

// parsePeerPath splits "peer:path" on the first colon.
// Handles peer IDs that start with "12D3KooW" (contain no colons before the path).
// For named peers like "home-server:/path", splits on first colon.
func parsePeerPath(s string) (peer, path string) {
	idx := strings.Index(s, ":")
	if idx < 0 {
		return "", ""
	}
	// If the part before the colon looks like it could be the start of a path
	// (e.g., "C:" on Windows), require at least 2 chars before the colon.
	if idx < 2 {
		return "", ""
	}
	return s[:idx], s[idx+1:]
}
