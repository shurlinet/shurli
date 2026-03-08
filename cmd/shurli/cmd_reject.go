package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	tc "github.com/shurlinet/shurli/internal/termcolor"
)

func runReject(args []string) {
	fs := flag.NewFlagSet("reject", flag.ExitOnError)
	reasonFlag := fs.String("reason", "", "reject reason: space, busy, or size")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: shurli reject <id> [--reason space|busy|size] [--json]")
		fmt.Println()
		fmt.Println("Reject a pending incoming file transfer (ask mode).")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  --reason <r>  Announce reject reason to sender (space, busy, size)")
		fmt.Println("                Without --reason, sender sees generic 'declined'")
		fmt.Println("  --json        Output as JSON")
		fmt.Println()
		fmt.Println("Use 'shurli transfers' to see pending transfers.")
		osExit(1)
	}

	id := remaining[0]

	// Validate reason flag.
	reason := *reasonFlag
	if reason != "" && reason != "space" && reason != "busy" && reason != "size" {
		fatal("Invalid reason %q. Must be: space, busy, or size", reason)
	}

	client := tryDaemonClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with:")
		fmt.Println("  shurli daemon")
		osExit(1)
	}

	if err := client.TransferReject(id, reason); err != nil {
		if *jsonFlag {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(map[string]string{"error": err.Error()})
		} else {
			fatal("Reject failed: %v", err)
		}
		return
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]string{"status": "rejected", "id": id, "reason": reason})
	} else {
		tc.Wfaint(os.Stdout, "Rejected")
		if reason != "" {
			fmt.Printf(" transfer %s (reason: %s)\n", id, reason)
		} else {
			fmt.Printf(" transfer %s\n", id)
		}
	}
}
