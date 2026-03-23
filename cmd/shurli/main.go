package main

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/shurlinet/shurli/internal/config"
	tc "github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/pkg/plugin"
	"github.com/shurlinet/shurli/plugins"
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

	// Register plugin CLI commands (always compiled in).
	plugins.RegisterCLI()

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
	case "plugin":
		runPlugin(os.Args[2:])
	case "reconnect":
		runReconnect(os.Args[2:])
	case "notify":
		runNotify(os.Args[2:])
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
		// Check plugin commands.
		if cmd, ok := plugin.FindCLICommand(os.Args[1]); ok {
			if !isPluginEnabledInConfig(cmd.PluginName) {
				fmt.Fprintf(os.Stderr, "Command %q is provided by plugin %q which is disabled.\n", os.Args[1], cmd.PluginName)
				fmt.Fprintf(os.Stderr, "Enable it with: shurli plugin enable %s\n", cmd.PluginName)
				osExit(1)
			}
			cmd.Run(os.Args[2:])
			return
		}
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
	fmt.Println("  reconnect <peer> [--json]              Clear backoffs and force redial")
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
	fmt.Println("  relay seeds <add|remove>               Add/remove public seed nodes")
	fmt.Println()
	fmt.Println("Relay server:")
	fmt.Println("  relay setup                            Initialize relay server config")
	fmt.Println("  relay serve [--config path]            Start the relay server")
	fmt.Println("  relay info                             Show peer ID and multiaddrs")
	fmt.Println("  relay authorize <peer-id> [comment]    Allow a peer")
	fmt.Println("  relay deauthorize <peer-id>            Remove a peer's access")
	fmt.Println("  relay set-attr <peer> <key> <value>    Set peer attribute (e.g. role admin)")
	fmt.Println("  relay grant <peer-id> --duration 1h    Grant time-limited data relay access")
	fmt.Println("  relay grants                           List active data relay grants")
	fmt.Println("  relay revoke <peer-id>                 Revoke data relay access")
	fmt.Println("  relay extend <peer-id> --duration 2h   Extend data relay grant")
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
	fmt.Println("  invite [--as \"home\"]                   Generate pairing invite")
	fmt.Println("  join <code> [--as \"laptop\"]            Join with invite code")
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
	fmt.Println("Notifications:")
	fmt.Println("  notify list                            Show configured notification sinks")
	fmt.Println("  notify test                            Send test notification to all sinks")
	fmt.Println()
	fmt.Println("Plugins:")
	fmt.Println("  plugin list [--json]                   List all plugins")
	fmt.Println("  plugin enable <name>                   Enable a plugin")
	fmt.Println("  plugin disable <name>                  Disable a plugin")
	fmt.Println("  plugin info <name> [--json]            Show plugin details")
	fmt.Println("  plugin disable-all                     Emergency: disable all plugins")
	fmt.Println()
	fmt.Println("Other:")
	fmt.Println("  status [--config path]                 Show local config and services")
	fmt.Println("  doctor [--fix]                         Check installation health")
	fmt.Println("  completion <bash|zsh|fish>             Generate shell completion script")
	fmt.Println("  man                                    Show manual page")
	fmt.Println("  version                                Show version information")
	fmt.Println()
	// Plugin commands: only show if enabled in config.
	cmds := plugin.CLICommandDescriptions()
	if len(cmds) > 0 {
		groups := make(map[string][]plugin.CLICommandEntry)
		for _, cmd := range cmds {
			if isPluginEnabledInConfig(cmd.PluginName) {
				groups[cmd.PluginName] = append(groups[cmd.PluginName], cmd)
			}
		}
		for pluginName, pluginCmds := range groups {
			title := strings.ToUpper(pluginName[:1]) + pluginName[1:]
			fmt.Printf("%s (plugin):\n", title)
			for _, cmd := range pluginCmds {
				fmt.Printf("  %-48s %s\n", cmd.Usage, cmd.Description)
			}
			fmt.Println()
		}
	}

	fmt.Println("The <target> can be a peer ID or a name from the names section of your config.")
	fmt.Println()
	fmt.Println("All commands support --config <path> to specify a config file.")
	fmt.Println("Without --config, shurli searches: ./shurli.yaml, /etc/shurli/config.yaml, ~/.shurli/config.yaml")
	fmt.Println()
	fmt.Println("Get started:  shurli init")
}

// pluginEnabledCache caches plugin enabled state. Populated once per process
// from daemon (preferred) or config file (fallback).
var pluginEnabledCache struct {
	loaded  bool
	entries map[string]bool // plugin name -> enabled
}

// isPluginEnabledInConfig checks if a plugin is enabled.
// Queries the running daemon first (runtime state reflects enable/disable commands).
// Falls back to config file when daemon is not running.
func isPluginEnabledInConfig(name string) bool {
	if pluginEnabledCache.loaded {
		enabled, ok := pluginEnabledCache.entries[name]
		if !ok {
			return true // not known = default enabled for built-in
		}
		return enabled
	}

	pluginEnabledCache.entries = make(map[string]bool)
	pluginEnabledCache.loaded = true

	// Try daemon first for runtime state.
	if c := tryDaemonClient(); c != nil {
		if plugins, err := c.PluginList(); err == nil {
			for _, p := range plugins {
				pluginEnabledCache.entries[p.Name] = (p.State == "active")
			}
			entry, ok := pluginEnabledCache.entries[name]
			if !ok {
				return true
			}
			return entry
		}
	}

	// Fallback: read config file.
	cfgFile, err := config.FindConfigFile("")
	if err != nil {
		return true // no config = default enabled
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		return true
	}
	for pname, entry := range cfg.Plugins.Entries {
		pluginEnabledCache.entries[pname] = entry.Enabled
	}
	entry, ok := pluginEnabledCache.entries[name]
	if !ok {
		return true // not in config = default enabled for built-in
	}
	return entry
}
