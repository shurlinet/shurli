package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/satindergrewal/peer-up/internal/auth"
	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/internal/identity"
)

// relaySetupFiles lists the config files managed by relay setup.
var relaySetupFiles = []string{
	"relay-server.yaml",
	"relay_node.key",
	"relay_authorized_keys",
}

func runRelaySetup(args []string) {
	if err := doRelaySetup(args, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelaySetup(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("relay setup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dirFlag := fs.String("dir", "", "relay server directory (default: working directory)")
	freshFlag := fs.Bool("fresh", false, "non-interactive fresh setup (backup existing, create new)")
	nonInteractive := fs.Bool("non-interactive", false, "fail if prompts would be needed")
	if err := fs.Parse(reorderArgs(args, map[string]bool{"fresh": true, "non-interactive": true})); err != nil {
		return err
	}

	// Resolve relay directory
	dir := *dirFlag
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("cannot determine working directory: %w", err)
		}
	}

	// Ensure relay directory exists
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Detect existing files
	hasConfig := fileExists(filepath.Join(dir, "relay-server.yaml"))
	hasKey := fileExists(filepath.Join(dir, "relay_node.key"))
	hasAuth := fileExists(filepath.Join(dir, "relay_authorized_keys"))
	hasExisting := hasConfig || hasKey || hasAuth

	sm := config.NewSnapshotManager(filepath.Join(dir, "backups"))

	if !hasExisting {
		// Fresh install - no prompts needed
		fmt.Fprintln(stdout, "  No existing setup found. Creating fresh configuration.")
		fmt.Fprintln(stdout)
		if err := createFreshRelayConfig(dir, stdout); err != nil {
			return err
		}
		return setRelayPermissions(dir)
	}

	// Files exist - determine action
	if *freshFlag {
		// Non-interactive fresh: backup + create new
		fmt.Fprintln(stdout)
		displayExistingSetup(dir, hasConfig, hasKey, hasAuth, sm, stdout)
		snap, err := sm.Create(dir, relaySetupFiles)
		if err != nil {
			return fmt.Errorf("backup failed: %w", err)
		}
		fmt.Fprintf(stdout, "  Backed up to: %s\n", snap.Path)
		fmt.Fprintln(stdout)
		if err := replaceFreshRelayConfig(dir, hasKey, stdout); err != nil {
			return err
		}
		return setRelayPermissions(dir)
	}

	if *nonInteractive {
		return fmt.Errorf("existing files found in %s. Use --fresh to backup and replace, or run interactively", dir)
	}

	// Interactive mode
	reader := bufio.NewReader(stdin)
	fmt.Fprintln(stdout)
	displayExistingSetup(dir, hasConfig, hasKey, hasAuth, sm, stdout)

	fmt.Fprintln(stdout, "  Options:")
	fmt.Fprintln(stdout, "    1) Keep all existing files")
	fmt.Fprintln(stdout, "    2) Fresh setup (backs up existing files, then creates new)")
	fmt.Fprintln(stdout, "    3) Restore from a backup snapshot")
	fmt.Fprintln(stdout, "    4) Choose per file")
	fmt.Fprintln(stdout)
	fmt.Fprint(stdout, "  Choice [1/2/3/4]: ")

	choice, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}
	choice = strings.TrimSpace(choice)

	switch choice {
	case "1", "":
		fmt.Fprintln(stdout, "  Keeping all existing files.")
	case "2":
		snap, err := sm.Create(dir, relaySetupFiles)
		if err != nil {
			return fmt.Errorf("backup failed: %w", err)
		}
		fmt.Fprintf(stdout, "  Backed up to: %s\n", snap.Path)
		fmt.Fprintln(stdout)
		if err := replaceFreshRelayConfig(dir, hasKey, stdout); err != nil {
			return err
		}
	case "3":
		if err := promptRestore(reader, stdout, dir, sm); err != nil {
			return err
		}
	case "4":
		if err := promptPerFile(reader, stdout, dir, hasConfig, hasKey, hasAuth, sm); err != nil {
			return err
		}
	default:
		fmt.Fprintln(stdout, "  Invalid choice. Keeping all existing files.")
	}

	// Ensure all required files exist (covers partial restores)
	ensureRelayFiles(dir, stdout)
	return setRelayPermissions(dir)
}

