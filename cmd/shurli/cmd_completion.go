package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/shurlinet/shurli/pkg/plugin"
)

func runCompletion(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: shurli completion <bash|zsh|fish> [--install|--uninstall|--path]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Generate or install shell completion scripts.")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Print to stdout (manual setup):")
		fmt.Fprintln(os.Stderr, "  eval \"$(shurli completion bash)\"")
		fmt.Fprintln(os.Stderr, "  eval \"$(shurli completion zsh)\"")
		fmt.Fprintln(os.Stderr, "  shurli completion fish | source")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Install system-wide (persists across sessions):")
		fmt.Fprintln(os.Stderr, "  shurli completion bash --install")
		fmt.Fprintln(os.Stderr, "  shurli completion zsh --install")
		fmt.Fprintln(os.Stderr, "  shurli completion fish --install")
		osExit(1)
	}

	shell := args[0]
	if shell != "bash" && shell != "zsh" && shell != "fish" {
		fmt.Fprintf(os.Stderr, "Unsupported shell: %s\n", shell)
		fmt.Fprintln(os.Stderr, "Supported: bash, zsh, fish")
		osExit(1)
	}

	// Check for --install / --uninstall / --path flags.
	if len(args) > 1 {
		switch args[1] {
		case "--install":
			installCompletion(shell)
			return
		case "--uninstall":
			uninstallCompletion(shell)
			return
		case "--path":
			fmt.Println(completionInstallPath(shell))
			return
		default:
			fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", args[1])
			osExit(1)
		}
	}

	// Default: print script to stdout.
	switch shell {
	case "bash":
		fmt.Print(buildCompletionBash())
	case "zsh":
		fmt.Print(buildCompletionZsh())
	case "fish":
		fmt.Print(buildCompletionFish())
	}
}

// buildCompletionBash returns the bash completion script with plugin commands injected.
// enabledPluginCommands returns only commands from enabled plugins.
func enabledPluginCommands() []plugin.CLICommandEntry {
	all := plugin.CLICommandDescriptions()
	var enabled []plugin.CLICommandEntry
	for _, cmd := range all {
		if isPluginEnabledInConfig(cmd.PluginName) {
			enabled = append(enabled, cmd)
		}
	}
	return enabled
}

func buildCompletionBash() string {
	cmds := enabledPluginCommands()
	if len(cmds) == 0 {
		return completionBash
	}
	// Inject plugin command names into the top-level commands= string.
	var names []string
	for _, cmd := range cmds {
		names = append(names, cmd.Name)
	}
	extra := strings.Join(names, " ")
	s := strings.Replace(completionBash, "PLUGIN_COMMANDS_PLACEHOLDER", extra, 1)
	// Inject plugin case branches.
	s = strings.Replace(s, "# PLUGIN_CASES_PLACEHOLDER", plugin.GenerateBashCompletion(cmds), 1)
	return s
}

// buildCompletionZsh returns the zsh completion script with plugin commands injected.
func buildCompletionZsh() string {
	cmds := enabledPluginCommands()
	if len(cmds) == 0 {
		return completionZsh
	}
	// Inject plugin command descriptions.
	var entries []string
	for _, cmd := range cmds {
		desc := strings.ReplaceAll(cmd.Description, "'", "'\\''")
		entries = append(entries, fmt.Sprintf("        '%s:%s'", cmd.Name, desc))
	}
	s := strings.Replace(completionZsh, "        # PLUGIN_COMMANDS_PLACEHOLDER", strings.Join(entries, "\n"), 1)
	// Inject plugin case branches.
	s = strings.Replace(s, "        # PLUGIN_CASES_PLACEHOLDER", plugin.GenerateZshCompletion(cmds), 1)
	return s
}

// buildCompletionFish returns the fish completion script with plugin commands injected.
func buildCompletionFish() string {
	cmds := enabledPluginCommands()
	if len(cmds) == 0 {
		return completionFish
	}
	s := strings.Replace(completionFish, "# PLUGIN_COMMANDS_PLACEHOLDER\n", plugin.GenerateFishCompletion(cmds), 1)
	return s
}

// completionInstallPath returns the install path for a given shell.
// User-local by default (no sudo needed). Falls back to system paths
// only when running as root.
func completionInstallPath(shell string) string {
	home, _ := os.UserHomeDir()
	isRoot := os.Getuid() == 0

	switch shell {
	case "bash":
		if isRoot {
			if runtime.GOOS == "darwin" {
				return "/usr/local/etc/bash_completion.d/shurli"
			}
			return "/etc/bash_completion.d/shurli"
		}
		// User-local bash completion.
		return filepath.Join(home, ".local", "share", "bash-completion", "completions", "shurli")
	case "zsh":
		if isRoot {
			return "/usr/local/share/zsh/site-functions/_shurli"
		}
		// User-local zsh completion. Add to fpath in .zshrc if needed.
		return filepath.Join(home, ".local", "share", "zsh", "site-functions", "_shurli")
	case "fish":
		// Fish always uses user directory.
		return filepath.Join(home, ".config", "fish", "completions", "shurli.fish")
	}
	return ""
}

func installCompletion(shell string) {
	dest := completionInstallPath(shell)
	dir := filepath.Dir(dest)

	var content string
	switch shell {
	case "bash":
		content = buildCompletionBash()
	case "zsh":
		content = buildCompletionZsh()
	case "fish":
		content = buildCompletionFish()
	}

	// Create parent directory.
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create %s: %v\n", dir, err)
		if os.IsPermission(err) && shell != "fish" {
			fmt.Fprintf(os.Stderr, "Try: sudo shurli completion %s --install\n", shell)
		}
		osExit(1)
	}

	if err := os.WriteFile(dest, []byte(content), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write %s: %v\n", dest, err)
		if os.IsPermission(err) && shell != "fish" {
			fmt.Fprintf(os.Stderr, "Try: sudo shurli completion %s --install\n", shell)
		}
		osExit(1)
	}

	fmt.Printf("Installed: %s\n", dest)

	switch shell {
	case "bash":
		fmt.Println("Restart your shell or run: source " + dest)
	case "zsh":
		dir := filepath.Dir(dest)
		if os.Getuid() != 0 {
			fmt.Printf("Ensure your fpath includes %s. Add to ~/.zshrc if needed:\n", dir)
			fmt.Printf("  fpath=(%s $fpath)\n", dir)
		}
		fmt.Println("Restart your shell or run: autoload -Uz compinit && compinit")
	case "fish":
		fmt.Println("Completions are active immediately in new fish sessions.")
	}
}

