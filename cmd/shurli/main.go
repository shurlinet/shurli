package main

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"

	"github.com/shurlinet/shurli/internal/config"
	tc "github.com/shurlinet/shurli/internal/termcolor"
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

	// Apply cli.color config setting (best-effort, no error on missing config).
	applyColorConfig()

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
	case "share":
		runShare(os.Args[2:])
	case "browse":
		runBrowse(os.Args[2:])
	case "download":
		runDownload(os.Args[2:])
	case "send":
		runSend(os.Args[2:])
	case "transfers":
		runTransfers(os.Args[2:])
	case "accept":
		runAccept(os.Args[2:])
	case "reject":
		runReject(os.Args[2:])
	case "cancel":
		runCancel(os.Args[2:])
	case "clean":
		runClean(os.Args[2:])
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

// applyColorConfig checks the config file for cli.color setting.
// Best-effort: silently skips if no config is found (e.g., before init).
// Tries node config first, then relay config.
func applyColorConfig() {
	// Try node config.
	if cfgFile, err := config.FindConfigFile(""); err == nil {
		if cfg, err := config.LoadNodeConfig(cfgFile); err == nil {
			if !cfg.CLI.IsColorEnabled() {
				tc.SetColorDisabled()
			}
			return
		}
	}
	// Try relay config.
	if cfgFile, err := config.FindRelayConfigFile(""); err == nil {
		if cfg, err := config.LoadRelayServerConfig(cfgFile); err == nil {
			if !cfg.CLI.IsColorEnabled() {
				tc.SetColorDisabled()
			}
		}
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
	fmt.Println("  daemon                                Start daemon (P2P host + control API)")
	fmt.Println("  daemon status [--json]                Query running daemon")
	fmt.Println("  daemon stop                           Graceful shutdown")
	fmt.Println("  daemon ping <target> [-c N] [--json]  Ping via daemon")
	fmt.Println("  daemon services [--json]              List services via daemon")
	fmt.Println("  daemon peers [--all] [--json]         List connected peers via daemon")
	fmt.Println("  daemon paths [--json]                 Show connection paths")
	fmt.Println("  daemon connect --peer <p> --service <s> --listen <addr>")
	fmt.Println("  daemon disconnect <id>                Tear down proxy")
	fmt.Println()
	fmt.Println("Network tools:")
	fmt.Println("  ping <target> [-c N] [--json]         P2P ping")
	fmt.Println("  traceroute <target> [--json]           P2P traceroute")
	fmt.Println("  resolve <name> [--json]                Resolve name to peer ID")
	fmt.Println("  proxy <target> <service> <local-port>  Forward TCP port")
	fmt.Println()
	fmt.Println("File transfer:")
	fmt.Println("  send <file> <peer> [--follow] [--json]  Send a file to a peer")
	fmt.Println("  share add <path> [--to peer] [--json]   Share a file or directory")
	fmt.Println("  share remove <path>                     Stop sharing a path")
	fmt.Println("  share list [--json]                      List shared paths")
	fmt.Println("  browse <peer> [--json]                   Browse a peer's shared files")
	fmt.Println("  download <peer>:<shareID/file> [--json]  Download from a share")
	fmt.Println("  transfers [--watch] [--json]             List/watch file transfers")
	fmt.Println("  accept <id|--all> [--json]               Accept a pending transfer")
	fmt.Println("  reject <id|--all> [--json]               Reject a pending transfer")
	fmt.Println("  cancel <id> [--json]                     Cancel a queued/active transfer")
	fmt.Println("  clean [--json]                           Remove temp files, free disk space")
	fmt.Println()
	fmt.Println("Identity & access:")
	fmt.Println("  whoami                                 Show your peer ID")
	fmt.Println("  auth add <peer-id> [--comment \"...\"]   Authorize a peer")
	fmt.Println("  auth list                              List authorized peers")
	fmt.Println("  auth remove <peer-id>                  Revoke a peer's access")
	fmt.Println("  auth validate [file]                   Validate authorized_keys format")
	fmt.Println()
	fmt.Println("Configuration:")
	fmt.Println("  init                                   Set up shurli configuration")
	fmt.Println("  config validate [--config path]        Validate config")
	fmt.Println("  config show [--config path]            Show resolved config")
	fmt.Println("  config set <key> <value>               Set a config value")
	fmt.Println("  config rollback [--config path]        Restore last-known-good config")
	fmt.Println("  config apply <new> [--confirm-timeout] Apply with auto-revert")
	fmt.Println("  config confirm [--config path]         Confirm applied config")
	fmt.Println()
	fmt.Println("Relay client:")
	fmt.Println("  relay add <address> [--peer-id <ID>]   Add a relay server")
	fmt.Println("  relay list                             List relay servers")
	fmt.Println("  relay remove <multiaddr>               Remove a relay server")
	fmt.Println()
	fmt.Println("Relay server:")
	fmt.Println("  relay setup                            Initialize relay server config")
	fmt.Println("  relay serve [--config path]            Start the relay server")
	fmt.Println("  relay info                             Show peer ID and multiaddrs")
	fmt.Println("  relay authorize <peer-id> [comment]    Allow a peer")
	fmt.Println("  relay deauthorize <peer-id>            Remove a peer's access")
	fmt.Println("  relay list-peers                       List authorized peers")
	fmt.Println("  relay verify <peer-id>                 Verify peer identity (SAS)")
	fmt.Println("  relay show                             Show resolved relay config")
	fmt.Println("  relay config validate                  Validate relay config")
	fmt.Println("  relay config rollback                  Restore last-known-good config")
	fmt.Println("  relay recover                          Recover relay identity from seed")
	fmt.Println()
	fmt.Println("Relay vault:")
	fmt.Println("  relay vault init [--totp] [--auto-seal] Initialize vault")
	fmt.Println("  relay vault seal                       Seal vault (watch-only mode)")
	fmt.Println("  relay vault unseal [--remote <addr>]   Unseal vault")
	fmt.Println("  relay vault status                     Show vault seal status")
	fmt.Println("  relay vault change-password            Change vault password")
	fmt.Println("  relay seal                             Shorthand for vault seal")
	fmt.Println("  relay unseal [--remote <addr>]         Shorthand for vault unseal")
	fmt.Println("  relay seal-status                      Shorthand for vault status")
	fmt.Println()
	fmt.Println("Relay invites:")
	fmt.Println("  relay invite create [--ttl 1h]         Generate an invite code")
	fmt.Println("  relay invite list                      List invite deposits")
	fmt.Println("  relay invite revoke <id>               Revoke a pending invite")
	fmt.Println()
	fmt.Println("Operator announcements:")
	fmt.Println("  relay motd set <message> [--remote]    Set message of the day")
	fmt.Println("  relay motd clear [--remote]            Clear MOTD")
	fmt.Println("  relay motd status [--remote]           Show MOTD and goodbye status")
	fmt.Println("  relay goodbye set <msg> [--remote]     Set goodbye (pushed to peers)")
	fmt.Println("  relay goodbye retract [--remote]       Retract goodbye announcement")
	fmt.Println("  relay goodbye shutdown [msg] [--remote] Send goodbye and shut down")
	fmt.Println()
	fmt.Println("ZKP:")
	fmt.Println("  relay zkp-setup [--keys-dir path]      Generate PLONK circuit keys")
	fmt.Println("  relay zkp-test [--auth-keys path]      End-to-end ZKP auth test")
	fmt.Println()
	fmt.Println("Services:")
	fmt.Println("  service add <name> <address>           Expose a local service")
	fmt.Println("  service remove <name>                  Remove a service")
	fmt.Println("  service enable <name>                  Enable a service")
	fmt.Println("  service disable <name>                 Disable a service")
	fmt.Println("  service list                           List configured services")
	fmt.Println()
	fmt.Println("Pairing:")
	fmt.Println("  invite [--name \"home\"]                 Generate pairing invite")
	fmt.Println("  join <code> [--name \"laptop\"]          Join with invite code")
	fmt.Println("  verify <peer>                          Verify a peer's identity (SAS)")
	fmt.Println()
	fmt.Println("Identity security:")
	fmt.Println("  recover [--relay] [--dir path]         Recover identity from seed phrase")
	fmt.Println("  change-password                        Change identity password")
	fmt.Println("  lock                                   Lock daemon (disable sensitive ops)")
	fmt.Println("  unlock                                 Unlock daemon with password")
	fmt.Println("  session refresh                        Rotate session token")
	fmt.Println("  session destroy                        Delete session token")
	fmt.Println()
	fmt.Println("Other:")
	fmt.Println("  status [--config path]                 Show local config and services")
	fmt.Println("  doctor [--fix]                         Check installation health")
	fmt.Println("  completion <bash|zsh|fish>             Generate shell completion script")
	fmt.Println("  man                                    Show manual page")
	fmt.Println("  version                                Show version information")
	fmt.Println()
	fmt.Println("The <target> can be a peer ID or a name from the names section of your config.")
	fmt.Println()
	fmt.Println("All commands support --config <path> to specify a config file.")
	fmt.Println("Without --config, shurli searches: ./shurli.yaml, ~/.config/shurli/config.yaml")
	fmt.Println()
	fmt.Println("Get started:  shurli init")
}
