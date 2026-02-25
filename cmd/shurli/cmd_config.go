package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/shurlinet/shurli/internal/config"
)

func runConfig(args []string) {
	if len(args) < 1 {
		printConfigUsage()
		osExit(1)
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
		osExit(1)
	}
}

func runConfigValidate(args []string) {
	if err := doConfigValidate(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doConfigValidate(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("config validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		fmt.Fprintf(stdout, "FAIL: %s\n", err)
		return fmt.Errorf("invalid config")
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	if err := config.ValidateNodeConfig(cfg); err != nil {
		fmt.Fprintf(stdout, "FAIL: %s\n", err)
		return fmt.Errorf("validation failed")
	}

	fmt.Fprintf(stdout, "OK: %s is valid\n", cfgFile)
	return nil
}

func runConfigShow(args []string) {
	if err := doConfigShow(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doConfigShow(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("config show", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	if err := config.ValidateNodeConfig(cfg); err != nil {
		fmt.Fprintf(stdout, "WARNING: config has validation errors: %v\n\n", err)
	}

	fmt.Fprintf(stdout, "# Resolved config from %s\n", cfgFile)
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	fmt.Fprint(stdout, string(out))

	// Show archive status
	if config.HasArchive(cfgFile) {
		fmt.Fprintf(stdout, "\n# Last-known-good archive: %s\n", config.ArchivePath(cfgFile))
	} else {
		fmt.Fprintf(stdout, "\n# No last-known-good archive (will be created on next successful serve)\n")
	}

	// Show pending commit-confirmed status
	deadline, err := config.CheckPending(cfgFile)
	if err == nil && !deadline.IsZero() {
		remaining := time.Until(deadline).Round(time.Second)
		if remaining > 0 {
			fmt.Fprintf(stdout, "# Commit-confirmed pending: %s remaining\n", remaining)
		} else {
			fmt.Fprintf(stdout, "# Commit-confirmed expired (will revert on next serve start)\n")
		}
	}
	return nil
}

func runConfigRollback(args []string) {
	if err := doConfigRollback(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doConfigRollback(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("config rollback", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	if !config.HasArchive(cfgFile) {
		return fmt.Errorf("no last-known-good archive for %s", cfgFile)
	}

	if err := config.Rollback(cfgFile); err != nil {
		return fmt.Errorf("rollback failed: %w", err)
	}

	fmt.Fprintf(stdout, "Restored %s from last-known-good archive\n", cfgFile)
	fmt.Fprintln(stdout, "You can now restart shurli daemon.")
	return nil
}

func runConfigApply(args []string) {
	if err := doConfigApply(args, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doConfigApply(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("config apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configFlag := fs.String("config", "", "path to current config file")
	timeout := fs.Duration("confirm-timeout", 5*time.Minute, "auto-revert timeout (e.g., 5m, 10m)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	remaining := fs.Args()
	if len(remaining) < 1 {
		return fmt.Errorf("usage: shurli config apply <new-config> [--config path] [--confirm-timeout 5m]")
	}
	newConfigPath := remaining[0]

	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	// Validate the new config before applying
	newCfg, err := config.LoadNodeConfig(newConfigPath)
	if err != nil {
		return fmt.Errorf("new config is invalid: %w", err)
	}
	config.ResolveConfigPaths(newCfg, filepath.Dir(newConfigPath))
	if err := config.ValidateNodeConfig(newCfg); err != nil {
		return fmt.Errorf("new config has validation errors: %w", err)
	}

	if err := config.ApplyCommitConfirmed(cfgFile, newConfigPath, *timeout); err != nil {
		return fmt.Errorf("apply failed: %w", err)
	}

	fmt.Fprintf(stdout, "Applied %s → %s\n", newConfigPath, cfgFile)
	fmt.Fprintf(stdout, "Auto-revert in %s unless confirmed.\n", timeout)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "After restarting shurli daemon and verifying connectivity:")
	fmt.Fprintln(stdout, "  shurli config confirm")
	return nil
}

func runConfigConfirm(args []string) {
	if err := doConfigConfirm(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doConfigConfirm(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("config confirm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	if err := config.Confirm(cfgFile); err != nil {
		return fmt.Errorf("confirm failed: %w", err)
	}

	fmt.Fprintf(stdout, "Config confirmed: %s is now permanent\n", cfgFile)
	return nil
}

func printConfigUsage() {
	fmt.Println("Usage: shurli config <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  validate [--config path]                                   Validate config without starting")
	fmt.Println("  show     [--config path]                                   Show resolved config")
	fmt.Println("  rollback [--config path]                                   Restore last-known-good config")
	fmt.Println("  apply    <new-config> [--config path] [--confirm-timeout]  Apply config with auto-revert safety")
	fmt.Println("  confirm  [--config path]                                   Confirm applied config (cancel revert)")
}
