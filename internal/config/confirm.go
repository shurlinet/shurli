package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// pendingState is the JSON structure stored in the pending marker file.
type pendingState struct {
	Deadline   time.Time `json:"deadline"`
	BackupFile string    `json:"backup"`
}

// PendingPath returns the commit-confirmed marker path for a config file.
// Example: config.yaml → .config.pending
func PendingPath(configPath string) string {
	dir := filepath.Dir(configPath)
	base := filepath.Base(configPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	return filepath.Join(dir, "."+name+".pending")
}

// backupPath returns the pre-confirmed backup path for a config file.
// Example: config.yaml → .config.pre-confirmed.yaml
func backupPath(configPath string) string {
	dir := filepath.Dir(configPath)
	base := filepath.Base(configPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	return filepath.Join(dir, "."+name+".pre-confirmed"+ext)
}

// BeginCommitConfirmed backs up the current config and writes a pending marker
// with the revert deadline. Returns ErrCommitConfirmedPending if one is already active.
func BeginCommitConfirmed(configPath string, timeout time.Duration) error {
	pendingFile := PendingPath(configPath)
	if _, err := os.Stat(pendingFile); err == nil {
		return fmt.Errorf("%w: %s", ErrCommitConfirmedPending, pendingFile)
	}

	// Back up current config
	backup := backupPath(configPath)
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("commit-confirmed: read current config: %w", err)
	}
	if err := os.WriteFile(backup, data, 0600); err != nil {
		return fmt.Errorf("commit-confirmed: write backup: %w", err)
	}

	// Write pending marker
	state := pendingState{
		Deadline:   time.Now().Add(timeout),
		BackupFile: filepath.Base(backup),
	}
	marker, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("commit-confirmed: marshal state: %w", err)
	}
	if err := os.WriteFile(pendingFile, marker, 0600); err != nil {
		os.Remove(backup)
		return fmt.Errorf("commit-confirmed: write marker: %w", err)
	}
	return nil
}

// ApplyCommitConfirmed copies newConfigPath over configPath and begins a
// commit-confirmed with the given timeout. This is the high-level operation
// for `peerup config apply`.
func ApplyCommitConfirmed(configPath, newConfigPath string, timeout time.Duration) error {
	// Begin the commit-confirmed first (backs up current config, creates marker)
	if err := BeginCommitConfirmed(configPath, timeout); err != nil {
		return err
	}

	// Now overwrite the config with the new one
	data, err := os.ReadFile(newConfigPath)
	if err != nil {
		// Clean up the pending state since we failed
		os.Remove(PendingPath(configPath))
		os.Remove(backupPath(configPath))
		return fmt.Errorf("commit-confirmed: read new config: %w", err)
	}

	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		os.Remove(PendingPath(configPath))
		os.Remove(backupPath(configPath))
		return fmt.Errorf("commit-confirmed: write temp: %w", err)
	}
	if err := os.Rename(tmp, configPath); err != nil {
		os.Remove(tmp)
		os.Remove(PendingPath(configPath))
		os.Remove(backupPath(configPath))
		return fmt.Errorf("commit-confirmed: rename: %w", err)
	}
	return nil
}

// Confirm removes the pending marker, making the current config permanent.
// Also removes the pre-confirmed backup since it's no longer needed.
// Returns ErrNoPending if no commit-confirmed is active.
func Confirm(configPath string) error {
	pendingFile := PendingPath(configPath)
	if _, err := os.Stat(pendingFile); os.IsNotExist(err) {
		return fmt.Errorf("%w", ErrNoPending)
	}

	// Read the state to find the backup file for cleanup
	data, err := os.ReadFile(pendingFile)
	if err != nil {
		return fmt.Errorf("confirm: read marker: %w", err)
	}
	var state pendingState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("confirm: parse marker: %w", err)
	}

	// Remove marker and backup
	os.Remove(pendingFile)
	if state.BackupFile != "" {
		os.Remove(filepath.Join(filepath.Dir(configPath), state.BackupFile))
	}
	return nil
}

// CheckPending checks if a commit-confirmed is pending.
// Returns the deadline if pending, zero time if not.
func CheckPending(configPath string) (time.Time, error) {
	pendingFile := PendingPath(configPath)
	data, err := os.ReadFile(pendingFile)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("check pending: %w", err)
	}

	var state pendingState
	if err := json.Unmarshal(data, &state); err != nil {
		return time.Time{}, fmt.Errorf("check pending: parse: %w", err)
	}
	return state.Deadline, nil
}

// revertPending restores the pre-confirmed backup over the current config
// and removes the pending marker. Used by EnforceCommitConfirmed on timeout.
func revertPending(configPath string) error {
	pendingFile := PendingPath(configPath)
	data, err := os.ReadFile(pendingFile)
	if err != nil {
		return fmt.Errorf("revert: read marker: %w", err)
	}

	var state pendingState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("revert: parse marker: %w", err)
	}

	backup := filepath.Join(filepath.Dir(configPath), state.BackupFile)
	backupData, err := os.ReadFile(backup)
	if err != nil {
		return fmt.Errorf("revert: read backup: %w", err)
	}

	// Atomic write to config
	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, backupData, 0600); err != nil {
		return fmt.Errorf("revert: write temp: %w", err)
	}
	if err := os.Rename(tmp, configPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("revert: rename: %w", err)
	}

	// Clean up marker and backup
	os.Remove(pendingFile)
	os.Remove(backup)
	return nil
}

// EnforceCommitConfirmed monitors a pending commit-confirmed and reverts
// the config if the deadline passes without confirmation. After reverting,
// it calls exitFunc to terminate the process (systemd will restart with
// the restored config).
//
// Pass os.Exit as exitFunc in production; use a custom function in tests.
func EnforceCommitConfirmed(ctx context.Context, configPath string, deadline time.Time, exitFunc func(int)) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		slog.Warn("commit-confirmed deadline already passed, reverting config",
			"config", configPath)
		if err := revertPending(configPath); err != nil {
			slog.Error("failed to revert config", "error", err)
		}
		exitFunc(1)
		return
	}

	slog.Info("commit-confirmed active",
		"deadline", deadline.Format(time.RFC3339),
		"remaining", remaining.Round(time.Second))

	timer := time.NewTimer(remaining)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		// Context cancelled (shutdown or confirm called) — nothing to do
		return
	case <-timer.C:
		slog.Warn("commit-confirmed timeout — reverting config",
			"config", configPath,
			"deadline", deadline.Format(time.RFC3339))
		if err := revertPending(configPath); err != nil {
			slog.Error("failed to revert config", "error", err)
		}
		exitFunc(1)
	}
}

// EnforceCommitConfirmedWriter is like EnforceCommitConfirmed but writes
// status messages to w instead of using slog. Used for testing.
func EnforceCommitConfirmedWriter(ctx context.Context, w io.Writer, configPath string, deadline time.Time, exitFunc func(int)) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		fmt.Fprintf(w, "commit-confirmed deadline already passed, reverting\n")
		if err := revertPending(configPath); err != nil {
			fmt.Fprintf(w, "revert error: %v\n", err)
		}
		exitFunc(1)
		return
	}

	timer := time.NewTimer(remaining)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		fmt.Fprintf(w, "commit-confirmed timeout, reverting\n")
		if err := revertPending(configPath); err != nil {
			fmt.Fprintf(w, "revert error: %v\n", err)
		}
		exitFunc(1)
	}
}
