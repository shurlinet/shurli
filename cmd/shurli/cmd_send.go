package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/shurlinet/shurli/internal/daemon"
	tc "github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

func runSend(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	followFlag := fs.Bool("follow", false, "follow transfer progress inline")
	noCompressFlag := fs.Bool("no-compress", false, "disable zstd compression")
	streamsFlag := fs.Int("streams", 0, "parallel stream count (0 = auto)")
	priorityFlag := fs.String("priority", "normal", "queue priority: low, normal, high")
	quietFlag := fs.Bool("quiet", false, "show only a single progress bar")
	silentFlag := fs.Bool("silent", false, "no progress output")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if len(remaining) < 2 {
		fmt.Println("Usage: shurli send <file|dir> <peer> [--follow] [--no-compress] [--streams N] [--json]")
		fmt.Println()
		fmt.Println("Send a file or directory to a peer. By default, submits to daemon and exits.")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  --follow       Follow transfer progress (Ctrl+C detaches, transfer continues)")
		fmt.Println("  --no-compress  Disable zstd compression")
		fmt.Println("  --streams N    Parallel stream count (0 = auto, default)")
		fmt.Println("  --priority P   Queue priority: low, normal (default), high")
		fmt.Println("  --quiet        Show only a single progress bar (no per-chunk details)")
		fmt.Println("  --silent       No progress output at all")
		fmt.Println("  --json         Output as JSON (contains peer IDs and filenames; treat as untrusted in pipelines)")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  shurli send photo.jpg home-server              # fire-and-forget")
		fmt.Println("  shurli send photo.jpg home-server --follow     # watch progress")
		fmt.Println("  shurli send ~/Documents/report.pdf laptop")
		fmt.Println("  shurli send mydir/ home-server                 # send entire directory")
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

	client := tryDaemonClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with:")
		fmt.Println("  shurli daemon")
		osExit(1)
	}

	if info.IsDir() {
		if !*jsonFlag {
			tc.Wfaint(os.Stdout, "Sending directory %s to %s...\n",
				filepath.Base(absPath), peer)
		}
	} else {
		if !*jsonFlag {
			tc.Wfaint(os.Stdout, "Sending %s (%s) to %s...\n",
				filepath.Base(absPath), humanSize(info.Size()), peer)
		}
	}

	resp, err := client.Send(absPath, peer, *noCompressFlag, *streamsFlag, *priorityFlag)
	if err != nil {
		fatal("Send failed: %v", err)
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		if *followFlag {
			// Stream JSON progress events (one JSON object per line, NDJSON).
			enc.Encode(map[string]any{
				"event":       "started",
				"transfer_id": resp.TransferID,
				"filename":    resp.Filename,
				"size":        resp.Size,
				"peer_id":     resp.PeerID,
			})
			pollTransferJSON(client, resp.TransferID)
		} else {
			enc.SetIndent("", "  ")
			enc.Encode(resp)
		}
		return
	}

	tc.Wgreen(os.Stdout, "Transfer started")
	fmt.Printf(" [%s]\n", resp.TransferID)

	if !*followFlag || *silentFlag {
		if !*silentFlag {
			tc.Wfaint(os.Stdout, "Transfer continues in daemon. Check: shurli transfers\n")
		}
		return
	}

	pollTransfer(client, resp.TransferID, *quietFlag)
}

// progressMode describes how progress output should be rendered based on the
// terminal environment. Detected once and cached.
type progressMode int

const (
	progressANSI     progressMode = iota // standard TTY: \r + \033[K (erase line)
	progressPlain                        // pipe/file: milestone lines only (25%, 50%, 75%, 100%)
)

// detectProgressMode checks the terminal environment and returns the
// appropriate progress rendering mode.
//
// - Standard TTY (including tmux, screen, SSH): ANSI mode with \r\033[K
// - Piped output or non-TTY: plain milestone lines
func detectProgressMode() progressMode {
	fd := int(os.Stdout.Fd())
	if !term.IsTerminal(fd) {
		return progressPlain
	}
	return progressANSI
}

// progressClear returns the escape sequence to clear the current line for
// in-place progress updates. For ANSI terminals (including tmux/screen/SSH),
// this is \r\033[K (carriage return + erase to end of line).
func progressClear() string {
	return "\r\033[K"
}

// progressBarWidth returns the bar width scaled to terminal width.
// Reserves space for percentage, speed, chunk info, and padding.
// Minimum 10, maximum 60.
func progressBarWidth() int {
	w := termWidth()
	barW := w - 50
	if barW < 10 {
		barW = 10
	}
	if barW > 60 {
		barW = 60
	}
	return barW
}

// pollTransferJSON streams NDJSON progress events for --follow --json mode.
// Emits: started (once), progress (every 500ms), completed/failed (once).
// JSON output may contain peer IDs and filenames - treat as untrusted in automated pipelines.
func pollTransferJSON(client *daemon.Client, id string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	enc := json.NewEncoder(os.Stdout)

	for range ticker.C {
		progress, err := client.TransferStatus(id)
		if err != nil {
			enc.Encode(map[string]any{"event": "error", "error": err.Error()})
			return
		}

		if progress.Done {
			ev := map[string]any{
				"event":           "completed",
				"transfer_id":     progress.ID,
				"transferred":     progress.Transferred,
				"size":            progress.Size,
				"compressed_size": progress.CompressedSize,
			}
			if progress.Error != "" {
				ev["event"] = "failed"
				ev["error"] = progress.Error
			}
			enc.Encode(ev)
			return
		}

		ev := map[string]any{
			"event":           "progress",
			"transfer_id":     progress.ID,
			"transferred":     progress.Transferred,
			"size":            progress.Size,
			"chunks_done":     progress.ChunksDone,
			"chunks_total":    progress.ChunksTotal,
			"compressed_size": progress.CompressedSize,
		}
		if len(progress.StreamProgress) > 0 {
			ev["stream_progress"] = progress.StreamProgress
		}
		enc.Encode(ev)
	}
}

