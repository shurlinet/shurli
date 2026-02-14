package auth

import (
	"os"
	"path/filepath"
	"testing"

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
