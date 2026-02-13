package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "init":
		runInit(os.Args[2:])
	case "serve":
		runServe(os.Args[2:])
	case "proxy":
		runProxy(os.Args[2:])
	case "ping":
		runPing(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: peerup <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  init                                    Set up peerup configuration")
	fmt.Println("  serve                                   Run as a server (expose services)")
	fmt.Println("  proxy <target> <service> <local-port>   Forward a local TCP port to a remote service")
	fmt.Println("  ping  <target>                          Send a ping to a remote peer")
	fmt.Println()
	fmt.Println("The <target> can be a peer ID or a name from the names section of your config.")
	fmt.Println()
	fmt.Println("All commands support --config <path> to specify a config file.")
	fmt.Println("Without --config, peerup searches: ./peerup.yaml, ~/.config/peerup/config.yaml")
	fmt.Println()
	fmt.Println("Get started:  peerup init")
}
