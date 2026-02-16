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
	case "daemon":
		runDaemon(os.Args[2:])
	case "serve":
		runServe(os.Args[2:])
	case "proxy":
		runProxy(os.Args[2:])
	case "ping":
		runPing(os.Args[2:])
	case "traceroute":
		runTraceroute(os.Args[2:])
	case "resolve":
		runResolve(os.Args[2:])
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
	case "service":
		runService(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
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
	fmt.Println("Daemon:")
	fmt.Println("  daemon                                   Start daemon (P2P host + control API)")
	fmt.Println("  daemon status [--json]                   Query running daemon")
	fmt.Println("  daemon stop                              Graceful shutdown")
	fmt.Println("  daemon ping <target> [-c N] [--json]     Ping via daemon")
	fmt.Println("  daemon services [--json]                 List services via daemon")
	fmt.Println("  daemon peers [--all] [--json]            List connected peers via daemon")
	fmt.Println("  daemon connect --peer <p> --service <s> --listen <addr>")
	fmt.Println("  daemon disconnect <id>                   Tear down proxy")
	fmt.Println("  serve                                    Alias for daemon")
	fmt.Println()
	fmt.Println("Network tools (standalone, no daemon required):")
	fmt.Println("  ping <target> [-c N] [--interval 1s] [--json]  P2P ping")
	fmt.Println("  traceroute <target> [--json]                    P2P traceroute")
	fmt.Println("  resolve <name> [--json]                         Resolve name to peer ID")
	fmt.Println("  proxy <target> <service> <local-port>           Forward TCP port")
	fmt.Println()
	fmt.Println("Identity & access:")
	fmt.Println("  whoami                                  Show your peer ID")
	fmt.Println("  auth add <peer-id> [--comment \"...\"]    Authorize a peer")
	fmt.Println("  auth list                               List authorized peers")
	fmt.Println("  auth remove <peer-id>                   Revoke a peer's access")
	fmt.Println()
	fmt.Println("Configuration:")
	fmt.Println("  init                                    Set up peerup configuration")
	fmt.Println("  config validate [--config path]          Validate config")
	fmt.Println("  config show     [--config path]          Show resolved config")
	fmt.Println("  config rollback [--config path]          Restore last-known-good config")
	fmt.Println("  config apply <new> [--confirm-timeout]   Apply with auto-revert")
	fmt.Println("  config confirm  [--config path]          Confirm applied config")
	fmt.Println()
	fmt.Println("  relay add <address> [--peer-id <ID>]     Add a relay server")
	fmt.Println("  relay list                              List relay servers")
	fmt.Println("  relay remove <multiaddr>                Remove a relay server")
	fmt.Println()
	fmt.Println("  service add <name> <address>             Expose a local service")
	fmt.Println("  service remove <name>                    Remove a service")
	fmt.Println("  service enable <name>                    Enable a service")
	fmt.Println("  service disable <name>                   Disable a service")
	fmt.Println("  service list                             List configured services")
	fmt.Println()
	fmt.Println("Pairing:")
	fmt.Println("  invite [--name \"home\"] [--non-interactive]")
	fmt.Println("  join <code> [--name \"laptop\"] [--non-interactive]")
	fmt.Println()
	fmt.Println("  status [--config path]                  Show local config and services")
	fmt.Println("  version                                 Show version information")
	fmt.Println()
	fmt.Println("The <target> can be a peer ID or a name from the names section of your config.")
	fmt.Println()
	fmt.Println("All commands support --config <path> to specify a config file.")
	fmt.Println("Without --config, peerup searches: ./peerup.yaml, ~/.config/peerup/config.yaml")
	fmt.Println()
	fmt.Println("Get started:  peerup init")
}
