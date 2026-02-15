package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestPendingPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/home/user/.config/peerup/config.yaml", "/home/user/.config/peerup/.config.pending"},
		{"relay-server.yaml", ".relay-server.pending"},
	}
	for _, tt := range tests {
		got := PendingPath(tt.input)
		if got != tt.want {
			t.Errorf("PendingPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBeginAndConfirm(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	original := []byte("version: 1\noriginal: true\n")

	if err := os.WriteFile(cfgPath, original, 0600); err != nil {
		t.Fatal(err)
	}

	// Begin commit-confirmed
	if err := BeginCommitConfirmed(cfgPath, 5*time.Minute); err != nil {
		t.Fatalf("BeginCommitConfirmed() error: %v", err)
	}

	// Pending marker should exist
	deadline, err := CheckPending(cfgPath)
	if err != nil {
		t.Fatalf("CheckPending() error: %v", err)
	}
	if deadline.IsZero() {
		t.Fatal("CheckPending() returned zero deadline")
	}
	if time.Until(deadline) < 4*time.Minute {
		t.Errorf("deadline too soon: %v", deadline)
	}

	// Backup should exist
	backup := backupPath(cfgPath)
	backupData, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("backup not created: %v", err)
	}
	if string(backupData) != string(original) {
		t.Errorf("backup = %q, want %q", backupData, original)
	}

	// Confirm
	if err := Confirm(cfgPath); err != nil {
		t.Fatalf("Confirm() error: %v", err)
	}

	// Marker and backup should be cleaned up
	if _, err := os.Stat(PendingPath(cfgPath)); !os.IsNotExist(err) {
		t.Error("pending marker not removed after Confirm()")
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Error("backup not removed after Confirm()")
	}
}

func TestBeginDuplicate(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("test\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := BeginCommitConfirmed(cfgPath, 5*time.Minute); err != nil {
		t.Fatal(err)
	}

	err := BeginCommitConfirmed(cfgPath, 5*time.Minute)
	if err == nil {
		t.Fatal("expected error on duplicate BeginCommitConfirmed()")
	}
	if !errors.Is(err, ErrCommitConfirmedPending) {
		t.Errorf("error = %v, want ErrCommitConfirmedPending", err)
	}
}

func TestConfirmNoPending(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	err := Confirm(cfgPath)
	if err == nil {
		t.Fatal("expected error on Confirm() with no pending")
	}
	if !errors.Is(err, ErrNoPending) {
		t.Errorf("error = %v, want ErrNoPending", err)
	}
}

func TestCheckPendingNone(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	deadline, err := CheckPending(cfgPath)
	if err != nil {
		t.Fatalf("CheckPending() error: %v", err)
	}
	if !deadline.IsZero() {
		t.Errorf("expected zero deadline, got %v", deadline)
	}
}

func TestApplyCommitConfirmed(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	newPath := filepath.Join(dir, "new-config.yaml")

	original := []byte("version: 1\noriginal: true\n")
	updated := []byte("version: 1\nupdated: true\n")

	if err := os.WriteFile(cfgPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, updated, 0600); err != nil {
		t.Fatal(err)
	}

	if err := ApplyCommitConfirmed(cfgPath, newPath, 5*time.Minute); err != nil {
		t.Fatalf("ApplyCommitConfirmed() error: %v", err)
	}

	// Config should now contain the new content
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(updated) {
		t.Errorf("config = %q, want %q", data, updated)
	}

	// Backup should contain the original
	backup := backupPath(cfgPath)
	backupData, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(backupData) != string(original) {
		t.Errorf("backup = %q, want %q", backupData, original)
	}

	// Pending should be active
	deadline, err := CheckPending(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if deadline.IsZero() {
		t.Fatal("expected pending after ApplyCommitConfirmed()")
	}
}

func TestEnforceCommitConfirmedTimeout(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	original := []byte("version: 1\noriginal: true\n")
	modified := []byte("version: 1\nmodified: true\n")

	if err := os.WriteFile(cfgPath, original, 0600); err != nil {
		t.Fatal(err)
	}

	// Begin commit-confirmed with very short timeout
	if err := BeginCommitConfirmed(cfgPath, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	// Overwrite config (simulating apply)
	if err := os.WriteFile(cfgPath, modified, 0600); err != nil {
		t.Fatal(err)
	}

	// Read the deadline
	deadline, err := CheckPending(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	// Track exit
	var exitCode atomic.Int32
	exitCode.Store(-1)
	exitFunc := func(code int) {
		exitCode.Store(int32(code))
	}

	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		EnforceCommitConfirmed(ctx, cfgPath, deadline, exitFunc)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("EnforceCommitConfirmed() did not return within timeout")
	}

	if exitCode.Load() != 1 {
		t.Errorf("exit code = %d, want 1", exitCode.Load())
	}

	// Config should be reverted
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Errorf("config after revert = %q, want %q", data, original)
	}

	// Marker and backup should be cleaned up
	if _, err := os.Stat(PendingPath(cfgPath)); !os.IsNotExist(err) {
		t.Error("pending marker not cleaned up after revert")
	}
}

func TestEnforceCommitConfirmedCancelled(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(cfgPath, []byte("test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := BeginCommitConfirmed(cfgPath, time.Hour); err != nil {
		t.Fatal(err)
	}

	deadline, _ := CheckPending(cfgPath)

	var exitCalled atomic.Bool
	exitFunc := func(code int) {
		exitCalled.Store(true)
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		EnforceCommitConfirmed(ctx, cfgPath, deadline, exitFunc)
		close(done)
	}()

	// Cancel immediately (simulates Confirm() or shutdown)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("EnforceCommitConfirmed() did not return on cancel")
	}

	if exitCalled.Load() {
		t.Error("exitFunc should not be called on cancel")
	}
}

func TestEnforceDeadlineAlreadyPassed(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	original := []byte("original\n")

	if err := os.WriteFile(cfgPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := BeginCommitConfirmed(cfgPath, time.Millisecond); err != nil {
		t.Fatal(err)
	}

	// Overwrite
	if err := os.WriteFile(cfgPath, []byte("modified\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Wait for deadline to pass
	time.Sleep(10 * time.Millisecond)

	var exitCode atomic.Int32
	exitCode.Store(-1)
	exitFunc := func(code int) {
		exitCode.Store(int32(code))
	}

	// Deadline is in the past
	EnforceCommitConfirmed(context.Background(), cfgPath, time.Now().Add(-time.Second), exitFunc)

	if exitCode.Load() != 1 {
		t.Errorf("exit code = %d, want 1", exitCode.Load())
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Errorf("config = %q, want %q", data, original)
	}
}
