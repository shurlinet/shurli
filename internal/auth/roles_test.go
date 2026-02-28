package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestGetPeerRoleDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid := genPeerIDStr(t)
	if err := AddPeer(path, pid, "no-role"); err != nil {
		t.Fatal(err)
	}

	peerID, _ := peer.Decode(pid)
	role := GetPeerRole(path, peerID)
	if role != RoleMember {
		t.Errorf("default role = %q, want %q", role, RoleMember)
	}
}

func TestSetAndGetPeerRole(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid := genPeerIDStr(t)
	if err := AddPeer(path, pid, "test"); err != nil {
		t.Fatal(err)
	}

	if err := SetPeerRole(path, pid, RoleAdmin); err != nil {
		t.Fatalf("SetPeerRole: %v", err)
	}

	peerID, _ := peer.Decode(pid)
	role := GetPeerRole(path, peerID)
	if role != RoleAdmin {
		t.Errorf("role = %q, want %q", role, RoleAdmin)
	}
}

func TestSetPeerRoleInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid := genPeerIDStr(t)
	AddPeer(path, pid, "")

	err := SetPeerRole(path, pid, "superadmin")
	if err == nil {
		t.Error("expected error for invalid role")
	}
}

func TestIsAdmin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid := genPeerIDStr(t)
	AddPeer(path, pid, "")
	SetPeerRole(path, pid, RoleAdmin)

	peerID, _ := peer.Decode(pid)
	if !IsAdmin(path, peerID) {
		t.Error("expected IsAdmin = true")
	}
}

func TestIsAdminFalseForMember(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid := genPeerIDStr(t)
	AddPeer(path, pid, "")

	peerID, _ := peer.Decode(pid)
	if IsAdmin(path, peerID) {
		t.Error("expected IsAdmin = false for member")
	}
}

func TestCountAdmins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid1 := genPeerIDStr(t)
	pid2 := genPeerIDStr(t)
	pid3 := genPeerIDStr(t)
	AddPeer(path, pid1, "admin-1")
	AddPeer(path, pid2, "member-1")
	AddPeer(path, pid3, "admin-2")

	SetPeerRole(path, pid1, RoleAdmin)
	SetPeerRole(path, pid3, RoleAdmin)

	count, err := CountAdmins(path)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("admin count = %d, want 2", count)
	}
}

func TestCountAdminsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid := genPeerIDStr(t)
	AddPeer(path, pid, "plain")

	count, err := CountAdmins(path)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("admin count = %d, want 0", count)
	}
}

func TestCountAdminsMissingFile(t *testing.T) {
	count, err := CountAdmins("/nonexistent/authorized_keys")
	if err != nil {
		t.Fatal(err) // ListPeers returns nil, nil for missing file
	}
	if count != 0 {
		t.Errorf("admin count = %d, want 0", count)
	}
}

func TestGetPeerRoleMissingFile(t *testing.T) {
	peerID, _ := peer.Decode(genPeerIDStr(t))
	role := GetPeerRole("/nonexistent/authorized_keys", peerID)
	if role != RoleMember {
		t.Errorf("role = %q, want %q for missing file", role, RoleMember)
	}
}

func TestRoleAttributePersistsInFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid := genPeerIDStr(t)
	AddPeer(path, pid, "node")
	SetPeerRole(path, pid, RoleAdmin)

	// Verify raw file content contains role=admin
	data, _ := os.ReadFile(path)
	content := string(data)
	if !contains(content, "role=admin") {
		t.Errorf("file should contain role=admin, got:\n%s", content)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
