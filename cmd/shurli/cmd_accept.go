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
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: shurli accept <id> [--dest /path/] [--json]")
		fmt.Println()
		fmt.Println("Accept a pending incoming file transfer (ask mode).")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  --dest <path>  Save to a specific directory instead of the default")
		fmt.Println("  --json         Output as JSON")
		fmt.Println()
		fmt.Println("Use 'shurli transfers' to see pending transfers.")
		osExit(1)
	}

	id := remaining[0]

	client := tryDaemonClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with:")
		fmt.Println("  shurli daemon")
		osExit(1)
	}

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
