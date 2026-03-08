package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shurlinet/shurli/internal/daemon"
	tc "github.com/shurlinet/shurli/internal/termcolor"
)

func runSend(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 2 {
		fmt.Println("Usage: shurli send <file> <peer>")
		fmt.Println("       shurli send <file> --to <peer>")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  --json    Output as JSON")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  shurli send photo.jpg home-server")
		fmt.Println("  shurli send ~/Documents/report.pdf laptop")
		fmt.Println("  shurli send backup.tar.gz 12D3KooW...")
		osExit(1)
	}

	filePath := remaining[0]
	peer := remaining[1]

	// Strip --to if used as a keyword
	if peer == "--to" {
		if len(remaining) < 3 {
			fatal("Missing peer after --to")
		}
		peer = remaining[2]
	}

	// Resolve to absolute path
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		fatal("Invalid path: %v", err)
	}

	// Check file exists
	info, err := os.Stat(absPath)
	if err != nil {
		fatal("Cannot access file: %v", err)
	}
	if info.IsDir() {
		fatal("Cannot send directory (single files only for now)")
	}

	client := tryDaemonClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with:")
		fmt.Println("  shurli daemon")
		osExit(1)
	}

	if !*jsonFlag {
		tc.Wfaint(os.Stdout, "Sending %s (%s) to %s...\n",
			filepath.Base(absPath), humanSize(info.Size()), peer)
	}

	resp, err := client.Send(absPath, peer)
	if err != nil {
		fatal("Send failed: %v", err)
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
		return
	}

	tc.Wgreen(os.Stdout, "Transfer started")
	fmt.Printf(" [%s]\n", resp.TransferID)

	// Poll for completion
	pollTransfer(client, resp.TransferID)
}

// pollTransfer polls the daemon for transfer progress until complete.
func pollTransfer(client *daemon.Client, id string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		progress, err := client.TransferStatus(id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cannot check transfer status: %v\n", err)
			return
		}

		if progress.Done {
			if progress.Error != "" {
				fmt.Printf("\r%-60s\r", " ")
				tc.Wred(os.Stdout, "Transfer failed: %s\n", progress.Error)
			} else {
				fmt.Printf("\r%-60s\r", " ")
				tc.Wgreen(os.Stdout, "Complete")
				fmt.Printf(" %s sent\n", humanSize(progress.Size))
			}
			return
		}

		// Print progress
		if progress.Size > 0 {
			pct := float64(progress.Sent) / float64(progress.Size) * 100
			fmt.Printf("\r  %s / %s (%.0f%%)",
				humanSize(progress.Sent), humanSize(progress.Size), pct)
		}
	}
}

// humanSize formats bytes into a human-readable size.
func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
