package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/shurlinet/shurli/internal/daemon"
	tc "github.com/shurlinet/shurli/internal/termcolor"
)

func runNotify(args []string) {
	if len(args) == 0 {
		printNotifyUsage()
		osExit(1)
	}

	switch args[0] {
	case "test":
		runWithJSON(doNotifyTest(args[1:], os.Stdout))
	case "list":
		runWithJSON(doNotifyList(args[1:], os.Stdout))
	default:
		fmt.Fprintf(os.Stderr, "Unknown notify command: %s\n\n", args[0])
		printNotifyUsage()
		osExit(1)
	}
}

func doNotifyTest(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("notify test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	errOut := func(err error) error {
		if *jsonFlag {
			return jsonErr(stdout, err)
		}
		return err
	}

	client, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return errOut(err)
	}

	result, err := client.NotifyTest()
	if err != nil {
		return errOut(err)
	}

	if *jsonFlag {
		return writeJSON(stdout, result)
	}

	tc.Green("Test notification sent to: %s", result["sinks"])
	return nil
}

func doNotifyList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("notify list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	errOut := func(err error) error {
		if *jsonFlag {
			return jsonErr(stdout, err)
		}
		return err
	}

	client, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return errOut(err)
	}

	sinks, err := client.NotifySinks()
	if err != nil {
		return errOut(err)
	}

	if *jsonFlag {
		return writeJSON(stdout, sinks)
	}

	if len(sinks) == 0 {
		tc.Faint("No notification sinks configured.\n")
		return nil
	}

	fmt.Fprintf(stdout, "Notification sinks (%d):\n", len(sinks))
	for _, s := range sinks {
		fmt.Fprintf(stdout, "  %s  [%s]\n", s.Name, s.Status)
	}
	return nil
}

func printNotifyUsage() {
	fmt.Fprintln(os.Stderr, "Usage: shurli notify <command> [--json]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  test    Send a test notification to all configured sinks")
	fmt.Fprintln(os.Stderr, "  list    Show configured notification sinks")
}
