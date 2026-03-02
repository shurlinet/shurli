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

func runChangePassword(args []string) {
	fs := flag.NewFlagSet("change-password", flag.ExitOnError)
	dirFlag := fs.String("dir", "", "config directory (default: auto-detect)")
	fs.Parse(args)

	// Resolve config directory.
	var configDir string
	if *dirFlag != "" {
		configDir = *dirFlag
	} else {
		cfgFile, err := config.FindConfigFile("")
		if err != nil {
			fatal("Config not found: %v\nRun 'shurli init' first.", err)
		}
		configDir = filepath.Dir(cfgFile)
	}

	keyPath := filepath.Join(configDir, "identity.key")

	// Read current password.
	currentPassword, err := readPassword("Current password: ", os.Stdout)
	if err != nil {
		fatal("Failed to read password: %v", err)
	}

	// Verify current password by loading the key.
	_, err = identity.LoadIdentity(keyPath, currentPassword)
	if err != nil {
		fatal("Invalid current password: %v", err)
	}

	// Read new password with confirmation.
	newPassword, err := readPasswordConfirm(
		"New password: ",
		"Confirm new password: ",
		os.Stdout,
	)
	if err != nil {
		fatal("Password error: %v", err)
	}

	if currentPassword == newPassword {
		fatal("New password must be different from current password.")
	}

	// Re-encrypt the key file.
	if err := identity.ChangeKeyPassword(keyPath, currentPassword, newPassword); err != nil {
		fatal("Failed to change password: %v", err)
	}

	// Update session token with new password.
	if identity.SessionExists(configDir) {
		if err := identity.CreateSession(configDir, newPassword); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to update session token: %v\n", err)
		}
	}

	fmt.Println()
	termcolor.Green("Password changed successfully.")
}
