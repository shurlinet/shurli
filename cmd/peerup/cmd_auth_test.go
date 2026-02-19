package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// generateTestPeerID creates a fresh valid Ed25519 peer ID for testing.
func generateTestPeerID(t *testing.T) string {
	t.Helper()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}

// writeAuthKeysFile creates an authorized_keys file with the given content.
func writeAuthKeysFile(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	return p
}

// ----- doAuthAdd tests -----

func TestDoAuthAdd(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, dir string) (args []string, authPath string)
		wantErr    bool
		wantErrStr string
		wantOutput string // substring in stdout buffer
	}{
		{
			name: "add peer to empty file",
			setup: func(t *testing.T, dir string) ([]string, string) {
				peerID := generateTestPeerID(t)
				akPath := filepath.Join(dir, "authorized_keys")
				// File does not need to exist for add — AddPeer creates it.
				return []string{peerID, "--file", akPath}, akPath
			},
			wantOutput: "File:",
		},
		{
			name: "add peer with comment",
			setup: func(t *testing.T, dir string) ([]string, string) {
				peerID := generateTestPeerID(t)
				akPath := filepath.Join(dir, "authorized_keys")
				return []string{peerID, "--file", akPath, "--comment", "home server"}, akPath
			},
			wantOutput: "Comment: home server",
		},
		{
			name: "invalid peer ID",
			setup: func(t *testing.T, dir string) ([]string, string) {
				akPath := filepath.Join(dir, "authorized_keys")
				return []string{"not-a-valid-peer-id", "--file", akPath}, akPath
			},
			wantErr:    true,
			wantErrStr: "invalid peer ID",
		},
		{
			name: "missing peer ID arg",
			setup: func(t *testing.T, dir string) ([]string, string) {
				akPath := filepath.Join(dir, "authorized_keys")
				return []string{"--file", akPath}, akPath
			},
			wantErr:    true,
			wantErrStr: "usage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args, _ := tt.setup(t, dir)

			var stdout bytes.Buffer
			err := doAuthAdd(args, &stdout)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantErrStr != "" && !strings.Contains(err.Error(), tt.wantErrStr) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErrStr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			out := stdout.String()
			if tt.wantOutput != "" && !strings.Contains(out, tt.wantOutput) {
				t.Errorf("output %q should contain %q", out, tt.wantOutput)
			}
		})
	}
}

func TestDoAuthAdd_FileContainsPeer(t *testing.T) {
	// Verify the peer ID is actually written to the file after add.
	dir := t.TempDir()
	peerID := generateTestPeerID(t)
	akPath := filepath.Join(dir, "authorized_keys")

	var stdout bytes.Buffer
	if err := doAuthAdd([]string{peerID, "--file", akPath}, &stdout); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(akPath)
	if err != nil {
		t.Fatalf("read authorized_keys: %v", err)
	}
	if !strings.Contains(string(data), peerID) {
		t.Errorf("authorized_keys should contain %q, got:\n%s", peerID, data)
	}
}

func TestDoAuthAdd_DuplicateRejected(t *testing.T) {
	dir := t.TempDir()
	peerID := generateTestPeerID(t)
	akPath := writeAuthKeysFile(t, dir, peerID+"\n")

	var stdout bytes.Buffer
	err := doAuthAdd([]string{peerID, "--file", akPath}, &stdout)
	if err == nil {
		t.Fatal("expected error for duplicate peer, got nil")
	}
	if !strings.Contains(err.Error(), "already authorized") {
		t.Errorf("error %q should contain %q", err.Error(), "already authorized")
	}
}

// ----- doAuthList tests -----

func TestDoAuthList(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, dir string) []string
		wantErr    bool
		wantOutput []string // substrings expected in stdout
	}{
		{
			name: "empty file",
			setup: func(t *testing.T, dir string) []string {
				akPath := writeAuthKeysFile(t, dir, "")
				return []string{"--file", akPath}
			},
			wantOutput: []string{"No authorized peers"},
		},
		{
			name: "file with one entry",
			setup: func(t *testing.T, dir string) []string {
				peerID := generateTestPeerID(t)
				akPath := writeAuthKeysFile(t, dir, peerID+"\n")
				return []string{"--file", akPath}
			},
			wantOutput: []string{"Authorized peers (1)", "1."},
		},
		{
			name: "file with two entries",
			setup: func(t *testing.T, dir string) []string {
				id1 := generateTestPeerID(t)
				id2 := generateTestPeerID(t)
				content := id1 + "  # home server\n" + id2 + "\n"
				akPath := writeAuthKeysFile(t, dir, content)
				return []string{"--file", akPath}
			},
			wantOutput: []string{"Authorized peers (2)", "home server", "1.", "2."},
		},
		{
			name: "entries with comments",
			setup: func(t *testing.T, dir string) []string {
				peerID := generateTestPeerID(t)
				content := peerID + "  # my laptop\n"
				akPath := writeAuthKeysFile(t, dir, content)
				return []string{"--file", akPath}
			},
			wantOutput: []string{"my laptop"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := tt.setup(t, dir)

			var stdout bytes.Buffer
			err := doAuthList(args, &stdout)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			out := stdout.String()
			for _, sub := range tt.wantOutput {
				if !strings.Contains(out, sub) {
					t.Errorf("output should contain %q, got:\n%s", sub, out)
				}
			}
		})
	}
}