func uninstallCompletion(shell string) {
	dest := completionInstallPath(shell)

	if _, err := os.Stat(dest); os.IsNotExist(err) {
		fmt.Printf("Completion not installed for %s.\n", shell)
		return
	}

	if err := os.Remove(dest); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to remove %s: %v\n", dest, err)
		if os.IsPermission(err) && shell != "fish" {
			fmt.Fprintf(os.Stderr, "Try: sudo shurli completion %s --uninstall\n", shell)
		}
		osExit(1)
	}

	fmt.Printf("Removed: %s\n", dest)
}

// completionBash generates a bash completion script.
// Uses the programmable completion (complete/compgen/COMPREPLY) API.
var completionBash = `# shurli bash completion - generated by shurli completion bash
# Add to ~/.bashrc: eval "$(shurli completion bash)"

_shurli_completions() {
    local cur prev words cword
    _init_completion || return

    local commands="init daemon proxy ping traceroute resolve whoami auth relay config invite join verify service plugin status recover change-password lock unlock session doctor completion man version help PLUGIN_COMMANDS_PLACEHOLDER"

    local daemon_cmds="start status stop ping services peers paths connect disconnect"
    local auth_cmds="add list remove validate grant grants revoke extend delegate"
    local config_cmds="validate show set rollback apply confirm"
    local relay_cmds="add list remove show setup serve authorize deauthorize set-attr grant grants revoke extend list-peers verify info invite vault seal unseal seal-status config version zkp-setup zkp-test motd goodbye recover"
    local relay_invite_cmds="create list revoke"
    local relay_vault_cmds="init seal unseal status change-password"
    local relay_motd_cmds="set clear status"
    local relay_goodbye_cmds="set retract shutdown status"
    local relay_config_cmds="show validate rollback"
    local service_cmds="add list remove enable disable"
    local plugin_cmds="list enable disable info disable-all"
    local completion_shells="bash zsh fish"

    case "${words[1]}" in
        daemon)
            case "${words[2]}" in
                status|services|peers|paths)
                    COMPREPLY=($(compgen -W "--json" -- "$cur"))
                    return ;;
                ping)
                    COMPREPLY=($(compgen -W "-c --interval --json" -- "$cur"))
                    return ;;
                connect)
                    COMPREPLY=($(compgen -W "--peer --service --listen" -- "$cur"))
                    return ;;
                start)
                    COMPREPLY=($(compgen -W "--config" -- "$cur"))
                    return ;;
                *)
                    COMPREPLY=($(compgen -W "$daemon_cmds" -- "$cur"))
                    return ;;
            esac
            ;;
        auth)
            case "${words[2]}" in
                add)
                    COMPREPLY=($(compgen -W "--config --file --comment --role" -- "$cur"))
                    return ;;
                list|remove|validate)
                    COMPREPLY=($(compgen -W "--config --file" -- "$cur"))
                    return ;;
                grant)
                    COMPREPLY=($(compgen -W "--duration --services --permanent --delegate" -- "$cur"))
                    return ;;
                extend)
                    COMPREPLY=($(compgen -W "--duration" -- "$cur"))
                    return ;;
                delegate)
                    COMPREPLY=($(compgen -W "--to --duration --services --delegate" -- "$cur"))
                    return ;;
                grants|revoke)
                    return ;;
                *)
                    COMPREPLY=($(compgen -W "$auth_cmds" -- "$cur"))
                    return ;;
            esac
            ;;
        config)
            case "${words[2]}" in
                apply)
                    COMPREPLY=($(compgen -W "--config --confirm-timeout" -- "$cur"))
                    return ;;
                set)
                    COMPREPLY=($(compgen -W "--config --duration" -- "$cur"))
                    return ;;
                validate|show|rollback|confirm)
                    COMPREPLY=($(compgen -W "--config" -- "$cur"))
                    return ;;
                *)
                    COMPREPLY=($(compgen -W "$config_cmds" -- "$cur"))
                    return ;;
            esac
            ;;
        relay)
            case "${words[2]}" in
                add)
                    COMPREPLY=($(compgen -W "--config --peer-id" -- "$cur"))
                    return ;;
                list|info|seal|seal-status|version)
                    COMPREPLY=($(compgen -W "--config" -- "$cur"))
                    return ;;
                authorize|deauthorize|list-peers|grants)
                    COMPREPLY=($(compgen -W "--config --remote" -- "$cur"))
                    return ;;
                grant)
                    COMPREPLY=($(compgen -W "--duration --services --permanent --remote" -- "$cur"))
                    return ;;
                revoke)
                    COMPREPLY=($(compgen -W "--remote" -- "$cur"))
                    return ;;
                extend)
                    COMPREPLY=($(compgen -W "--duration --remote" -- "$cur"))
                    return ;;
                remove)
                    COMPREPLY=($(compgen -W "--config --force -f" -- "$cur"))
                    return ;;
                serve)
                    COMPREPLY=($(compgen -W "--config" -- "$cur"))
                    return ;;
                setup)
                    COMPREPLY=($(compgen -W "--dir --fresh --non-interactive" -- "$cur"))
                    return ;;
                unseal)
                    COMPREPLY=($(compgen -W "--config --remote --totp" -- "$cur"))
                    return ;;
                invite)
                    case "${words[3]}" in
                        create)
                            COMPREPLY=($(compgen -W "--ttl --expires --remote" -- "$cur"))
                            return ;;
                        *)
                            COMPREPLY=($(compgen -W "$relay_invite_cmds" -- "$cur"))
                            return ;;
                    esac
                    ;;
                vault)
                    case "${words[3]}" in
                        init)
                            COMPREPLY=($(compgen -W "--totp --auto-seal" -- "$cur"))
                            return ;;
                        unseal)
                            COMPREPLY=($(compgen -W "--remote --totp" -- "$cur"))
                            return ;;
                        *)
                            COMPREPLY=($(compgen -W "$relay_vault_cmds" -- "$cur"))
                            return ;;
                    esac
                    ;;
                config)
                    COMPREPLY=($(compgen -W "$relay_config_cmds" -- "$cur"))
                    return ;;
                zkp-setup)
                    COMPREPLY=($(compgen -W "--keys-dir --force" -- "$cur"))
                    return ;;
                zkp-test)
                    COMPREPLY=($(compgen -W "--auth-keys --keys-dir --relay --role" -- "$cur"))
                    return ;;
                motd)
                    case "${words[3]}" in
                        set|clear|status)
                            COMPREPLY=($(compgen -W "--remote" -- "$cur"))
                            return ;;
                        *)
                            COMPREPLY=($(compgen -W "$relay_motd_cmds" -- "$cur"))
                            return ;;
                    esac
                    ;;
                goodbye)
                    case "${words[3]}" in
                        set|retract|shutdown|status)
                            COMPREPLY=($(compgen -W "--remote" -- "$cur"))
                            return ;;
                        *)
                            COMPREPLY=($(compgen -W "$relay_goodbye_cmds" -- "$cur"))
                            return ;;
                    esac
                    ;;
                *)
                    COMPREPLY=($(compgen -W "$relay_cmds" -- "$cur"))
                    return ;;
            esac
            ;;
        service)
            case "${words[2]}" in
                add)
                    COMPREPLY=($(compgen -W "--config --protocol" -- "$cur"))
                    return ;;
                list|remove|enable|disable)
                    COMPREPLY=($(compgen -W "--config" -- "$cur"))
                    return ;;
                *)
                    COMPREPLY=($(compgen -W "$service_cmds" -- "$cur"))
                    return ;;
            esac
            ;;
        plugin)
            case "${words[2]}" in
                list|info)
                    COMPREPLY=($(compgen -W "--json" -- "$cur"))
                    return ;;
                *)
                    COMPREPLY=($(compgen -W "$plugin_cmds" -- "$cur"))
                    return ;;
            esac
            ;;
        recover)
            COMPREPLY=($(compgen -W "--relay --dir" -- "$cur"))
            return ;;
        change-password)
            COMPREPLY=($(compgen -W "--dir" -- "$cur"))
            return ;;
        session)
            COMPREPLY=($(compgen -W "refresh destroy" -- "$cur"))
            return ;;
        ping)
            COMPREPLY=($(compgen -W "--config -c -n --interval --json --standalone" -- "$cur"))
            return ;;
        traceroute)
            COMPREPLY=($(compgen -W "--config --json --standalone" -- "$cur"))
            return ;;
        resolve)
            COMPREPLY=($(compgen -W "--config --json" -- "$cur"))
            return ;;
        # PLUGIN_CASES_PLACEHOLDER
        proxy)
            COMPREPLY=($(compgen -W "--config --standalone" -- "$cur"))
            return ;;
        whoami|verify|status)
            COMPREPLY=($(compgen -W "--config" -- "$cur"))
            return ;;
        invite)
            COMPREPLY=($(compgen -W "--config --as --ttl --non-interactive" -- "$cur"))
            return ;;
        join)
            COMPREPLY=($(compgen -W "--config --as --non-interactive" -- "$cur"))
            return ;;
        init)
            COMPREPLY=($(compgen -W "--dir --network" -- "$cur"))
            return ;;
        doctor)
            COMPREPLY=($(compgen -W "--fix" -- "$cur"))
            return ;;
        completion)
            if [[ ${cword} -eq 2 ]]; then
                COMPREPLY=($(compgen -W "$completion_shells" -- "$cur"))
            else
                COMPREPLY=($(compgen -W "--install --uninstall --path" -- "$cur"))
            fi
            return ;;
        man)
            COMPREPLY=($(compgen -W "--install --uninstall --path" -- "$cur"))
            return ;;
    esac

    # Top-level command completion.
    if [[ ${cword} -eq 1 ]]; then
        COMPREPLY=($(compgen -W "$commands" -- "$cur"))
        return
    fi
}

complete -F _shurli_completions shurli
`

