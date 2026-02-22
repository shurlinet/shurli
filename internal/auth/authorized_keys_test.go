package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func genPeerIDStr(t testing.TB) string {
	t.Helper()
	priv, _, _ := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	pid, _ := peer.IDFromPrivateKey(priv)
	return pid.String()
}

func writeAuthKeys(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAuthorizedKeys(t *testing.T) {
	pid1 := genPeerIDStr(t)
	pid2 := genPeerIDStr(t)

	dir := t.TempDir()
	content := "# comment line\n" + pid1 + "  # home server\n\n" + pid2 + "\n"
	path := writeAuthKeys(t, dir, content)

	peers, err := LoadAuthorizedKeys(path)
	if err != nil {
		t.Fatalf("LoadAuthorizedKeys: %v", err)
	}

	if len(peers) != 2 {
		t.Errorf("loaded %d peers, want 2", len(peers))
	}
}

func TestLoadAuthorizedKeysEmpty(t *testing.T) {
	dir := t.TempDir()
	path := writeAuthKeys(t, dir, "# only comments\n\n# another comment\n")

	peers, err := LoadAuthorizedKeys(path)
	if err != nil {
		t.Fatalf("LoadAuthorizedKeys: %v", err)
	}

	if len(peers) != 0 {
		t.Errorf("loaded %d peers, want 0", len(peers))
	}
}

func TestLoadAuthorizedKeysInvalidPeerID(t *testing.T) {
	dir := t.TempDir()
	path := writeAuthKeys(t, dir, "not-a-valid-peer-id\n")

	_, err := LoadAuthorizedKeys(path)
	if err == nil {
		t.Error("expected error for invalid peer ID")
	}
}

func TestLoadAuthorizedKeysMissingFile(t *testing.T) {
	_, err := LoadAuthorizedKeys("/nonexistent/authorized_keys")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestIsAuthorizedFunc(t *testing.T) {
	priv, _, _ := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	pid, _ := peer.IDFromPrivateKey(priv)

	peers := map[peer.ID]bool{pid: true}

	if !IsAuthorized(pid, peers) {
		t.Error("should be authorized")
	}

	priv2, _, _ := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	pid2, _ := peer.IDFromPrivateKey(priv2)

	if IsAuthorized(pid2, peers) {
		t.Error("should not be authorized")
	}
}

// --- Attribute parsing tests ---

func TestLoadAuthorizedKeysWithAttributes(t *testing.T) {
	pid1 := genPeerIDStr(t)
	pid2 := genPeerIDStr(t)

	dir := t.TempDir()
	content := pid1 + "  expires=2026-03-15T00:00:00Z  # contractor\n" +
		pid2 + "  verified=sha256:abc123  # mum\n"
	path := writeAuthKeys(t, dir, content)

	// LoadAuthorizedKeys still works (ignores attributes)
	peers, err := LoadAuthorizedKeys(path)
	if err != nil {
		t.Fatalf("LoadAuthorizedKeys: %v", err)
	}
	if len(peers) != 2 {
		t.Errorf("loaded %d peers, want 2", len(peers))
	}
}

func TestListPeersWithAttributes(t *testing.T) {
	pid1 := genPeerIDStr(t)
	pid2 := genPeerIDStr(t)
	pid3 := genPeerIDStr(t)

	dir := t.TempDir()
	content := pid1 + "  expires=2026-03-15T00:00:00Z  # contractor\n" +
		pid2 + "  verified=sha256:abc123  # mum\n" +
		pid3 + "  expires=2026-04-01T00:00:00Z  verified=sha256:def456  # temp\n"
	path := writeAuthKeys(t, dir, content)

	entries, err := ListPeers(path)
	if err != nil {
		t.Fatalf("ListPeers: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}

	// First entry: expires only
	if entries[0].ExpiresAt.IsZero() {
		t.Error("entry 0 should have ExpiresAt")
	}
	if entries[0].ExpiresAt.Year() != 2026 || entries[0].ExpiresAt.Month() != 3 {
		t.Errorf("entry 0 ExpiresAt = %v", entries[0].ExpiresAt)
	}
	if entries[0].Verified != "" {
		t.Errorf("entry 0 Verified = %q, want empty", entries[0].Verified)
	}
	if entries[0].Comment != "contractor" {
		t.Errorf("entry 0 Comment = %q, want contractor", entries[0].Comment)
	}

	// Second entry: verified only
	if !entries[1].ExpiresAt.IsZero() {
		t.Error("entry 1 should not have ExpiresAt")
	}
	if entries[1].Verified != "sha256:abc123" {
		t.Errorf("entry 1 Verified = %q, want sha256:abc123", entries[1].Verified)
	}
	if entries[1].Comment != "mum" {
		t.Errorf("entry 1 Comment = %q, want mum", entries[1].Comment)
	}

	// Third entry: both
	if entries[2].ExpiresAt.IsZero() {
		t.Error("entry 2 should have ExpiresAt")
	}
	if entries[2].Verified != "sha256:def456" {
		t.Errorf("entry 2 Verified = %q, want sha256:def456", entries[2].Verified)
	}
}

func TestListPeersNoAttributes(t *testing.T) {
	pid := genPeerIDStr(t)
	dir := t.TempDir()
	path := writeAuthKeys(t, dir, pid+"  # dad\n")

	entries, err := ListPeers(path)
	if err != nil {
		t.Fatalf("ListPeers: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Comment != "dad" {
		t.Errorf("Comment = %q, want dad", entries[0].Comment)
	}
	if !entries[0].ExpiresAt.IsZero() {
		t.Error("should not have ExpiresAt")
	}
	if entries[0].Verified != "" {
		t.Error("should not have Verified")
	}
}

func TestSetPeerAttr(t *testing.T) {
	pid := genPeerIDStr(t)
	dir := t.TempDir()
	path := writeAuthKeys(t, dir, pid+"  # dad\n")

	// Set verified attribute
	if err := SetPeerAttr(path, pid, "verified", "sha256:abcd1234"); err != nil {
		t.Fatalf("SetPeerAttr: %v", err)
	}

	// Read back and check
	entries, err := ListPeers(path)
	if err != nil {
		t.Fatalf("ListPeers: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Verified != "sha256:abcd1234" {
		t.Errorf("Verified = %q, want sha256:abcd1234", entries[0].Verified)
	}
	if entries[0].Comment != "dad" {
		t.Errorf("Comment = %q, want dad", entries[0].Comment)
	}

	// Set expires attribute (adding second attr)
	expiry := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	if err := SetPeerAttr(path, pid, "expires", expiry.Format(time.RFC3339)); err != nil {
		t.Fatalf("SetPeerAttr expires: %v", err)
	}

	entries, _ = ListPeers(path)
	if entries[0].ExpiresAt.IsZero() {
		t.Error("should have ExpiresAt after setting")
	}
	if entries[0].Verified != "sha256:abcd1234" {
		t.Error("verified should be preserved")
	}

	// Remove attribute by setting empty value
	if err := SetPeerAttr(path, pid, "verified", ""); err != nil {
		t.Fatalf("SetPeerAttr remove: %v", err)
	}
	entries, _ = ListPeers(path)
	if entries[0].Verified != "" {
		t.Errorf("verified should be empty after removal, got %q", entries[0].Verified)
	}
}

func TestSetPeerAttrNotFound(t *testing.T) {
	pid := genPeerIDStr(t)
	other := genPeerIDStr(t)
	dir := t.TempDir()
	path := writeAuthKeys(t, dir, pid+"  # dad\n")

	err := SetPeerAttr(path, other, "verified", "sha256:test")
	if err == nil {
		t.Error("should error for unknown peer")
	}
}

func TestParseLineFormats(t *testing.T) {
	pid := genPeerIDStr(t)

	tests := []struct {
		name       string
		line       string
		wantPeerID bool
		wantAttrs  int
		wantComment string
	}{
		{"empty", "", false, 0, ""},
		{"comment only", "# hello", false, 0, ""},
		{"peer only", pid, true, 0, ""},
		{"peer with comment", pid + "  # dad", true, 0, "dad"},
		{"peer with one attr", pid + "  expires=2026-01-01T00:00:00Z  # temp", true, 1, "temp"},
		{"peer with two attrs", pid + "  expires=2026-01-01T00:00:00Z  verified=sha256:abc  # both", true, 2, "both"},
		{"peer with attr no comment", pid + "  verified=sha256:xyz", true, 1, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pidStr, attrs, comment := parseLine(tt.line)
			hasPeer := pidStr != ""
			if hasPeer != tt.wantPeerID {
				t.Errorf("hasPeer = %v, want %v", hasPeer, tt.wantPeerID)
			}
			if len(attrs) != tt.wantAttrs {
				t.Errorf("attrs count = %d, want %d", len(attrs), tt.wantAttrs)
			}
			if comment != tt.wantComment {
				t.Errorf("comment = %q, want %q", comment, tt.wantComment)
			}
		})
	}
}

func TestFormatLineRoundTrip(t *testing.T) {
	pid := genPeerIDStr(t)
	attrs := map[string]string{
		"expires":  "2026-03-15T00:00:00Z",
		"verified": "sha256:abc123",
	}

	line := formatLine(pid, attrs, "dad")

	// Parse it back
	gotPID, gotAttrs, gotComment := parseLine(line)
	if gotPID != pid {
		t.Errorf("peer ID mismatch")
	}
	if gotAttrs["expires"] != "2026-03-15T00:00:00Z" {
		t.Errorf("expires mismatch: %q", gotAttrs["expires"])
	}
	if gotAttrs["verified"] != "sha256:abc123" {
		t.Errorf("verified mismatch: %q", gotAttrs["verified"])
	}
	if gotComment != "dad" {
		t.Errorf("comment = %q, want dad", gotComment)
	}

	// Verify order: expires before verified in output
	expiresIdx := strings.Index(line, "expires=")
	verifiedIdx := strings.Index(line, "verified=")
	if expiresIdx > verifiedIdx {
		t.Error("expires should appear before verified in formatted line")
	}
}
