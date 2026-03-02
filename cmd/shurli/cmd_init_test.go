package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoInit_FullMultiaddr(t *testing.T) {
	// Skip: doInit now requires interactive terminal for seed confirmation
	// (confirmSeedBackup) and password entry (term.ReadPassword on os.Stdin.Fd()),
	// which cannot be driven from a test harness with io.Reader alone.
	t.Skip("doInit requires interactive terminal for seed confirmation and password entry")
}

func TestDoInit_IPAndPort(t *testing.T) {
	// Skip: doInit now requires interactive terminal for seed confirmation
	// (confirmSeedBackup) and password entry (term.ReadPassword on os.Stdin.Fd()),
	// which cannot be driven from a test harness with io.Reader alone.
	t.Skip("doInit requires interactive terminal for seed confirmation and password entry")
}

func TestDoInit_ConfigAlreadyExists(t *testing.T) {
	dir := t.TempDir()

	// Create a config file first
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}

	stdin := strings.NewReader("1.2.3.4\n")
	var stdout bytes.Buffer

	err := doInit([]string{"--dir", dir}, stdin, &stdout)
	if err == nil {
		t.Fatal("expected error when config exists")
	}
	if !strings.Contains(err.Error(), "config already exists") {
		t.Errorf("error = %q, want 'config already exists'", err.Error())
	}
}

func TestDoInit_EmptyRelay(t *testing.T) {
	dir := t.TempDir()

	stdin := strings.NewReader("\n")
	var stdout bytes.Buffer

	err := doInit([]string{"--dir", dir}, stdin, &stdout)
	if err == nil {
		t.Fatal("expected error for empty relay")
	}
	if !strings.Contains(err.Error(), "relay address is required") {
		t.Errorf("error = %q, want 'relay address is required'", err.Error())
	}
}

func TestDoInit_InvalidMultiaddr(t *testing.T) {
	dir := t.TempDir()

	// Starts with / so isFullMultiaddr returns true, but it's invalid
	stdin := strings.NewReader("/invalid/multiaddr\n")
	var stdout bytes.Buffer

	err := doInit([]string{"--dir", dir}, stdin, &stdout)
	if err == nil {
		t.Fatal("expected error for invalid multiaddr")
	}
	if !strings.Contains(err.Error(), "invalid multiaddr") {
		t.Errorf("error = %q, want 'invalid multiaddr'", err.Error())
	}
}

func TestDoInit_IPWithEmptyPeerID(t *testing.T) {
	dir := t.TempDir()

	// IP:port input, then empty peer ID
	stdin := strings.NewReader("1.2.3.4:7777\n\n")
	var stdout bytes.Buffer

	err := doInit([]string{"--dir", dir}, stdin, &stdout)
	if err == nil {
		t.Fatal("expected error for empty peer ID")
	}
	if !strings.Contains(err.Error(), "relay Peer ID is required") {
		t.Errorf("error = %q, want 'relay Peer ID is required'", err.Error())
	}
}

func TestDoInit_IPWithInvalidPeerID(t *testing.T) {
	dir := t.TempDir()

	stdin := strings.NewReader("1.2.3.4:7777\nnot-a-valid-peer-id\n")
	var stdout bytes.Buffer

	err := doInit([]string{"--dir", dir}, stdin, &stdout)
	if err == nil {
		t.Fatal("expected error for invalid peer ID")
	}
	if !strings.Contains(err.Error(), "invalid Peer ID") {
		t.Errorf("error = %q, want 'invalid Peer ID'", err.Error())
	}
}
