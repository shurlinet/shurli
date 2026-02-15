package main

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
)

// Set via -ldflags at build time:
//
//	go build -ldflags "-X main.version=0.1.0 -X main.commit=$(git rev-parse --short HEAD) -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o peerup ./cmd/peerup
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

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
	case "whoami":
		runWhoami(os.Args[2:])
	case "auth":
		runAuth(os.Args[2:])
	case "relay":
		runRelay(os.Args[2:])
	case "config":
		runConfig(os.Args[2:])
	case "invite":
		runInvite(os.Args[2:])
	case "join":
		runJoin(os.Args[2:])
	case "version", "--version":
		printVersion()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printVersion() {
	fmt.Printf("peerup %s (%s) built %s\n", version, commit, buildDate)
	fmt.Printf("Go %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
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
	fmt.Println("  whoami                                  Show your peer ID")
	fmt.Println("  auth add <peer-id> [--comment \"...\"]    Authorize a peer")
	fmt.Println("  auth list                               List authorized peers")
	fmt.Println("  auth remove <peer-id>                   Revoke a peer's access")
	fmt.Println()
	fmt.Println("  relay add <address> [--peer-id <ID>]     Add a relay server address")
	fmt.Println("  relay list                              List configured relay addresses")
	fmt.Println("  relay remove <multiaddr>                Remove a relay server address")
	fmt.Println()
	fmt.Println("  config validate [--config path]          Validate config without starting")
	fmt.Println("  config show     [--config path]          Show resolved config")
	fmt.Println("  config rollback [--config path]          Restore last-known-good config")
	fmt.Println("  config apply <new> [--confirm-timeout]   Apply config with auto-revert safety")
	fmt.Println("  config confirm  [--config path]          Confirm applied config")
	fmt.Println()
	fmt.Println("  invite [--name \"home\"]                   Generate an invite code for pairing")
	fmt.Println("  join <code> [--name \"laptop\"]            Accept an invite and auto-configure")
	fmt.Println()
	fmt.Println("  version                                 Show version information")
	fmt.Println()
	fmt.Println("The <target> can be a peer ID or a name from the names section of your config.")
	fmt.Println()
	fmt.Println("All commands support --config <path> to specify a config file.")
	fmt.Println("Without --config, peerup searches: ./peerup.yaml, ~/.config/peerup/config.yaml")
	fmt.Println()
	fmt.Println("Get started:  peerup init")
}
