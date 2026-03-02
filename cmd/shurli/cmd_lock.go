package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/identity"
	"github.com/shurlinet/shurli/internal/termcolor"
)

func runLock(args []string) {
	_ = args // no flags needed

	c := daemonClient()
	if err := c.Lock(); err != nil {
		fatal("Failed to lock: %v", err)
	}

	termcolor.Green("Daemon locked.")
	fmt.Println("Sensitive operations disabled. Use 'shurli unlock' to re-enable.")
}

func runUnlock(args []string) {
	_ = args // no flags needed

	// Prompt for password to prove human presence.
	password, err := readPassword("Password: ", os.Stdout)
	if err != nil {
		fatal("Failed to read password: %v", err)
	}

	// Verify password against identity.key.
	cfgFile, err := config.FindConfigFile("")
	if err != nil {
		fatal("Config not found: %v", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		fatal("Config error: %v", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	if _, err := identity.LoadIdentity(cfg.Identity.KeyFile, password); err != nil {
		fatal("Invalid password: %v", err)
	}

	// Signal daemon to unlock.
	c := daemonClient()
	if err := c.Unlock(); err != nil {
		fatal("Failed to unlock: %v", err)
	}

	termcolor.Green("Daemon unlocked.")
	fmt.Println("Sensitive operations enabled.")
}

func runSession(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: shurli session <command>")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Commands:")
		fmt.Fprintln(os.Stderr, "  refresh   Rotate session token (same password, fresh crypto)")
		fmt.Fprintln(os.Stderr, "  destroy   Delete session token (require password on next start)")
		osExit(1)
	}

	switch args[0] {
	case "refresh":
		runSessionRefresh()
	case "destroy":
		runSessionDestroy()
	default:
		fmt.Fprintf(os.Stderr, "Unknown session command: %s\n", args[0])
		osExit(1)
	}
}

func runSessionRefresh() {
	// Prompt for current password.
	password, err := readPassword("Password: ", os.Stdout)
	if err != nil {
		fatal("Failed to read password: %v", err)
	}

	// Verify password against identity.key.
	cfgFile, err := config.FindConfigFile("")
	if err != nil {
		fatal("Config not found: %v", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		fatal("Config error: %v", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))
	configDir := filepath.Dir(cfgFile)

	if _, err := identity.LoadIdentity(cfg.Identity.KeyFile, password); err != nil {
		fatal("Invalid password: %v", err)
	}

	// Create new session token with fresh crypto material.
	if err := identity.CreateSession(configDir, password); err != nil {
		fatal("Failed to refresh session: %v", err)
	}

	termcolor.Green("Session token refreshed.")
	fmt.Println("Fresh cryptographic material generated. Same password, new token.")
}

func runSessionDestroy() {
	fs := flag.NewFlagSet("session destroy", flag.ExitOnError)
	fs.Parse(os.Args) // consume to avoid warnings

	cfgFile, err := config.FindConfigFile("")
	if err != nil {
		fatal("Config not found: %v", err)
	}
	configDir := filepath.Dir(cfgFile)

	if !identity.SessionExists(configDir) {
		fmt.Println("No session token found.")
		return
	}

	if err := identity.DestroySession(configDir); err != nil {
		fatal("Failed to destroy session: %v", err)
	}

	termcolor.Green("Session token destroyed.")
	fmt.Println("You will need to provide a password on next daemon start.")
	fmt.Println("Run 'shurli init' to create a new session token.")
}
