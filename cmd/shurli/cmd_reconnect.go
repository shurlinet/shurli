package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/shurlinet/shurli/internal/daemon"
	"github.com/shurlinet/shurli/internal/termcolor"
)

func runReconnect(args []string) {
	runWithJSON(doReconnect(args, os.Stdout))
}

func doReconnect(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("reconnect", flag.ContinueOnError)
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

	if fs.NArg() != 1 {
		return errOut(fmt.Errorf("usage: shurli reconnect <peer> [--json]"))
	}

	peerName := fs.Arg(0)

	client, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return errOut(err)
	}

	resp, err := client.Reconnect(peerName)
	if err != nil {
		return errOut(err)
	}

	if *jsonFlag {
		return writeJSON(stdout, resp)
	}

	switch resp.Status {
	case "reconnecting":
		termcolor.Green("Reconnecting to %s", resp.Peer)
		fmt.Fprintln(stdout, "  Backoffs cleared. Dial attempt in progress.")
	case "not_watched":
		fmt.Fprintf(stdout, "Peer %s is not in the watchlist (not in authorized_keys).\n", resp.Peer)
		fmt.Fprintln(stdout, "  Swarm backoffs cleared, but PeerManager will not auto-reconnect.")
	default:
		fmt.Fprintf(stdout, "Reconnect status: %s\n", resp.Status)
	}
	return nil
}
