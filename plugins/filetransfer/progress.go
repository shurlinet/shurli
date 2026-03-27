package filetransfer

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	tc "github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/pkg/sdk"
)

// humanSize formats bytes into a human-readable string.
// Consolidated from cmd_send.go humanSize() and handlers.go humanSizeAPI().
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

// termWidth returns the terminal width, defaulting to 80 if detection fails.
func termWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return 80
	}
	return w
}

// progressMode describes how progress output should be rendered.
type progressMode int

const (
	progressANSI progressMode = iota // standard TTY: \r + \033[K (erase line)
	progressPlain                     // pipe/file: milestone lines only
)

// detectProgressMode checks the terminal environment.
func detectProgressMode() progressMode {
	fd := int(os.Stdout.Fd())
	if !term.IsTerminal(fd) {
		return progressPlain
	}
	return progressANSI
}

// progressClear returns the escape sequence to clear the current line.
func progressClear() string {
	return "\r\033[K"
}

// progressBarWidth returns the bar width scaled to terminal width.
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
func pollTransferJSON(client *daemonClient, id string) {
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

func pollTransfer(client *daemonClient, id string, quiet bool) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	pmode := detectProgressMode()
	var lastMilestone int
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
					sdk.SanitizeDisplayName(progress.Filename),
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
					sdk.SanitizeDisplayName(progress.Filename), humanSize(progress.Size))
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
				milestone := int(pct*100) / 25 * 25
				if milestone > lastMilestone && milestone > 0 {
					fmt.Printf("  %d%% %s%s%s\n", milestone, speedStr, chunkInfo, compressTag)
					lastMilestone = milestone
				}
			} else {
				fmt.Printf("%s  %s %3.0f%% %s%s%s%s%s", progressClear(), bar, pct*100, speedStr, chunkInfo, compressTag, erasureTag, etaStr)
			}
		}
	}
}
