package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/shurlinet/shurli/internal/config"
)

func runPlugin(args []string) {
	if len(args) == 0 {
		printPluginUsage()
		osExit(1)
	}

	switch args[0] {
	case "list":
		runPluginList(args[1:])
	case "enable":
		runPluginEnable(args[1:])
	case "disable":
		runPluginDisable(args[1:])
	case "info":
		runPluginInfo(args[1:])
	case "disable-all":
		runPluginDisableAll(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown plugin command: %s\n\n", args[0])
		printPluginUsage()
		osExit(1)
	}
}

func runPluginList(args []string) {
	fs := flag.NewFlagSet("plugin list", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(args)

	c := tryDaemonClient()
	if c == nil {
		// Fall back to config file when daemon is not running.
		showPluginConfigFallback(*jsonFlag)
		return
	}

	plugins, err := c.PluginList()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(plugins)
		return
	}

	if len(plugins) == 0 {
		fmt.Println("No plugins registered.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tVERSION\tTYPE\tSTATE")
	for _, p := range plugins {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Name, p.Version, p.Type, p.State)
	}
	w.Flush()
}

func runPluginEnable(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: shurli plugin enable <name>")
		osExit(1)
	}

	name := args[0]
	c := daemonClient()
	if err := c.PluginEnable(name); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
	fmt.Printf("Plugin %q enabled.\n", name)
}

func runPluginDisable(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: shurli plugin disable <name>")
		osExit(1)
	}

	name := args[0]
	c := daemonClient()
	if err := c.PluginDisable(name); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
	fmt.Printf("Plugin %q disabled.\n", name)
}

func runPluginInfo(args []string) {
	fs := flag.NewFlagSet("plugin info", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: shurli plugin info <name> [--json]")
		osExit(1)
	}

	name := remaining[0]
	c := tryDaemonClient()
	if c == nil {
		// Fall back to config when daemon not running.
		showPluginConfigFallback(*jsonFlag)
		return
	}
	info, err := c.PluginInfo(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(info)
		return
	}

	fmt.Printf("Name:        %s\n", info.Name)
	fmt.Printf("Version:     %s\n", info.Version)
	fmt.Printf("Type:        %s\n", info.Type)
	fmt.Printf("State:       %s\n", info.State)
	fmt.Printf("Config key:  %s\n", info.ConfigKey)
	if info.CrashCount > 0 {
		fmt.Printf("Crashes:     %d\n", info.CrashCount)
	}
	if len(info.Commands) > 0 {
		fmt.Printf("Commands:    %v\n", info.Commands)
	}
	if len(info.Routes) > 0 {
		fmt.Printf("Routes:      %v\n", info.Routes)
	}
	if len(info.Protocols) > 0 {
		fmt.Printf("Protocols:   %v\n", info.Protocols)
	}
}

func runPluginDisableAll(_ []string) {
	c := daemonClient()
	count, err := c.PluginDisableAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
	fmt.Printf("Disabled %d plugin(s).\n", count)
}

// showPluginConfigFallback reads the config file and shows plugin enabled/disabled
// status when the daemon is not running.
func showPluginConfigFallback(jsonOut bool) {
	cfgFile, err := config.FindConfigFile("")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Daemon not running and no config file found.")
		return
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Daemon not running. Config error: %v\n", err)
		return
	}

	states := cfg.Plugins.PluginStates()
	if len(states) == 0 {
		fmt.Println("Daemon not running. No plugins configured.")
		return
	}

	fmt.Fprintln(os.Stderr, "Daemon not running - showing config only.")

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(states)
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tENABLED")
	for name, enabled := range states {
		fmt.Fprintf(w, "%s\t%v\n", name, enabled)
	}
	w.Flush()
}

func printPluginUsage() {
	fmt.Println("Usage: shurli plugin <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  list [--json]              List all plugins")
	fmt.Println("  enable <name>              Enable a plugin")
	fmt.Println("  disable <name>             Disable a plugin")
	fmt.Println("  info <name> [--json]       Show plugin details")
	fmt.Println("  disable-all                Emergency: disable all plugins")
}
