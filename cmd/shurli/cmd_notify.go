package main

import (
	"fmt"
	"os"

	tc "github.com/shurlinet/shurli/internal/termcolor"
)

func runNotify(args []string) {
	if len(args) == 0 {
		printNotifyUsage()
		osExit(1)
	}

	switch args[0] {
	case "test":
		runNotifyTest(args[1:])
	case "list":
		runNotifyList(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown notify command: %s\n\n", args[0])
		printNotifyUsage()
		osExit(1)
	}
}

func runNotifyTest(_ []string) {
	c := daemonClient()
	result, err := c.NotifyTest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}

	tc.Green("Test notification sent to: %s", result["sinks"])
}

func runNotifyList(_ []string) {
	c := daemonClient()
	sinks, err := c.NotifySinks()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}

	if len(sinks) == 0 {
		tc.Faint("No notification sinks configured.\n")
		return
	}

	fmt.Printf("Notification sinks (%d):\n", len(sinks))
	for _, s := range sinks {
		fmt.Printf("  %s\n", s.Name)
	}
}

func printNotifyUsage() {
	fmt.Fprintln(os.Stderr, "Usage: shurli notify <command>")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  test    Send a test notification to all configured sinks")
	fmt.Fprintln(os.Stderr, "  list    Show configured notification sinks")
}