// displayExistingSetup shows what files exist and any backup snapshots.
func displayExistingSetup(dir string, hasConfig, hasKey, hasAuth bool, sm *config.SnapshotManager, stdout io.Writer) {
	fmt.Fprintln(stdout, "  Existing setup detected:")

	if hasConfig {
		configPath := filepath.Join(dir, "relay-server.yaml")
		info, err := os.Stat(configPath)
		if err == nil {
			fmt.Fprintf(stdout, "    relay-server.yaml     (modified: %s)\n", info.ModTime().Format("2006-01-02 15:04:05"))
		} else {
			fmt.Fprintln(stdout, "    relay-server.yaml")
		}
	}

	if hasKey {
		keyPath := filepath.Join(dir, "relay_node.key")
		peerID, err := identity.PeerIDFromKeyFile(keyPath)
		if err == nil {
			fmt.Fprintf(stdout, "    relay_node.key        (peer ID: %s)\n", peerID)
		} else {
			fmt.Fprintln(stdout, "    relay_node.key        (identity key)")
		}
	}

	if hasAuth {
		authPath := filepath.Join(dir, "relay_authorized_keys")
		peers, err := auth.ListPeers(authPath)
		if err == nil {
			fmt.Fprintf(stdout, "    relay_authorized_keys (%d peer(s))\n", len(peers))
		} else {
			fmt.Fprintln(stdout, "    relay_authorized_keys")
		}
	}

	// Show existing snapshots
	snapshots, err := sm.List()
	if err == nil && len(snapshots) > 0 {
		fmt.Fprintln(stdout)
		fmt.Fprintf(stdout, "  Existing backups (%d snapshot(s)):\n", len(snapshots))
		for _, snap := range snapshots {
			fmt.Fprintf(stdout, "    %s  [%s]\n", snap.Name, strings.Join(snap.Files, ", "))
		}
	}
	fmt.Fprintln(stdout)
}

// createFreshRelayConfig writes all config files from scratch.
func createFreshRelayConfig(dir string, stdout io.Writer) error {
	// Config file from template
	configPath := filepath.Join(dir, "relay-server.yaml")
	if err := os.WriteFile(configPath, []byte(relayServerConfigTemplate()), 0600); err != nil {
		return fmt.Errorf("failed to write relay-server.yaml: %w", err)
	}
	fmt.Fprintln(stdout, "  Created relay-server.yaml")

	// Empty authorized_keys with header
	authPath := filepath.Join(dir, "relay_authorized_keys")
	header := "# relay_authorized_keys - Authorized peer IDs (one per line)\n" +
		"# Format: <peer_id>  # optional comment\n" +
		"# Add peers with: peerup relay authorize <peer-id>\n"
	if err := os.WriteFile(authPath, []byte(header), 0600); err != nil {
		return fmt.Errorf("failed to write relay_authorized_keys: %w", err)
	}
	fmt.Fprintln(stdout, "  Created relay_authorized_keys (empty)")

	// Key is auto-generated on first serve
	fmt.Fprintln(stdout, "  relay_node.key will be generated on first 'peerup relay serve'")
	return nil
}

// replaceFreshRelayConfig replaces existing files with fresh ones.
// Deletes the key file so a new identity is generated on next serve.
func replaceFreshRelayConfig(dir string, hadKey bool, stdout io.Writer) error {
	if err := createFreshRelayConfig(dir, stdout); err != nil {
		return err
	}
	if hadKey {
		if err := os.Remove(filepath.Join(dir, "relay_node.key")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove old key: %w", err)
		}
	}
	return nil
}

// promptRestore handles the interactive restore-from-snapshot flow.
func promptRestore(reader *bufio.Reader, stdout io.Writer, dir string, sm *config.SnapshotManager) error {
	snapshots, err := sm.List()
	if err != nil {
		return fmt.Errorf("failed to list snapshots: %w", err)
	}
	if len(snapshots) == 0 {
		fmt.Fprintln(stdout, "  No backup snapshots found. Keeping existing files.")
		return nil
	}

	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "  Available snapshots:")
	for i, snap := range snapshots {
		fmt.Fprintf(stdout, "    %d) %s  [%s]\n", i+1, snap.Name, strings.Join(snap.Files, ", "))
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "  Restore which snapshot? [1-%d]: ", len(snapshots))

	input, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}
	idx, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || idx < 1 || idx > len(snapshots) {
		fmt.Fprintln(stdout, "  Invalid selection. Keeping existing files.")
		return nil
	}

	chosen := &snapshots[idx-1]

	// Safety-net: backup current state before restoring
	safetySnap, err := sm.Create(dir, relaySetupFiles)
	if err != nil {
		return fmt.Errorf("safety backup failed: %w", err)
	}
	fmt.Fprintf(stdout, "  Current state backed up to: %s\n", safetySnap.Path)

	if err := sm.Restore(chosen, dir); err != nil {
		return fmt.Errorf("restore failed: %w", err)
	}
	fmt.Fprintf(stdout, "  Restored from: %s\n", chosen.Name)
	return nil
}

