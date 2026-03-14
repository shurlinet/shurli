package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func runClean(args []string) {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(reorderFlags(fs, args))

	client := tryDaemonClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with:")
		fmt.Println("  shurli daemon")
		osExit(1)
	}

	count, bytes, err := client.CleanTempFiles()
	if err != nil {
		if *jsonFlag {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(map[string]string{"error": err.Error()})
		} else {
			fatal("Clean failed: %v", err)
		}
		return
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{
			"files_removed": count,
			"bytes_freed":   bytes,
		})
	} else {
		if count == 0 {
			fmt.Println("No temporary files to clean.")
		} else {
			fmt.Printf("Cleaned %d temp file(s), freed %s\n", count, humanSize(bytes))
		}
	}
}
