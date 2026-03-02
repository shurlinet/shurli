package main

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
)

// Set via -ldflags at build time:
//
//	go build -ldflags "-X main.version=0.1.0 -X main.commit=$(git rev-parse --short HEAD) -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o shurli ./cmd/shurli
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
		osExit(1)
	}

	switch os.Args[1] {
	case "init":
		runInit(os.Args[2:])
	case "daemon":
		runDaemon(os.Args[2:])
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
	case "verify":
		runVerify(os.Args[2:])
	case "service":
		runService(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
	case "recover":
		runRecover(os.Args[2:])
	case "change-password":
		runChangePassword(os.Args[2:])
	case "lock":
		runLock(os.Args[2:])
	case "unlock":
		runUnlock(os.Args[2:])
	case "session":
		runSession(os.Args[2:])
	case "doctor":
		runDoctor(os.Args[2:])
	case "completion":
		runCompletion(os.Args[2:])
	case "man":
		runMan()
	case "version", "--version":
		printVersion()
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		osExit(1)
	}
}

func printVersion() {
	fmt.Printf("shurli %s (%s) built %s\n", version, commit, buildDate)
	fmt.Printf("Go %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func printUsage() {
	fmt.Println("Usage: shurli <command> [options]")
	fmt.Println()
	fmt.Println("Daemon:")
	fmt.Println("  daemon                                   Start daemon (P2P host + control API)")
	fmt.Println("  daemon status [--json]                   Query running daemon")
	fmt.Println("  daemon stop                              Graceful shutdown")
	fmt.Println("  daemon ping <target> [-c N] [--json]     Ping via daemon")
	fmt.Println("  daemon services [--json]                 List services via daemon")
	fmt.Println("  daemon peers [--all] [--json]            List connected peers via daemon")
	fmt.Println("  daemon paths [--json]                    Show connection paths")
	fmt.Println("  daemon connect --peer <p> --service <s> --listen <addr>")
	fmt.Println("  daemon disconnect <id>                   Tear down proxy")
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
	fmt.Println("  auth validate [file]                    Validate authorized_keys format")
	fmt.Println()
	fmt.Println("Configuration:")
	fmt.Println("  init                                    Set up shurli configuration")
	fmt.Println("  config validate [--config path]          Validate config")
	fmt.Println("  config show     [--config path]          Show resolved config")
	fmt.Println("  config rollback [--config path]          Restore last-known-good config")
	fmt.Println("  config apply <new> [--confirm-timeout]   Apply with auto-revert")
	fmt.Println("  config confirm  [--config path]          Confirm applied config")
	fmt.Println()
	fmt.Println("Relay client (manages shurli.yaml):")
	fmt.Println("  relay add <address> [--peer-id <ID>]     Add a relay server")
	fmt.Println("  relay list                              List relay servers")
	fmt.Println("  relay remove <multiaddr>                Remove a relay server")
	fmt.Println()
	fmt.Println("Relay server (run on your VPS):")
	fmt.Println("  relay setup                              Initialize relay server config")
	fmt.Println("  relay serve [--config path]              Start the relay server")
	fmt.Println("  relay authorize <peer-id> [comment]      Allow a peer")
	fmt.Println("  relay deauthorize <peer-id>              Remove a peer's access")
	fmt.Println("  relay list-peers                         List authorized peers")
	fmt.Println("  relay info                               Show peer ID and multiaddrs")
	fmt.Println("  relay pair [--count N] [--ttl 1h]        Generate pairing codes")
	fmt.Println("  relay config validate                    Validate relay config")
	fmt.Println("  relay config rollback                    Restore last-known-good config")
	fmt.Println("  relay version                            Show relay version")
	fmt.Println()
	fmt.Println("Relay vault:")
	fmt.Println("  relay vault init [--totp] [--auto-seal]  Initialize vault")
	fmt.Println("  relay vault seal                         Seal vault (watch-only mode)")
	fmt.Println("  relay vault unseal [--remote <addr>]     Unseal vault")
	fmt.Println("  relay vault status                       Show vault seal status")
	fmt.Println("  relay seal                               Shorthand for vault seal")
	fmt.Println("  relay unseal [--remote <addr>]           Shorthand for vault unseal")
	fmt.Println("  relay seal-status                        Shorthand for vault status")
	fmt.Println()
	fmt.Println("Relay invites (macaroon-backed deposits):")
	fmt.Println("  relay invite create [--caveat ...] [--ttl N]")
	fmt.Println("  relay invite list                        List invite deposits")
	fmt.Println("  relay invite revoke <id>                 Revoke a pending invite")
	fmt.Println("  relay invite modify <id> --add-caveat .. Add restrictions")
	fmt.Println()
	fmt.Println("Operator announcements (MOTD / goodbye):")
	fmt.Println("  relay motd set <message> [--remote ..]   Set message of the day")
	fmt.Println("  relay motd clear [--remote ..]           Clear MOTD")
	fmt.Println("  relay motd status [--remote ..]          Show MOTD and goodbye status")
	fmt.Println("  relay goodbye set <message> [--remote ..]  Set goodbye (pushed to peers)")
	fmt.Println("  relay goodbye retract [--remote ..]      Retract goodbye announcement")
	fmt.Println("  relay goodbye shutdown [msg] [--remote ..]  Send goodbye and shut down")
	fmt.Println()
	fmt.Println("ZKP anonymous authentication:")
	fmt.Println("  relay zkp-setup [--keys-dir path]        Generate PLONK circuit keys")
	fmt.Println("  relay zkp-test [--auth-keys path]        End-to-end ZKP auth test")
	fmt.Println()
	fmt.Println("Services:")
	fmt.Println("  service add <name> <address>             Expose a local service")
	fmt.Println("  service remove <name>                    Remove a service")
	fmt.Println("  service enable <name>                    Enable a service")
	fmt.Println("  service disable <name>                   Disable a service")
	fmt.Println("  service list                             List configured services")
	fmt.Println()
	fmt.Println("Pairing:")
	fmt.Println("  invite [--name \"home\"] [--non-interactive]")
	fmt.Println("  join <code> [--name \"laptop\"] [--non-interactive]")
	fmt.Println("  verify <peer>                           Verify a peer's identity (SAS)")
	fmt.Println()
	fmt.Println("Identity security:")
	fmt.Println("  recover [--relay] [--dir path]           Recover identity from seed phrase")
	fmt.Println("  change-password                         Change identity password")
	fmt.Println("  lock                                    Lock daemon (disable sensitive ops)")
	fmt.Println("  unlock                                  Unlock daemon with password")
	fmt.Println("  session refresh                         Rotate session token")
	fmt.Println("  session destroy                         Delete session token")
	fmt.Println()
	fmt.Println("Other:")
	fmt.Println("  status [--config path]                  Show local config and services")
	fmt.Println("  doctor [--fix]                          Check installation health, fix issues")
	fmt.Println("  completion <bash|zsh|fish>              Generate shell completion script")
	fmt.Println("  man                                     Show manual page")
	fmt.Println("  version                                 Show version information")
	fmt.Println()
	fmt.Println("The <target> can be a peer ID or a name from the names section of your config.")
	fmt.Println()
	fmt.Println("All commands support --config <path> to specify a config file.")
	fmt.Println("Without --config, shurli searches: ./shurli.yaml, ~/.config/shurli/config.yaml")
	fmt.Println()
	fmt.Println("Get started:  shurli init")
}