// promptPerFile handles the interactive per-file replacement flow.
func promptPerFile(reader *bufio.Reader, stdout io.Writer, dir string,
	hasConfig, hasKey, hasAuth bool, sm *config.SnapshotManager) error {

	backedUp := false
	doBackup := func() error {
		if backedUp {
			return nil
		}
		snap, err := sm.Create(dir, relaySetupFiles)
		if err != nil {
			return fmt.Errorf("backup failed: %w", err)
		}
		fmt.Fprintf(stdout, "  Backed up to: %s\n", snap.Path)
		backedUp = true
		return nil
	}

	if hasConfig {
		fmt.Fprint(stdout, "  Keep existing config? [Y/n]: ")
		resp, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(resp)) == "n" {
			if err := doBackup(); err != nil {
				return err
			}
			configPath := filepath.Join(dir, "relay-server.yaml")
			if err := os.WriteFile(configPath, []byte(relayServerConfigTemplate()), 0600); err != nil {
				return fmt.Errorf("failed to write config: %w", err)
			}
			fmt.Fprintln(stdout, "  Replaced config with fresh template")
		}
	}

	if hasKey {
		fmt.Fprint(stdout, "  Keep existing identity key? [Y/n]: ")
		resp, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(resp)) == "n" {
			if err := doBackup(); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(dir, "relay_node.key")); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to remove key: %w", err)
			}
			fmt.Fprintln(stdout, "  Removed old key (new identity on first serve)")
		}
	}

	if hasAuth {
		fmt.Fprint(stdout, "  Keep existing authorized peers? [Y/n]: ")
		resp, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(resp)) == "n" {
			if err := doBackup(); err != nil {
				return err
			}
			authPath := filepath.Join(dir, "relay_authorized_keys")
			header := "# relay_authorized_keys - Authorized peer IDs (one per line)\n" +
				"# Format: <peer_id>  # optional comment\n" +
				"# Add peers with: peerup relay authorize <peer-id>\n"
			if err := os.WriteFile(authPath, []byte(header), 0600); err != nil {
				return fmt.Errorf("failed to write authorized_keys: %w", err)
			}
			fmt.Fprintln(stdout, "  Cleared authorized peers")
		}
	}

	return nil
}

// ensureRelayFiles creates any missing required files.
// Called after all operations to handle partial restores.
// Only prints for files it actually creates (avoids duplicate messages
// when createFreshRelayConfig already reported them).
func ensureRelayFiles(dir string, stdout io.Writer) {
	configPath := filepath.Join(dir, "relay-server.yaml")
	if !fileExists(configPath) {
		if err := os.WriteFile(configPath, []byte(relayServerConfigTemplate()), 0600); err == nil {
			fmt.Fprintln(stdout, "  Created missing relay-server.yaml")
		}
	}

	authPath := filepath.Join(dir, "relay_authorized_keys")
	if !fileExists(authPath) {
		header := "# relay_authorized_keys - Authorized peer IDs (one per line)\n" +
			"# Format: <peer_id>  # optional comment\n" +
			"# Add peers with: peerup relay authorize <peer-id>\n"
		if err := os.WriteFile(authPath, []byte(header), 0600); err == nil {
			fmt.Fprintln(stdout, "  Created missing relay_authorized_keys")
			fmt.Fprintln(stdout, "  Add peers with: peerup relay authorize <peer-id>")
		}
	}
	// Note: relay_node.key is not created here. It is auto-generated by
	// 'peerup relay serve' on first run. The informational message about
	// this is printed by createFreshRelayConfig to avoid duplication.
}

// setRelayPermissions sets 0600 on all relay config files that exist.
func setRelayPermissions(dir string) error {
	for _, fname := range relaySetupFiles {
		path := filepath.Join(dir, fname)
		if fileExists(path) {
			if err := os.Chmod(path, 0600); err != nil {
				return fmt.Errorf("chmod %s: %w", fname, err)
			}
		}
	}
	return nil
}

// fileExists returns true if the path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
