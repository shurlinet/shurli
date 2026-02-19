package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoInit_FullMultiaddr(t *testing.T) {
	dir := t.TempDir()
	relayAddr := "/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"

	stdin := strings.NewReader(relayAddr + "\n")
	var stdout bytes.Buffer

	err := doInit([]string{"--dir", dir}, stdin, &stdout)
	if err != nil {
		t.Fatalf("doInit: %v", err)
	}

	out := stdout.String()

	// Verify config file was created
	configFile := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		t.Error("config.yaml not created")
	}

	// Verify identity key was created
	keyFile := filepath.Join(dir, "identity.key")
	if _, err := os.Stat(keyFile); os.IsNotExist(err) {
		t.Error("identity.key not created")
	}

	// Verify authorized_keys was created
	akFile := filepath.Join(dir, "authorized_keys")
	if _, err := os.Stat(akFile); os.IsNotExist(err) {
		t.Error("authorized_keys not created")
	}

	// Verify output contains expected strings
	if !strings.Contains(out, "Welcome to peer-up!") {
		t.Error("output missing 'Welcome to peer-up!'")
	}
	if !strings.Contains(out, "Your Peer ID:") {
		t.Error("output missing 'Your Peer ID:'")
	}
	if !strings.Contains(out, "Config written to:") {
		t.Error("output missing 'Config written to:'")
	}
	if !strings.Contains(out, "Next steps:") {
		t.Error("output missing 'Next steps:'")
	}

	// Verify config content includes the relay address
	data, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "203.0.113.50") {
		t.Error("config should contain relay address")
	}
}

func TestDoInit_IPAndPort(t *testing.T) {
	dir := t.TempDir()
	// Simulate IP:port input, then peer ID on second prompt
	peerID := "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
	stdin := strings.NewReader("1.2.3.4:7777\n" + peerID + "\n")
	var stdout bytes.Buffer

	err := doInit([]string{"--dir", dir}, stdin, &stdout)
	if err != nil {
		t.Fatalf("doInit: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Relay:") {
		t.Error("output should show constructed relay multiaddr")
	}

	// Verify config was created with the multiaddr
	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "1.2.3.4") {
		t.Error("config should contain relay IP")
	}
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