func pollTransfer(client *daemon.Client, id string, quiet bool) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	pmode := detectProgressMode()
	var lastMilestone int // for plain mode: track last printed milestone (25, 50, 75)
	var lastSent int64
	var headerPrinted bool
	var speedHistory []float64
	var stallCount int
	lastTime := time.Now()
	startTime := time.Now()

	for range ticker.C {
		progress, err := client.TransferStatus(id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cannot check transfer status: %v\n", err)
			return
		}

		if progress.Done {
			fmt.Print(progressClear())
			if progress.Error != "" {
				tc.Wred(os.Stdout, "Transfer failed: %s\n", progress.Error)
			} else {
				bar := strings.Repeat("\u2588", progressBarWidth())
				tc.Wgreen(os.Stdout, "%s 100%%\n", bar)

				dur := time.Since(startTime).Truncate(time.Millisecond)
				avgSpeed := float64(0)
				if dur.Seconds() > 0 {
					avgSpeed = float64(progress.Size) / dur.Seconds()
				}
				fmt.Printf("  %s  %s in %s (%s/s avg)",
					p2pnet.SanitizeDisplayName(progress.Filename),
					humanSize(progress.Size), dur, humanSize(int64(avgSpeed)))
				if progress.Compressed && progress.CompressedSize > 0 {
					ratio := float64(progress.Size) / float64(progress.CompressedSize)
					fmt.Printf("  [zstd %.1f:1, %s wire]", ratio, humanSize(progress.CompressedSize))
				}
				if len(progress.StreamProgress) > 1 {
					fmt.Printf("  [%d streams]", len(progress.StreamProgress))
				}
				fmt.Println()
			}
			return
		}

		if progress.Size > 0 {
			if !headerPrinted {
				tc.Wfaint(os.Stdout, "  File: %s (%s)\n",
					p2pnet.SanitizeDisplayName(progress.Filename), humanSize(progress.Size))
				mode := "1 stream"
				if len(progress.StreamProgress) > 1 {
					mode = fmt.Sprintf("%d streams", len(progress.StreamProgress))
				}
				if progress.Compressed {
					mode += ", zstd"
				}
				if progress.ErasureParity > 0 {
					mode += fmt.Sprintf(", RS %.0f%% parity", progress.ErasureOverhead*100)
				}
				tc.Wfaint(os.Stdout, "  Mode: %s\n", mode)
				headerPrinted = true
			}

			pct := float64(progress.Transferred) / float64(progress.Size)
			barW := progressBarWidth()
			filled := int(pct * float64(barW))
			if filled > barW {
				filled = barW
			}
			bar := strings.Repeat("\u2588", filled) + strings.Repeat("\u2591", barW-filled)

			now := time.Now()
			elapsed := now.Sub(lastTime).Seconds()
			var speed float64
			var speedStr string
			if elapsed > 0 && progress.Transferred > lastSent {
				speed = float64(progress.Transferred-lastSent) / elapsed
				speedStr = humanSize(int64(speed)) + "/s"
				stallCount = 0
			} else {
				stallCount++
			}
			lastSent = progress.Transferred
			lastTime = now

			if speed > 0 {
				speedHistory = append(speedHistory, speed)
				if len(speedHistory) > 5 {
					speedHistory = speedHistory[len(speedHistory)-5:]
				}
			}

			etaStr := ""
			remaining := progress.Size - progress.Transferred
			if stallCount >= 3 {
				etaStr = " ETA stalled"
			} else if len(speedHistory) >= 2 && remaining > 0 {
				var avgSpeed float64
				for _, s := range speedHistory {
					avgSpeed += s
				}
				avgSpeed /= float64(len(speedHistory))
				if avgSpeed > 0 {
					etaSec := float64(remaining) / avgSpeed
					if etaSec >= 2 {
						etaStr = fmt.Sprintf(" ETA %s", (time.Duration(etaSec) * time.Second).Truncate(time.Second))
					}
				}
			}

			chunkInfo := ""
			if !quiet && progress.ChunksTotal > 0 {
				chunkInfo = fmt.Sprintf(" [%d/%d chunks]", progress.ChunksDone, progress.ChunksTotal)
			}

			compressTag := ""
			if progress.Compressed && progress.CompressedSize > 0 && progress.Size > 0 {
				ratio := float64(progress.Size) / float64(progress.CompressedSize)
				compressTag = fmt.Sprintf(" [zstd %.1f:1]", ratio)
			} else if progress.Compressed {
				compressTag = " [zstd]"
			}

			erasureTag := ""
			if progress.ErasureParity > 0 {
				erasureTag = fmt.Sprintf(" [RS %.0f%% parity, %d chunks]",
					progress.ErasureOverhead*100, progress.ErasureParity)
			}

			if pmode == progressPlain {
				// Non-TTY (piped/redirected): print milestone lines only.
				milestone := int(pct*100) / 25 * 25
				if milestone > lastMilestone && milestone > 0 {
					fmt.Printf("  %d%% %s%s%s\n", milestone, speedStr, chunkInfo, compressTag)
					lastMilestone = milestone
				}
			} else {
				// TTY (including tmux, screen, SSH): \r\033[K clears line first, then draws.
				fmt.Printf("%s  %s %3.0f%% %s%s%s%s%s", progressClear(), bar, pct*100, speedStr, chunkInfo, compressTag, erasureTag, etaStr)
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