// completionZsh generates a zsh completion script.
// Uses the compdef/compadd API with _arguments for flag handling.
var completionZsh = `#compdef shurli
# shurli zsh completion - generated by shurli completion zsh
# Add to ~/.zshrc: eval "$(shurli completion zsh)"

_shurli() {
    local -a commands
    commands=(
        'init:Set up shurli configuration'
        'daemon:Daemon management'
        'proxy:Forward TCP port'
        'ping:P2P ping'
        'traceroute:P2P traceroute'
        'resolve:Resolve name to peer ID'
        # PLUGIN_COMMANDS_PLACEHOLDER
        'whoami:Show your peer ID'
        'auth:Identity and access management'
        'relay:Relay client and server commands'
        'config:Configuration management'
        'invite:Create an invite code'
        'join:Join using an invite code'
        'verify:Verify a peer identity (SAS)'
        'service:Manage local services'
        'plugin:Manage plugins'
        'status:Show local config and services'
        'recover:Recover identity from seed phrase'
        'change-password:Change identity password'
        'lock:Lock daemon'
        'unlock:Unlock daemon'
        'session:Session token management'
        'doctor:Check installation health'
        'completion:Generate shell completion script'
        'man:Show manual page'
        'version:Show version information'
        'help:Show usage information'
    )

    local -a daemon_cmds
    daemon_cmds=(
        'start:Start daemon in foreground'
        'status:Query running daemon'
        'stop:Graceful shutdown'
        'ping:Ping a peer via daemon'
        'services:List services via daemon'
        'peers:List connected peers'
        'paths:Show connection paths'
        'connect:Create a TCP proxy'
        'disconnect:Tear down a proxy'
    )

    local -a auth_cmds
    auth_cmds=(
        'add:Authorize a peer'
        'list:List authorized peers'
        'remove:Revoke a peer'
        'validate:Validate authorized_keys format'
        'grant:Grant data access'
        'grants:List active grants'
        'revoke:Revoke data access grant'
        'extend:Extend a grant'
        'delegate:Delegate a grant to another peer'
    )

    local -a config_cmds
    config_cmds=(
        'validate:Validate config'
        'show:Show resolved config'
        'set:Set a config value'
        'rollback:Restore last-known-good config'
        'apply:Apply config with auto-revert'
        'confirm:Confirm applied config'
    )

    local -a relay_cmds
    relay_cmds=(
        'add:Add a relay server'
        'list:List relay servers'
        'remove:Remove a relay server'
        'show:Show resolved relay config'
        'setup:Initialize relay server config'
        'serve:Start the relay server'
        'authorize:Allow a peer'
        'deauthorize:Remove peer access'
        'list-peers:List authorized peers'
        'verify:Verify a peer identity (SAS)'
        'info:Show peer ID and multiaddrs'
        'invite:Manage invites'
        'vault:Manage relay vault'
        'seal:Seal vault (watch-only mode)'
        'unseal:Unseal vault'
        'seal-status:Show vault seal status'
        'config:Relay config management'
        'version:Show relay version'
        'zkp-setup:Generate PLONK circuit keys'
        'zkp-test:End-to-end ZKP auth test'
        'motd:Manage relay MOTD'
        'goodbye:Manage goodbye announcements'
        'recover:Recover relay identity from seed'
        'grant:Grant time-limited data relay access'
        'grants:List active data relay grants'
        'revoke:Revoke data relay access'
        'extend:Extend data relay grant'
    )

    local -a relay_invite_cmds
    relay_invite_cmds=(
        'create:Generate an invite code'
        'list:List active invites'
        'revoke:Revoke an invite'
    )

    local -a relay_vault_cmds
    relay_vault_cmds=(
        'init:Initialize vault'
        'seal:Seal vault'
        'unseal:Unseal vault'
        'status:Show vault status'
        'change-password:Change vault password'
    )

    local -a relay_motd_cmds
    relay_motd_cmds=(
        'set:Set MOTD message'
        'clear:Clear MOTD'
        'status:Show MOTD and goodbye status'
    )

    local -a relay_goodbye_cmds
    relay_goodbye_cmds=(
        'set:Set goodbye announcement'
        'retract:Retract goodbye'
        'shutdown:Send goodbye and shut down'
        'status:Show MOTD and goodbye status'
    )

    local -a relay_config_cmds
    relay_config_cmds=(
        'show:Show resolved relay config'
        'validate:Validate relay config'
        'rollback:Restore last-known-good config'
    )

    local -a service_cmds
    service_cmds=(
        'add:Expose a local service'
        'list:List configured services'
        'remove:Remove a service'
        'enable:Enable a service'
        'disable:Disable a service'
    )

    local -a plugin_cmds
    plugin_cmds=(
        'list:List all plugins'
        'enable:Enable a plugin'
        'disable:Disable a plugin'
        'info:Show plugin details'
        'disable-all:Emergency disable all plugins'
    )

    local -a completion_shells
    completion_shells=('bash' 'zsh' 'fish')

    # Determine context.
    if (( CURRENT == 2 )); then
        _describe -t commands 'shurli command' commands
        return
    fi

    case "${words[2]}" in
        daemon)
            if (( CURRENT == 3 )); then
                _describe -t daemon_cmds 'daemon subcommand' daemon_cmds
            else
                case "${words[3]}" in
                    status|services|peers|paths)
                        _arguments '--json[Output as JSON]' ;;
                    ping)
                        _arguments '-c[Number of pings]:count' '--interval[Ping interval (ms)]:ms' '--json[Output as JSON]' ;;
                    connect)
                        _arguments '--peer[Peer name or ID]:peer' '--service[Service name]:service' '--listen[Local listen address]:addr' ;;
                    start)
                        _arguments '--config[Config file]:file:_files' ;;
                esac
            fi
            ;;
        auth)
            if (( CURRENT == 3 )); then
                _describe -t auth_cmds 'auth subcommand' auth_cmds
            else
                case "${words[3]}" in
                    grant)
                        _arguments '--duration[Grant duration]:duration' '--services[Comma-separated services]:services' '--permanent[Permanent grant]' '--delegate[Delegation hops (0=none, N=limited, -1=unlimited)]:hops' ;;
                    extend)
                        _arguments '--duration[Extension duration]:duration' ;;
                    delegate)
                        _arguments '--to[Target peer]:peer' '--duration[Shorter duration]:duration' '--services[Fewer services]:services' '--delegate[Further delegation hops]:hops' ;;
                    add)
                        _arguments '--config[Config file]:file:_files' '--file[authorized_keys path]:file:_files' '--comment[Peer comment]:comment' '--role[Peer role (admin/member)]:role:(admin member)' ;;
                    *)
                        _arguments '--config[Config file]:file:_files' '--file[authorized_keys path]:file:_files' ;;
                esac
            fi
            ;;
        config)
            if (( CURRENT == 3 )); then
                _describe -t config_cmds 'config subcommand' config_cmds
            else
                case "${words[3]}" in
                    set)
                        _arguments '--config[Config file]:file:_files' '--duration[Timed receive mode duration]:duration' ;;
                    apply)
                        _arguments '--config[Config file]:file:_files' '--confirm-timeout[Auto-revert timeout]:duration' ;;
                    *)
                        _arguments '--config[Config file]:file:_files' ;;
                esac
            fi
            ;;
        relay)
            if (( CURRENT == 3 )); then
                _describe -t relay_cmds 'relay subcommand' relay_cmds
            else
                case "${words[3]}" in
                    add)
                        _arguments '--config[Config file]:file:_files' '--peer-id[Relay peer ID]:id' ;;
                    remove)
                        _arguments '--config[Config file]:file:_files' '--force[Force removal]' '-f[Force removal]' ;;
                    serve)
                        _arguments '--config[Config file]:file:_files' ;;
                    setup)
                        _arguments '--dir[Relay directory]:dir:_directories' '--fresh[Non-interactive fresh setup]' '--non-interactive[Fail if prompts needed]' ;;
                    authorize|deauthorize|list-peers|grants)
                        _arguments '--config[Config file]:file:_files' '--remote[Relay multiaddr]:addr' ;;
                    grant)
                        _arguments '--duration[Grant duration]:duration' '--services[Service names]:services' '--permanent[No expiry]' '--remote[Relay multiaddr]:addr' ;;
                    revoke)
                        _arguments '--remote[Relay multiaddr]:addr' ;;
                    extend)
                        _arguments '--duration[New duration]:duration' '--remote[Relay multiaddr]:addr' ;;
                    unseal)
                        _arguments '--config[Config file]:file:_files' '--remote[Relay multiaddr]:addr' '--totp[Prompt for TOTP code]' ;;
                    invite)
                        if (( CURRENT == 4 )); then
                            _describe -t relay_invite_cmds 'invite subcommand' relay_invite_cmds
                        else
                            case "${words[4]}" in
                                create)
                                    _arguments '--ttl[Code validity]:duration' '--expires[Auth expiry]:duration' '--remote[Relay multiaddr]:addr' ;;
                            esac
                        fi
                        ;;
                    vault)
                        if (( CURRENT == 4 )); then
                            _describe -t relay_vault_cmds 'vault subcommand' relay_vault_cmds
                        else
                            case "${words[4]}" in
                                init)
                                    _arguments '--totp[Enable TOTP 2FA]' '--auto-seal[Auto-seal timeout (minutes)]:minutes' ;;
                                unseal)
                                    _arguments '--remote[Relay multiaddr]:addr' '--totp[Prompt for TOTP code]' ;;
                            esac
                        fi
                        ;;
                    config)
                        if (( CURRENT == 4 )); then
                            _describe -t relay_config_cmds 'relay config subcommand' relay_config_cmds
                        fi
                        ;;
                    zkp-setup)
                        _arguments '--keys-dir[Output directory]:dir:_directories' '--force[Overwrite existing keys]' ;;
                    zkp-test)
                        _arguments '--auth-keys[authorized_keys path]:file:_files' '--keys-dir[ZKP keys directory]:dir:_directories' '--relay[Relay multiaddr]:addr' '--role[Role to prove]:role:(0 1 2)' ;;
                    motd)
                        if (( CURRENT == 4 )); then
                            _describe -t relay_motd_cmds 'motd subcommand' relay_motd_cmds
                        else
                            _arguments '--remote[Relay multiaddr]:addr'
                        fi
                        ;;
                    goodbye)
                        if (( CURRENT == 4 )); then
                            _describe -t relay_goodbye_cmds 'goodbye subcommand' relay_goodbye_cmds
                        else
                            _arguments '--remote[Relay multiaddr]:addr'
                        fi
                        ;;
                esac
            fi
            ;;
        service)
            if (( CURRENT == 3 )); then
                _describe -t service_cmds 'service subcommand' service_cmds
            else
                _arguments '--config[Config file]:file:_files' '--protocol[Custom protocol ID]:protocol'
            fi
            ;;
        plugin)
            if (( CURRENT == 3 )); then
                _describe -t plugin_cmds 'plugin subcommand' plugin_cmds
            else
                case "${words[3]}" in
                    list|info)
                        _arguments '--json[Output as JSON]' ;;
                esac
            fi
            ;;
        ping)
            _arguments '--config[Config file]:file:_files' '-c[Number of pings]:count' '-n[Number of pings]:count' '--interval[Ping interval]:interval' '--json[Output as JSON]' '--standalone[Direct P2P mode]' ;;
        traceroute)
            _arguments '--config[Config file]:file:_files' '--json[Output as JSON]' '--standalone[Direct P2P mode]' ;;
        resolve)
            _arguments '--config[Config file]:file:_files' '--json[Output as JSON]' ;;
        # PLUGIN_CASES_PLACEHOLDER
        proxy)
            _arguments '--config[Config file]:file:_files' '--standalone[Direct P2P mode]' ;;
        whoami|verify|status)
            _arguments '--config[Config file]:file:_files' ;;
        invite)
            _arguments '--config[Config file]:file:_files' '--as[Your node name]:name' '--ttl[Invite TTL]:duration' '--non-interactive[Machine-friendly output]' ;;
        join)
            _arguments '--config[Config file]:file:_files' '--as[Your node name]:name' '--non-interactive[Machine-friendly output]' ;;
        recover)
            _arguments '--relay[Also recover relay vault]' '--dir[Config directory]:dir:_directories' ;;
        change-password)
            _arguments '--dir[Config directory]:dir:_directories' ;;
        session)
            if (( CURRENT == 3 )); then
                local -a session_cmds
                session_cmds=('refresh:Rotate session token' 'destroy:Delete session token')
                _describe -t session_cmds 'session subcommand' session_cmds
            fi
            ;;
        init)
            _arguments '--dir[Config directory]:dir:_directories' '--network[DHT namespace]:namespace' ;;
        doctor)
            _arguments '--fix[Auto-fix issues]'
            ;;
        completion)
            if (( CURRENT == 3 )); then
                _describe -t shells 'shell' completion_shells
            elif (( CURRENT == 4 )); then
                _arguments '--install[Install completion system-wide]' '--uninstall[Remove installed completion]' '--path[Show install path]'
            fi
            ;;
        man)
            _arguments '--install[Install man page system-wide]' '--uninstall[Remove installed man page]' '--path[Show install path]'
            ;;
    esac
}

compdef _shurli shurli
`

