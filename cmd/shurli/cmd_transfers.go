package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/shurlinet/shurli/internal/daemon"
	tc "github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

func runTransfers(args []string) {
	fs := flag.NewFlagSet("transfers", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	watchFlag := fs.Bool("watch", false, "live feed (refreshes every 2s)")
	historyFlag := fs.Bool("history", false, "show transfer event history from log")
	maxFlag := fs.Int("max", 50, "max events to show with --history")
	fs.Parse(reorderFlags(fs, args))

	client := tryDaemonClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with:")
		fmt.Println("  shurli daemon")
		osExit(1)
	}

	if *historyFlag {
		showTransferHistory(client, *maxFlag, *jsonFlag)
		return
	}

	if *watchFlag {
		watchTransfers(client, *jsonFlag)
		return
	}

	transfers, err := client.TransferList()
	if err != nil {
		fatal("Failed to list transfers: %v", err)
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(transfers)
		return
	}

	if len(transfers) == 0 {
		tc.Wfaint(os.Stdout, "No transfers.\n")
		return
	}

	printTransferTable(transfers)
}

func showTransferHistory(client *daemon.Client, max int, jsonOutput bool) {
	events, err := client.TransferHistory(max)
	if err != nil {
		fatal("Failed to get transfer history: %v", err)
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(events)
		return
	}

	if len(events) == 0 {
		tc.Wfaint(os.Stdout, "No transfer history.\n")
		return
	}

	for _, ev := range events {
		ts := ev.Timestamp.Local().Format("2006-01-02 15:04:05")
		dir := "\u2191"
		if ev.Direction == "receive" {
			dir = "\u2193"
		}

		sizeStr := ""
		if ev.FileSize > 0 {
			sizeStr = humanSize(ev.FileSize)
		}

		fmt.Printf("  %s  %s %s  %-18s  %s", ts, dir, ev.FileName, ev.EventType, sizeStr)

		if ev.Duration != "" {
			fmt.Printf("  %s", ev.Duration)
		}
		if ev.Error != "" {
			fmt.Printf("  ")
			tc.Wred(os.Stdout, "%s", ev.Error)
		}

		peerShort := ev.PeerID
		if len(peerShort) > 16 {
			peerShort = peerShort[:16] + "..."
		}
		fmt.Printf("  %s", peerShort)
		fmt.Println()
	}
}

func printTransferTable(transfers []p2pnet.TransferProgress) {
	// Sort: active first, then by start time descending.
	sort.Slice(transfers, func(i, j int) bool {
		if transfers[i].Done != transfers[j].Done {
			return !transfers[i].Done // active first
		}
		return transfers[i].StartTime.After(transfers[j].StartTime)
	})

	for i := range transfers {
		t := &transfers[i]
		dir := "\u2191" // up arrow for send
		if t.Direction == "receive" {
			dir = "\u2193" // down arrow for receive
		}

		peerShort := t.PeerID
		if len(peerShort) > 16 {
			peerShort = peerShort[:16] + "..."
		}

		pctStr := ""
		if t.Size > 0 && !t.Done {
			pct := float64(t.Transferred) / float64(t.Size) * 100
			pctStr = fmt.Sprintf(" %.0f%%", pct)
		}

		compressTag := ""
		if t.Compressed {
			compressTag = " [zstd]"
		}

		age := time.Since(t.StartTime).Truncate(time.Second)

		fmt.Printf("  %s %s  %s  %s  %s/%s%s%s  ",
			dir,
			t.ID,
			t.Filename,
			peerShort,
			humanSize(t.Transferred), humanSize(t.Size),
			pctStr,
			compressTag,
		)

		// Print status with color.
		switch t.Status {
		case "complete":
			tc.Wgreen(os.Stdout, "complete")
		case "failed":
			tc.Wred(os.Stdout, "failed")
		case "active":
			tc.Wyellow(os.Stdout, "active")
		case "pending":
			tc.Wfaint(os.Stdout, "pending")
		default:
			fmt.Print(t.Status)
		}

		fmt.Printf("  %s\n", age)

		if t.Error != "" {
			tc.Wred(os.Stdout, "    error: %s\n", t.Error)
		}
	}
}

func watchTransfers(client *daemon.Client, jsonOutput bool) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Print once immediately.
	printWatchRound(client, jsonOutput)

	for range ticker.C {
		// Clear screen (ANSI).
		fmt.Print("\033[2J\033[H")
		printWatchRound(client, jsonOutput)
	}
}

func printWatchRound(client *daemon.Client, jsonOutput bool) {
	transfers, err := client.TransferList()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		return
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(transfers)
		return
	}

	tc.Wfaint(os.Stdout, "Transfers (live, Ctrl+C to exit)  %s\n\n",
		time.Now().Format("15:04:05"))

	if len(transfers) == 0 {
		tc.Wfaint(os.Stdout, "No transfers.\n")
		return
	}

	printTransferTable(transfers)
}
