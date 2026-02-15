package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/satindergrewal/peer-up/internal/config"
)

func runConfig(args []string) {
	if len(args) < 1 {
		printConfigUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "validate":
		runConfigValidate(args[1:])
	case "show":
		runConfigShow(args[1:])
	case "rollback":
		runConfigRollback(args[1:])
	case "apply":
		runConfigApply(args[1:])
	case "confirm":
		runConfigConfirm(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown config command: %s\n\n", args[0])
		printConfigUsage()
		os.Exit(1)
	}
}

func runConfigValidate(args []string) {
	fs := flag.NewFlagSet("config validate", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fs.Parse(args)

	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		fmt.Printf("FAIL: %s\n", err)
		os.Exit(1)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	if err := config.ValidateNodeConfig(cfg); err != nil {
		fmt.Printf("FAIL: %s\n", err)
		os.Exit(1)
	}

	fmt.Printf("OK: %s is valid\n", cfgFile)
}

func runConfigShow(args []string) {
	fs := flag.NewFlagSet("config show", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fs.Parse(args)

	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	if err := config.ValidateNodeConfig(cfg); err != nil {
		fmt.Printf("WARNING: config has validation errors: %v\n\n", err)
	}

	fmt.Printf("# Resolved config from %s\n", cfgFile)
	out, err := yaml.Marshal(cfg)
	if err != nil {
		log.Fatalf("Failed to marshal config: %v", err)
	}
	fmt.Print(string(out))

	// Show archive status
	if config.HasArchive(cfgFile) {
		fmt.Printf("\n# Last-known-good archive: %s\n", config.ArchivePath(cfgFile))
	} else {
		fmt.Printf("\n# No last-known-good archive (will be created on next successful serve)\n")
	}

	// Show pending commit-confirmed status
	deadline, err := config.CheckPending(cfgFile)
	if err == nil && !deadline.IsZero() {
		remaining := time.Until(deadline).Round(time.Second)
		if remaining > 0 {
			fmt.Printf("# Commit-confirmed pending: %s remaining\n", remaining)
		} else {
			fmt.Printf("# Commit-confirmed expired (will revert on next serve start)\n")
		}
	}
}

func runConfigRollback(args []string) {
	fs := flag.NewFlagSet("config rollback", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fs.Parse(args)

	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	if !config.HasArchive(cfgFile) {
		fmt.Printf("No last-known-good archive for %s\n", cfgFile)
		fmt.Println("Archives are created automatically on each successful peerup serve startup.")
		os.Exit(1)
	}

	if err := config.Rollback(cfgFile); err != nil {
		log.Fatalf("Rollback failed: %v", err)
	}

	fmt.Printf("Restored %s from last-known-good archive\n", cfgFile)
	fmt.Println("You can now restart peerup serve.")
}

func runConfigApply(args []string) {
	fs := flag.NewFlagSet("config apply", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to current config file")
	timeout := fs.Duration("confirm-timeout", 5*time.Minute, "auto-revert timeout (e.g., 5m, 10m)")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: peerup config apply <new-config> [--config path] [--confirm-timeout 5m]")
		os.Exit(1)
	}
	newConfigPath := remaining[0]

	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	// Validate the new config before applying
	newCfg, err := config.LoadNodeConfig(newConfigPath)
	if err != nil {
		fmt.Printf("New config is invalid: %v\n", err)
		os.Exit(1)
	}
	config.ResolveConfigPaths(newCfg, filepath.Dir(newConfigPath))
	if err := config.ValidateNodeConfig(newCfg); err != nil {
		fmt.Printf("New config has validation errors: %v\n", err)
		os.Exit(1)
	}

	if err := config.ApplyCommitConfirmed(cfgFile, newConfigPath, *timeout); err != nil {
		log.Fatalf("Apply failed: %v", err)
	}

	fmt.Printf("Applied %s → %s\n", newConfigPath, cfgFile)
	fmt.Printf("Auto-revert in %s unless confirmed.\n", timeout)
	fmt.Println()
	fmt.Println("After restarting peerup serve and verifying connectivity:")
	fmt.Println("  peerup config confirm")
}

func runConfigConfirm(args []string) {
	fs := flag.NewFlagSet("config confirm", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fs.Parse(args)

	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	if err := config.Confirm(cfgFile); err != nil {
		log.Fatalf("Confirm failed: %v", err)
	}

	fmt.Printf("Config confirmed: %s is now permanent\n", cfgFile)
}

func printConfigUsage() {
	fmt.Println("Usage: peerup config <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  validate [--config path]                                   Validate config without starting")
	fmt.Println("  show     [--config path]                                   Show resolved config")
	fmt.Println("  rollback [--config path]                                   Restore last-known-good config")
	fmt.Println("  apply    <new-config> [--config path] [--confirm-timeout]  Apply config with auto-revert safety")
	fmt.Println("  confirm  [--config path]                                   Confirm applied config (cancel revert)")
}
