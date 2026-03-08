package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shurlinet/shurli/internal/daemon"
	tc "github.com/shurlinet/shurli/internal/termcolor"
)

func runSend(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	followFlag := fs.Bool("follow", false, "follow transfer progress inline")
	noCompressFlag := fs.Bool("no-compress", false, "disable zstd compression")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 2 {
		fmt.Println("Usage: shurli send <file> <peer> [--follow] [--no-compress] [--json]")
		fmt.Println()
		fmt.Println("Send a file to a peer. By default, submits to daemon and exits.")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  --follow       Follow transfer progress (Ctrl+C detaches, transfer continues)")
		fmt.Println("  --no-compress  Disable zstd compression")
		fmt.Println("  --json         Output as JSON")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  shurli send photo.jpg home-server              # fire-and-forget")
		fmt.Println("  shurli send photo.jpg home-server --follow     # watch progress")
		fmt.Println("  shurli send ~/Documents/report.pdf laptop")
		fmt.Println("  shurli send backup.tar.gz 12D3KooW...")
		fmt.Println()
		fmt.Println("Check status anytime with: shurli transfers")
		osExit(1)
	}

	filePath := remaining[0]
	peer := remaining[1]

	if peer == "--to" {
		if len(remaining) < 3 {
			fatal("Missing peer after --to")
		}
		peer = remaining[2]
	}

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		fatal("Invalid path: %v", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		fatal("Cannot access file: %v", err)
	}
	if info.IsDir() {
		fatal("Cannot send directory (directory transfer is Phase E)")
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

	resp, err := client.Send(absPath, peer, *noCompressFlag)
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

	if !*followFlag {
		tc.Wfaint(os.Stdout, "Transfer continues in daemon. Check: shurli transfers\n")
		return
	}

	pollTransfer(client, resp.TransferID)
}

const progressBarWidth = 30

func pollTransfer(client *daemon.Client, id string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastSent int64
	lastTime := time.Now()

	for range ticker.C {
		progress, err := client.TransferStatus(id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cannot check transfer status: %v\n", err)
			return
		}

		if progress.Done {
			fmt.Printf("\r%-70s\r", " ")
			if progress.Error != "" {
				tc.Wred(os.Stdout, "Transfer failed: %s\n", progress.Error)
			} else {
				bar := strings.Repeat("\u2588", progressBarWidth)
				tc.Wgreen(os.Stdout, "%s 100%%\n", bar)
				fmt.Println("Transfer complete")
			}
			return
		}

		if progress.Size > 0 {
			pct := float64(progress.Transferred) / float64(progress.Size)
			filled := int(pct * float64(progressBarWidth))
			if filled > progressBarWidth {
				filled = progressBarWidth
			}
			bar := strings.Repeat("\u2588", filled) + strings.Repeat("\u2591", progressBarWidth-filled)

			now := time.Now()
			elapsed := now.Sub(lastTime).Seconds()
			var speedStr string
			if elapsed > 0 && progress.Transferred > lastSent {
				speed := float64(progress.Transferred-lastSent) / elapsed
				speedStr = humanSize(int64(speed)) + "/s"
			}
			lastSent = progress.Transferred
			lastTime = now

			chunkInfo := ""
			if progress.ChunksTotal > 0 {
				chunkInfo = fmt.Sprintf(" [%d/%d chunks]", progress.ChunksDone, progress.ChunksTotal)
			}

			if speedStr != "" {
				fmt.Printf("\r%s %.0f%% - %s%s   ", bar, pct*100, speedStr, chunkInfo)
			} else {
				fmt.Printf("\r%s %.0f%%%s   ", bar, pct*100, chunkInfo)
			}
		}
	}
}

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