// ----- doAuthRemove tests -----

func TestDoAuthRemove(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, dir string) []string
		wantErr    bool
		wantErrStr string
		wantOutput string
	}{
		{
			name: "remove existing peer",
			setup: func(t *testing.T, dir string) []string {
				peerID := generateTestPeerID(t)
				akPath := writeAuthKeysFile(t, dir, peerID+"\n")
				return []string{peerID, "--file", akPath}
			},
			wantOutput: "File:",
		},
		{
			name: "remove non-existent peer",
			setup: func(t *testing.T, dir string) []string {
				existing := generateTestPeerID(t)
				toRemove := generateTestPeerID(t)
				akPath := writeAuthKeysFile(t, dir, existing+"\n")
				return []string{toRemove, "--file", akPath}
			},
			wantErr:    true,
			wantErrStr: "peer not found",
		},
		{
			name: "missing peer ID arg",
			setup: func(t *testing.T, dir string) []string {
				akPath := writeAuthKeysFile(t, dir, "")
				return []string{"--file", akPath}
			},
			wantErr:    true,
			wantErrStr: "usage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := tt.setup(t, dir)

			var stdout bytes.Buffer
			err := doAuthRemove(args, &stdout)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantErrStr != "" && !strings.Contains(err.Error(), tt.wantErrStr) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErrStr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			out := stdout.String()
			if tt.wantOutput != "" && !strings.Contains(out, tt.wantOutput) {
				t.Errorf("output %q should contain %q", out, tt.wantOutput)
			}
		})
	}
}

func TestDoAuthRemove_PeerActuallyRemoved(t *testing.T) {
	// Verify the peer ID is actually gone from the file after removal.
	dir := t.TempDir()
	peerID := generateTestPeerID(t)
	otherID := generateTestPeerID(t)
	akPath := writeAuthKeysFile(t, dir, peerID+"\n"+otherID+"\n")

	var stdout bytes.Buffer
	if err := doAuthRemove([]string{peerID, "--file", akPath}, &stdout); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(akPath)
	if err != nil {
		t.Fatalf("read authorized_keys: %v", err)
	}
	if strings.Contains(string(data), peerID) {
		t.Errorf("authorized_keys should not contain removed peer %q, got:\n%s", peerID, data)
	}
	if !strings.Contains(string(data), otherID) {
		t.Errorf("authorized_keys should still contain %q, got:\n%s", otherID, data)
	}
}

// ----- doAuthValidate tests -----

func TestDoAuthValidate(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, dir string) []string
		wantErr    bool
		wantErrStr string
		wantOutput []string // substrings expected in stdout
	}{
		{
			name: "valid file with entries",
			setup: func(t *testing.T, dir string) []string {
				id1 := generateTestPeerID(t)
				id2 := generateTestPeerID(t)
				content := id1 + "  # node A\n" + id2 + "\n"
				akPath := writeAuthKeysFile(t, dir, content)
				return []string{"--file", akPath}
			},
			wantOutput: []string{"Valid peer IDs: 2"},
		},
		{
			name: "empty file is valid",
			setup: func(t *testing.T, dir string) []string {
				akPath := writeAuthKeysFile(t, dir, "")
				return []string{"--file", akPath}
			},
			wantOutput: []string{"Valid peer IDs: 0"},
		},
		{
			name: "file with invalid peer ID",
			setup: func(t *testing.T, dir string) []string {
				content := "not-a-real-peer-id\n"
				akPath := writeAuthKeysFile(t, dir, content)
				return []string{"--file", akPath}
			},
			wantErr:    true,
			wantErrStr: "validation failed",
		},
		{
			name: "mixed valid and invalid entries",
			setup: func(t *testing.T, dir string) []string {
				validID := generateTestPeerID(t)
				content := validID + "\nbogus-id\n"
				akPath := writeAuthKeysFile(t, dir, content)
				return []string{"--file", akPath}
			},
			wantErr:    true,
			wantErrStr: "validation failed",
		},
		{
			name: "comments and blank lines are ignored",
			setup: func(t *testing.T, dir string) []string {
				validID := generateTestPeerID(t)
				content := "# header comment\n\n" + validID + "  # my node\n\n"
				akPath := writeAuthKeysFile(t, dir, content)
				return []string{"--file", akPath}
			},
			wantOutput: []string{"Valid peer IDs: 1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := tt.setup(t, dir)

			var stdout bytes.Buffer
			err := doAuthValidate(args, &stdout)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantErrStr != "" && !strings.Contains(err.Error(), tt.wantErrStr) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErrStr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			out := stdout.String()
			for _, sub := range tt.wantOutput {
				if !strings.Contains(out, sub) {
					t.Errorf("output should contain %q, got:\n%s", sub, out)
				}
			}
		})
	}
}

func TestDoAuthValidate_ErrorLinesInOutput(t *testing.T) {
	// When validation fails, the error details should be written to stdout.
	dir := t.TempDir()
	validID := generateTestPeerID(t)
	content := validID + "\ngarbage-peer-id\n"
	akPath := writeAuthKeysFile(t, dir, content)

	var stdout bytes.Buffer
	err := doAuthValidate([]string{"--file", akPath}, &stdout)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	out := stdout.String()
	if !strings.Contains(out, "Line 2") {
		t.Errorf("output should reference the invalid line, got:\n%s", out)
	}
}
