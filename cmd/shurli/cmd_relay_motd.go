package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func runRelayMOTD(args []string, configFile string) {
	if len(args) < 1 {
		printRelayMOTDUsage()
		osExit(1)
	}
	switch args[0] {
	case "set":
		runRelayMOTDSet(args[1:], configFile)
	case "clear":
		runRelayMOTDClear(args[1:], configFile)
	case "status":
		runRelayMOTDStatus(args[1:], configFile)
	default:
		fmt.Fprintf(os.Stderr, "Unknown motd command: %s\n\n", args[0])
		printRelayMOTDUsage()
		osExit(1)
	}
}

func runRelayMOTDSet(args []string, configFile string) {
	if err := doRelayMOTDSet(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayMOTDSet(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay motd set", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: shurli relay motd set <message> [--remote <addr>]")
	}
	message := strings.Join(fs.Args(), " ")

	client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := client.SetMOTD(message); err != nil {
		return fmt.Errorf("failed to set MOTD: %w", err)
	}

	fmt.Fprintln(stdout, "MOTD set. New connections will see this message.")
	return nil
}

func runRelayMOTDClear(args []string, configFile string) {
	if err := doRelayMOTDClear(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayMOTDClear(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay motd clear", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := client.ClearMOTD(); err != nil {
		return fmt.Errorf("failed to clear MOTD: %w", err)
	}

	fmt.Fprintln(stdout, "MOTD cleared.")
	return nil
}

func runRelayMOTDStatus(args []string, configFile string) {
	if err := doRelayMOTDStatus(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayMOTDStatus(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay motd status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
	if err != nil {
		return err
	}
	defer cleanup()

	status, err := client.GetMOTDStatus()
	if err != nil {
		return fmt.Errorf("failed to get MOTD status: %w", err)
	}

	if status.MOTD != "" {
		fmt.Fprintf(stdout, "MOTD:    %s\n", status.MOTD)
	} else {
		fmt.Fprintln(stdout, "MOTD:    (none)")
	}

	if status.GoodbyeActive {
		fmt.Fprintf(stdout, "Goodbye: %s\n", status.Goodbye)
	} else {
		fmt.Fprintln(stdout, "Goodbye: (none)")
	}
	return nil
}

func runRelayGoodbye(args []string, configFile string) {
	if len(args) < 1 {
		printRelayGoodbyeUsage()
		osExit(1)
	}
	switch args[0] {
	case "set":
		runRelayGoodbyeSet(args[1:], configFile)
	case "retract":
		runRelayGoodbyeRetract(args[1:], configFile)
	case "shutdown":
		runRelayGoodbyeShutdown(args[1:], configFile)
	case "status":
		// Reuse MOTD status (shows both)
		runRelayMOTDStatus(args[1:], configFile)
	default:
		fmt.Fprintf(os.Stderr, "Unknown goodbye command: %s\n\n", args[0])
		printRelayGoodbyeUsage()
		osExit(1)
	}
}

func runRelayGoodbyeSet(args []string, configFile string) {
	if err := doRelayGoodbyeSet(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayGoodbyeSet(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay goodbye set", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: shurli relay goodbye set <message> [--remote <addr>]")
	}
	message := strings.Join(fs.Args(), " ")

	client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := client.SetGoodbye(message); err != nil {
		return fmt.Errorf("failed to set goodbye: %w", err)
	}

	fmt.Fprintln(stdout, "Goodbye set and pushed to all connected peers.")
	fmt.Fprintln(stdout, "Peers will cache this message and show it on reconnect attempts.")
	return nil
}

func runRelayGoodbyeRetract(args []string, configFile string) {
	if err := doRelayGoodbyeRetract(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayGoodbyeRetract(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay goodbye retract", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := client.RetractGoodbye(); err != nil {
		return fmt.Errorf("failed to retract goodbye: %w", err)
	}

	fmt.Fprintln(stdout, "Goodbye retracted. Peers will clear their cached goodbye.")
	return nil
}

func runRelayGoodbyeShutdown(args []string, configFile string) {
	if err := doRelayGoodbyeShutdown(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayGoodbyeShutdown(args []string, configFile string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay goodbye shutdown", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remoteFlag := fs.String("remote", "", "relay multiaddr for remote P2P admin")
	if err := fs.Parse(args); err != nil {
		return err
	}

	message := "Relay shutting down"
	if fs.NArg() > 0 {
		message = strings.Join(fs.Args(), " ")
	}

	client, cleanup, err := relayAdminClientOrRemote(*remoteFlag, configFile)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := client.GoodbyeShutdown(message); err != nil {
		return fmt.Errorf("goodbye shutdown failed: %w", err)
	}

	fmt.Fprintln(stdout, "Goodbye sent to all peers. Relay will shut down shortly.")
	return nil
}

func printRelayMOTDUsage() {
	fmt.Println("Usage: shurli relay motd <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  set     <message>  [--remote <addr>]   Set relay MOTD (shown to connecting peers)")
	fmt.Println("  clear              [--remote <addr>]   Clear relay MOTD")
	fmt.Println("  status             [--remote <addr>]   Show current MOTD and goodbye status")
	fmt.Println()
	fmt.Println("The MOTD is a short message shown to peers when they connect to the relay.")
	fmt.Println("Messages are signed by the relay's identity key and verified by clients.")
}

func printRelayGoodbyeUsage() {
	fmt.Println("Usage: shurli relay goodbye <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  set      <message>  [--remote <addr>]   Set goodbye (pushed to all peers)")
	fmt.Println("  retract             [--remote <addr>]   Retract a goodbye announcement")
	fmt.Println("  shutdown [message]  [--remote <addr>]   Send goodbye and shut down relay")
	fmt.Println("  status              [--remote <addr>]   Show current MOTD and goodbye status")
	fmt.Println()
	fmt.Println("Goodbye messages are signed, persistent announcements that survive restarts.")
	fmt.Println("Clients cache goodbye messages and show them on reconnect attempts.")
	fmt.Println("Use 'shutdown' for planned relay decommission with a clean handoff.")
}
