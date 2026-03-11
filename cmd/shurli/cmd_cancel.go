package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	tc "github.com/shurlinet/shurli/internal/termcolor"
)

func runCancel(args []string) {
	fs := flag.NewFlagSet("cancel", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: shurli cancel <id> [--json]")
		fmt.Println()
		fmt.Println("Cancel a queued or active file transfer.")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  --json  Output as JSON")
		fmt.Println()
		fmt.Println("Use 'shurli transfers' to see transfer IDs.")
		osExit(1)
	}

	client := tryDaemonClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with:")
		fmt.Println("  shurli daemon")
		osExit(1)
	}

	id := remaining[0]

	if err := client.CancelTransfer(id); err != nil {
		if *jsonFlag {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(map[string]string{"error": err.Error()})
		} else {
			fatal("Cancel failed: %v", err)
		}
		return
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]string{"status": "cancelled", "id": id})
	} else {
		tc.Wfaint(os.Stdout, "Cancelled")
		fmt.Printf(" transfer %s\n", id)
	}
}