// completionFish generates a fish shell completion script.
// Uses the fish "complete" builtin with conditions for subcommand context.
var completionFish = `# shurli fish completion - generated by shurli completion fish
# Save to: ~/.config/fish/completions/shurli.fish
# Or run:  shurli completion fish | source

# Disable file completions by default.
complete -c shurli -f

# Helper: true when no subcommand has been given yet.
function __shurli_no_subcommand
    set -l cmd (commandline -opc)
    test (count $cmd) -eq 1
end

# Helper: true when the first arg matches.
function __shurli_using_command
    set -l cmd (commandline -opc)
    test (count $cmd) -ge 2; and test "$cmd[2]" = "$argv[1]"
end

# Helper: true when first + second arg match.
function __shurli_using_subcommand
    set -l cmd (commandline -opc)
    test (count $cmd) -ge 3; and test "$cmd[2]" = "$argv[1]"; and test "$cmd[3]" = "$argv[2]"
end

# --- Top-level commands ---
complete -c shurli -n __shurli_no_subcommand -a init        -d 'Set up shurli configuration'
complete -c shurli -n __shurli_no_subcommand -a daemon      -d 'Daemon management'
complete -c shurli -n __shurli_no_subcommand -a proxy       -d 'Forward TCP port'
complete -c shurli -n __shurli_no_subcommand -a ping        -d 'P2P ping'
complete -c shurli -n __shurli_no_subcommand -a traceroute  -d 'P2P traceroute'
complete -c shurli -n __shurli_no_subcommand -a resolve     -d 'Resolve name to peer ID'
# PLUGIN_COMMANDS_PLACEHOLDER
complete -c shurli -n __shurli_no_subcommand -a whoami      -d 'Show your peer ID'
complete -c shurli -n __shurli_no_subcommand -a auth        -d 'Identity and access management'
complete -c shurli -n __shurli_no_subcommand -a relay       -d 'Relay client and server'
complete -c shurli -n __shurli_no_subcommand -a config      -d 'Configuration management'
complete -c shurli -n __shurli_no_subcommand -a invite      -d 'Create an invite code'
complete -c shurli -n __shurli_no_subcommand -a join        -d 'Join using an invite code'
complete -c shurli -n __shurli_no_subcommand -a verify      -d 'Verify a peer identity (SAS)'
complete -c shurli -n __shurli_no_subcommand -a service     -d 'Manage local services'
complete -c shurli -n __shurli_no_subcommand -a plugin      -d 'Manage plugins'
complete -c shurli -n __shurli_no_subcommand -a status      -d 'Show local config and services'
complete -c shurli -n __shurli_no_subcommand -a recover         -d 'Recover identity from seed phrase'
complete -c shurli -n __shurli_no_subcommand -a change-password -d 'Change identity password'
complete -c shurli -n __shurli_no_subcommand -a lock            -d 'Lock daemon'
complete -c shurli -n __shurli_no_subcommand -a unlock          -d 'Unlock daemon'
complete -c shurli -n __shurli_no_subcommand -a session         -d 'Session token management'
complete -c shurli -n __shurli_no_subcommand -a doctor      -d 'Check installation health'
complete -c shurli -n __shurli_no_subcommand -a completion  -d 'Generate shell completion script'
complete -c shurli -n __shurli_no_subcommand -a man         -d 'Show manual page'
complete -c shurli -n __shurli_no_subcommand -a version     -d 'Show version information'
complete -c shurli -n __shurli_no_subcommand -a help        -d 'Show usage information'

# --- init ---
complete -c shurli -n '__shurli_using_command init' -l dir     -d 'Config directory'
complete -c shurli -n '__shurli_using_command init' -l network -d 'DHT namespace'

# --- daemon subcommands ---
complete -c shurli -n '__shurli_using_command daemon' -a start      -d 'Start daemon'
complete -c shurli -n '__shurli_using_command daemon' -a status     -d 'Query running daemon'
complete -c shurli -n '__shurli_using_command daemon' -a stop       -d 'Graceful shutdown'
complete -c shurli -n '__shurli_using_command daemon' -a ping       -d 'Ping a peer via daemon'
complete -c shurli -n '__shurli_using_command daemon' -a services   -d 'List services via daemon'
complete -c shurli -n '__shurli_using_command daemon' -a peers      -d 'List connected peers'
complete -c shurli -n '__shurli_using_command daemon' -a paths      -d 'Show connection paths'
complete -c shurli -n '__shurli_using_command daemon' -a connect    -d 'Create a TCP proxy'
complete -c shurli -n '__shurli_using_command daemon' -a disconnect -d 'Tear down a proxy'

complete -c shurli -n '__shurli_using_subcommand daemon status'   -l json -d 'Output as JSON'
complete -c shurli -n '__shurli_using_subcommand daemon services' -l json -d 'Output as JSON'
complete -c shurli -n '__shurli_using_subcommand daemon peers'    -l json -d 'Output as JSON'
complete -c shurli -n '__shurli_using_subcommand daemon peers'    -l all  -d 'Show all peers'
complete -c shurli -n '__shurli_using_subcommand daemon paths'    -l json -d 'Output as JSON'
complete -c shurli -n '__shurli_using_subcommand daemon ping'     -s c    -d 'Number of pings'
complete -c shurli -n '__shurli_using_subcommand daemon ping'     -l interval -d 'Ping interval (ms)'
complete -c shurli -n '__shurli_using_subcommand daemon ping'     -l json -d 'Output as JSON'
complete -c shurli -n '__shurli_using_subcommand daemon connect'  -l peer    -d 'Peer name or ID'
complete -c shurli -n '__shurli_using_subcommand daemon connect'  -l service -d 'Service name'
complete -c shurli -n '__shurli_using_subcommand daemon connect'  -l listen  -d 'Local listen address'
complete -c shurli -n '__shurli_using_subcommand daemon start'    -l config  -d 'Config file'

# --- auth subcommands ---
complete -c shurli -n '__shurli_using_command auth' -a add      -d 'Authorize a peer'
complete -c shurli -n '__shurli_using_command auth' -a list     -d 'List authorized peers'
complete -c shurli -n '__shurli_using_command auth' -a remove   -d 'Revoke a peer'
complete -c shurli -n '__shurli_using_command auth' -a validate -d 'Validate authorized_keys'

complete -c shurli -n '__shurli_using_subcommand auth add'      -l config  -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand auth add'      -l file    -d 'authorized_keys path'
complete -c shurli -n '__shurli_using_subcommand auth add'      -l comment -d 'Peer comment'
complete -c shurli -n '__shurli_using_subcommand auth add'      -l role    -d 'Peer role'
complete -c shurli -n '__shurli_using_subcommand auth list'     -l config  -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand auth list'     -l file    -d 'authorized_keys path'
complete -c shurli -n '__shurli_using_subcommand auth remove'   -l config  -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand auth remove'   -l file    -d 'authorized_keys path'
complete -c shurli -n '__shurli_using_subcommand auth validate' -l config  -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand auth validate' -l file    -d 'authorized_keys path'
complete -c shurli -n '__shurli_using_command auth' -a grant    -d 'Grant data access'
complete -c shurli -n '__shurli_using_command auth' -a grants   -d 'List active grants'
complete -c shurli -n '__shurli_using_command auth' -a revoke   -d 'Revoke data access grant'
complete -c shurli -n '__shurli_using_command auth' -a extend   -d 'Extend a grant'
complete -c shurli -n '__shurli_using_command auth' -a delegate -d 'Delegate a grant to another peer'
complete -c shurli -n '__shurli_using_subcommand auth grant'    -l duration  -d 'Grant duration (e.g. 1h, 7d)'
complete -c shurli -n '__shurli_using_subcommand auth grant'    -l services  -d 'Comma-separated services'
complete -c shurli -n '__shurli_using_subcommand auth grant'    -l permanent -d 'Permanent grant'
complete -c shurli -n '__shurli_using_subcommand auth grant'    -l delegate  -d 'Delegation hops (0=none, N, -1=unlimited)'
complete -c shurli -n '__shurli_using_subcommand auth extend'   -l duration  -d 'Extension duration'
complete -c shurli -n '__shurli_using_subcommand auth delegate' -l to        -d 'Target peer'
complete -c shurli -n '__shurli_using_subcommand auth delegate' -l duration  -d 'Shorter duration'
complete -c shurli -n '__shurli_using_subcommand auth delegate' -l services  -d 'Fewer services'
complete -c shurli -n '__shurli_using_subcommand auth delegate' -l delegate  -d 'Further delegation hops'

# --- config subcommands ---
complete -c shurli -n '__shurli_using_command config' -a validate -d 'Validate config'
complete -c shurli -n '__shurli_using_command config' -a show     -d 'Show resolved config'
complete -c shurli -n '__shurli_using_command config' -a set      -d 'Set a config value'
complete -c shurli -n '__shurli_using_command config' -a rollback -d 'Restore last-known-good config'
complete -c shurli -n '__shurli_using_command config' -a apply    -d 'Apply config with auto-revert'
complete -c shurli -n '__shurli_using_command config' -a confirm  -d 'Confirm applied config'

complete -c shurli -n '__shurli_using_subcommand config validate' -l config -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand config show'     -l config -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand config set'      -l config   -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand config set'      -l duration -d 'Timed receive mode duration (e.g. 10m)'
complete -c shurli -n '__shurli_using_subcommand config rollback' -l config -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand config apply'    -l config -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand config apply'    -l confirm-timeout -d 'Auto-revert timeout'
complete -c shurli -n '__shurli_using_subcommand config confirm'  -l config -d 'Config file'

# --- relay subcommands ---
complete -c shurli -n '__shurli_using_command relay' -a add         -d 'Add a relay server'
complete -c shurli -n '__shurli_using_command relay' -a list        -d 'List relay servers'
complete -c shurli -n '__shurli_using_command relay' -a remove      -d 'Remove a relay server'
complete -c shurli -n '__shurli_using_command relay' -a show        -d 'Show resolved relay config'
complete -c shurli -n '__shurli_using_command relay' -a setup       -d 'Initialize relay server config'
complete -c shurli -n '__shurli_using_command relay' -a serve       -d 'Start the relay server'
complete -c shurli -n '__shurli_using_command relay' -a authorize   -d 'Allow a peer'
complete -c shurli -n '__shurli_using_command relay' -a deauthorize -d 'Remove peer access'
complete -c shurli -n '__shurli_using_command relay' -a list-peers  -d 'List authorized peers'
complete -c shurli -n '__shurli_using_command relay' -a verify      -d 'Verify a peer identity (SAS)'
complete -c shurli -n '__shurli_using_command relay' -a info        -d 'Show peer ID and multiaddrs'
complete -c shurli -n '__shurli_using_command relay' -a invite      -d 'Manage invites'
complete -c shurli -n '__shurli_using_command relay' -a vault       -d 'Manage relay vault'
complete -c shurli -n '__shurli_using_command relay' -a seal        -d 'Seal vault'
complete -c shurli -n '__shurli_using_command relay' -a unseal      -d 'Unseal vault'
complete -c shurli -n '__shurli_using_command relay' -a seal-status -d 'Show vault seal status'
complete -c shurli -n '__shurli_using_command relay' -a config      -d 'Relay config management'
complete -c shurli -n '__shurli_using_command relay' -a version     -d 'Show relay version'
complete -c shurli -n '__shurli_using_command relay' -a zkp-setup   -d 'Generate PLONK circuit keys'
complete -c shurli -n '__shurli_using_command relay' -a zkp-test    -d 'End-to-end ZKP auth test'
complete -c shurli -n '__shurli_using_command relay' -a motd        -d 'Manage relay MOTD'
complete -c shurli -n '__shurli_using_command relay' -a goodbye     -d 'Manage goodbye announcements'
complete -c shurli -n '__shurli_using_command relay' -a recover     -d 'Recover relay identity from seed'
complete -c shurli -n '__shurli_using_command relay' -a grant       -d 'Grant time-limited data relay access'
complete -c shurli -n '__shurli_using_command relay' -a grants      -d 'List active data relay grants'
complete -c shurli -n '__shurli_using_command relay' -a revoke      -d 'Revoke data relay access'
complete -c shurli -n '__shurli_using_command relay' -a extend      -d 'Extend data relay grant'

complete -c shurli -n '__shurli_using_subcommand relay add'    -l config  -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand relay add'    -l peer-id -d 'Relay peer ID'
complete -c shurli -n '__shurli_using_subcommand relay remove' -l config  -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand relay remove' -l force   -d 'Force removal'
complete -c shurli -n '__shurli_using_subcommand relay remove' -s f       -d 'Force removal'
complete -c shurli -n '__shurli_using_subcommand relay authorize'   -l config -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand relay authorize'   -l remote -d 'Relay multiaddr'
complete -c shurli -n '__shurli_using_subcommand relay deauthorize' -l config -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand relay deauthorize' -l remote -d 'Relay multiaddr'
complete -c shurli -n '__shurli_using_subcommand relay list-peers'  -l config -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand relay list-peers'  -l remote -d 'Relay multiaddr'
complete -c shurli -n '__shurli_using_subcommand relay grant'       -l duration  -d 'Grant duration'
complete -c shurli -n '__shurli_using_subcommand relay grant'       -l services  -d 'Comma-separated services'
complete -c shurli -n '__shurli_using_subcommand relay grant'       -l permanent -d 'Permanent grant'
complete -c shurli -n '__shurli_using_subcommand relay grant'       -l remote    -d 'Relay multiaddr'
complete -c shurli -n '__shurli_using_subcommand relay grants'      -l remote    -d 'Relay multiaddr'
complete -c shurli -n '__shurli_using_subcommand relay revoke'      -l remote    -d 'Relay multiaddr'
complete -c shurli -n '__shurli_using_subcommand relay extend'      -l duration  -d 'New duration'
complete -c shurli -n '__shurli_using_subcommand relay extend'      -l remote    -d 'Relay multiaddr'
complete -c shurli -n '__shurli_using_subcommand relay serve'  -l config  -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand relay setup'  -l dir     -d 'Relay directory'
complete -c shurli -n '__shurli_using_subcommand relay setup'  -l fresh   -d 'Non-interactive fresh setup'
complete -c shurli -n '__shurli_using_subcommand relay setup'  -l non-interactive -d 'Fail if prompts needed'
complete -c shurli -n '__shurli_using_subcommand relay unseal' -l config  -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand relay unseal' -l remote  -d 'Relay multiaddr'
complete -c shurli -n '__shurli_using_subcommand relay unseal' -l totp    -d 'Prompt for TOTP code'

# relay invite sub-subcommands
complete -c shurli -n '__shurli_using_subcommand relay invite' -a create -d 'Generate an invite code'
complete -c shurli -n '__shurli_using_subcommand relay invite' -a list   -d 'List active invites'
complete -c shurli -n '__shurli_using_subcommand relay invite' -a revoke -d 'Revoke an invite'

# relay vault sub-subcommands
complete -c shurli -n '__shurli_using_subcommand relay vault' -a init   -d 'Initialize vault'
complete -c shurli -n '__shurli_using_subcommand relay vault' -a seal   -d 'Seal vault'
complete -c shurli -n '__shurli_using_subcommand relay vault' -a unseal -d 'Unseal vault'
complete -c shurli -n '__shurli_using_subcommand relay vault' -a status          -d 'Show vault status'
complete -c shurli -n '__shurli_using_subcommand relay vault' -a change-password -d 'Change vault password'

# relay motd sub-subcommands
complete -c shurli -n '__shurli_using_subcommand relay motd' -a set    -d 'Set MOTD message'
complete -c shurli -n '__shurli_using_subcommand relay motd' -a clear  -d 'Clear MOTD'
complete -c shurli -n '__shurli_using_subcommand relay motd' -a status -d 'Show MOTD and goodbye status'

# relay goodbye sub-subcommands
complete -c shurli -n '__shurli_using_subcommand relay goodbye' -a set      -d 'Set goodbye announcement'
complete -c shurli -n '__shurli_using_subcommand relay goodbye' -a retract  -d 'Retract goodbye'
complete -c shurli -n '__shurli_using_subcommand relay goodbye' -a shutdown -d 'Send goodbye and shut down'
complete -c shurli -n '__shurli_using_subcommand relay goodbye' -a status   -d 'Show MOTD and goodbye status'

# relay config sub-subcommands
complete -c shurli -n '__shurli_using_subcommand relay config' -a show     -d 'Show resolved relay config'
complete -c shurli -n '__shurli_using_subcommand relay config' -a validate -d 'Validate relay config'
complete -c shurli -n '__shurli_using_subcommand relay config' -a rollback -d 'Restore last-known-good config'

# relay zkp flags
complete -c shurli -n '__shurli_using_subcommand relay zkp-setup' -l seed      -d 'BIP39 seed phrase'
complete -c shurli -n '__shurli_using_subcommand relay zkp-setup' -l keys-dir  -d 'Output directory'
complete -c shurli -n '__shurli_using_subcommand relay zkp-setup' -l generate  -d 'Generate new seed'
complete -c shurli -n '__shurli_using_subcommand relay zkp-setup' -l force     -d 'Overwrite existing keys'
complete -c shurli -n '__shurli_using_subcommand relay zkp-test'  -l auth-keys -d 'authorized_keys path'
complete -c shurli -n '__shurli_using_subcommand relay zkp-test'  -l keys-dir  -d 'ZKP keys directory'
complete -c shurli -n '__shurli_using_subcommand relay zkp-test'  -l relay     -d 'Relay multiaddr'
complete -c shurli -n '__shurli_using_subcommand relay zkp-test'  -l role      -d 'Role to prove (0/1/2)'
complete -c shurli -n '__shurli_using_subcommand relay zkp-test'  -l seed      -d 'BIP39 seed phrase'

# --- service subcommands ---
complete -c shurli -n '__shurli_using_command service' -a add     -d 'Expose a local service'
complete -c shurli -n '__shurli_using_command service' -a list    -d 'List configured services'
complete -c shurli -n '__shurli_using_command service' -a remove  -d 'Remove a service'
complete -c shurli -n '__shurli_using_command service' -a enable  -d 'Enable a service'
complete -c shurli -n '__shurli_using_command service' -a disable -d 'Disable a service'

complete -c shurli -n '__shurli_using_subcommand service add'     -l config   -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand service add'     -l protocol -d 'Custom protocol ID'
complete -c shurli -n '__shurli_using_subcommand service list'    -l config   -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand service remove'  -l config   -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand service enable'  -l config   -d 'Config file'
complete -c shurli -n '__shurli_using_subcommand service disable' -l config   -d 'Config file'

# --- plugin subcommands ---
complete -c shurli -n '__shurli_using_command plugin' -a list        -d 'List all plugins'
complete -c shurli -n '__shurli_using_command plugin' -a enable      -d 'Enable a plugin'
complete -c shurli -n '__shurli_using_command plugin' -a disable     -d 'Disable a plugin'
complete -c shurli -n '__shurli_using_command plugin' -a info        -d 'Show plugin details'
complete -c shurli -n '__shurli_using_command plugin' -a disable-all -d 'Emergency disable all plugins'

complete -c shurli -n '__shurli_using_subcommand plugin list' -l json -d 'Output as JSON'
complete -c shurli -n '__shurli_using_subcommand plugin info' -l json -d 'Output as JSON'

# --- standalone commands with flags ---
complete -c shurli -n '__shurli_using_command ping'       -l config     -d 'Config file'
complete -c shurli -n '__shurli_using_command ping'       -s c          -d 'Number of pings'
complete -c shurli -n '__shurli_using_command ping'       -s n          -d 'Number of pings'
complete -c shurli -n '__shurli_using_command ping'       -l interval   -d 'Ping interval'
complete -c shurli -n '__shurli_using_command ping'       -l json       -d 'Output as JSON'
complete -c shurli -n '__shurli_using_command ping'       -l standalone -d 'Direct P2P mode'
complete -c shurli -n '__shurli_using_command traceroute' -l config     -d 'Config file'
complete -c shurli -n '__shurli_using_command traceroute' -l json       -d 'Output as JSON'
complete -c shurli -n '__shurli_using_command traceroute' -l standalone -d 'Direct P2P mode'
complete -c shurli -n '__shurli_using_command resolve'    -l config     -d 'Config file'
complete -c shurli -n '__shurli_using_command resolve'    -l json       -d 'Output as JSON'
complete -c shurli -n '__shurli_using_command proxy'      -l config     -d 'Config file'
complete -c shurli -n '__shurli_using_command proxy'      -l standalone -d 'Direct P2P mode'
complete -c shurli -n '__shurli_using_command whoami'     -l config     -d 'Config file'
complete -c shurli -n '__shurli_using_command verify'     -l config     -d 'Config file'
complete -c shurli -n '__shurli_using_command status'     -l config     -d 'Config file'
complete -c shurli -n '__shurli_using_command invite'     -l config     -d 'Config file'
complete -c shurli -n '__shurli_using_command invite'     -l name       -d 'Peer name'
complete -c shurli -n '__shurli_using_command invite'     -l ttl        -d 'Invite TTL'
complete -c shurli -n '__shurli_using_command invite'     -l non-interactive -d 'Machine-friendly output'
complete -c shurli -n '__shurli_using_command join'       -l config     -d 'Config file'
complete -c shurli -n '__shurli_using_command join'       -l name       -d 'Peer name'
complete -c shurli -n '__shurli_using_command join'       -l non-interactive -d 'Machine-friendly output'

# --- identity security ---
complete -c shurli -n '__shurli_using_command recover'         -l seed          -d 'BIP39 seed phrase'
complete -c shurli -n '__shurli_using_command recover'         -l relay         -d 'Also recover relay vault'
complete -c shurli -n '__shurli_using_command recover'         -l dir           -d 'Config directory'
complete -c shurli -n '__shurli_using_command change-password' -l dir           -d 'Config directory'

# --- session subcommands ---
complete -c shurli -n '__shurli_using_command session' -a refresh -d 'Rotate session token'
complete -c shurli -n '__shurli_using_command session' -a destroy -d 'Delete session token'

# --- doctor ---
complete -c shurli -n '__shurli_using_command doctor' -l fix -d 'Auto-fix issues'

# --- completion ---
complete -c shurli -n '__shurli_using_command completion' -a 'bash zsh fish' -d 'Shell type'

# --- man ---
complete -c shurli -n '__shurli_using_command man' -l install   -d 'Install man page system-wide'
complete -c shurli -n '__shurli_using_command man' -l uninstall -d 'Remove installed man page'
complete -c shurli -n '__shurli_using_command man' -l path      -d 'Show install path'
`
