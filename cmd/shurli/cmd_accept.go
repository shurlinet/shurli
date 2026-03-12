package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	tc "github.com/shurlinet/shurli/internal/termcolor"
)

func runAccept(args []string) {
	fs := flag.NewFlagSet("accept", flag.ExitOnError)
	destFlag := fs.String("dest", "", "accept to a specific directory")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	allFlag := fs.Bool("all", false, "accept all pending transfers")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if !*allFlag && len(remaining) < 1 {
		fmt.Println("Usage: shurli accept <id> [--dest /path/] [--json]")
		fmt.Println("       shurli accept --all [--dest /path/] [--json]")
		fmt.Println()
		fmt.Println("Accept a pending incoming file transfer (ask mode).")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  --all          Accept all pending transfers")
		fmt.Println("  --dest <path>  Save to a specific directory instead of the default")
		fmt.Println("  --json         Output as JSON")
		fmt.Println()
		fmt.Println("Use 'shurli transfers' to see pending transfers.")
		osExit(1)
	}

	client := tryDaemonClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with:")
		fmt.Println("  shurli daemon")
		osExit(1)
	}

	if *allFlag {
		pending, err := client.TransferPending()
		if err != nil {
			fatal("Failed to list pending transfers: %v", err)
		}
		if len(pending) == 0 {
			tc.Wfaint(os.Stdout, "No pending transfers.\n")
			return
		}
		for _, p := range pending {
			if err := client.TransferAccept(p.ID, *destFlag); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: accept %s failed: %v\n", p.ID, err)
				continue
			}
			if *jsonFlag {
				enc := json.NewEncoder(os.Stdout)
				enc.Encode(map[string]string{"status": "accepted", "id": p.ID})
			} else {
				tc.Wgreen(os.Stdout, "Accepted")
				fmt.Printf(" %s (%s from %s)\n", p.ID, p.Filename, p.PeerID)
			}
		}
		return
	}

	id := remaining[0]

	if err := client.TransferAccept(id, *destFlag); err != nil {
		if *jsonFlag {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(map[string]string{"error": err.Error()})
		} else {
			fatal("Accept failed: %v", err)
		}
		return
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]string{"status": "accepted", "id": id})
	} else {
		tc.Wgreen(os.Stdout, "Accepted")
		fmt.Printf(" transfer %s\n", id)
	}
}
