package grants

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func genAuditKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestAuditAppendAndVerify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	key := genAuditKey(t)

	al, err := NewAuditLog(path, key)
	if err != nil {
		t.Fatal(err)
	}

	pid := genPeerID(t)

	// Append several entries.
	if err := al.Append(AuditGrantCreated, pid, map[string]string{"permanent": "false"}); err != nil {
		t.Fatal(err)
	}
	if err := al.Append(AuditGrantExtended, pid, map[string]string{"expires_at": "2026-12-31T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := al.Append(AuditGrantRevoked, pid, nil); err != nil {
		t.Fatal(err)
	}

	// Verify chain.
	count, err := al.Verify()
	if err != nil {
		t.Fatalf("verify failed: %v (count: %d)", err, count)
	}
	if count != 3 {
		t.Fatalf("expected 3 entries, got %d", count)
	}
}

func TestAuditTamperDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	key := genAuditKey(t)

	al, err := NewAuditLog(path, key)
	if err != nil {
		t.Fatal(err)
	}

	pid := genPeerID(t)
	al.Append(AuditGrantCreated, pid, nil)
	al.Append(AuditGrantRevoked, pid, nil)

	// Tamper with the file: modify a byte in the first line.
	data, _ := os.ReadFile(path)
	if len(data) > 20 {
		data[10] = 'X'
	}
	os.WriteFile(path, data, 0600)

	count, err := al.Verify()
	if err == nil {
		t.Fatal("should detect tampered file")
	}
	_ = count // may be 0 or 1 depending on where the tamper hit
}

func TestAuditWrongKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	key1 := genAuditKey(t)
	key2 := genAuditKey(t)

	al1, _ := NewAuditLog(path, key1)
	pid := genPeerID(t)
	al1.Append(AuditGrantCreated, pid, nil)

	// Try to verify with wrong key.
	al2, _ := NewAuditLog(path, key2)
	_, err := al2.Verify()
	if err == nil {
		t.Fatal("should fail with wrong key")
	}
}

func TestAuditEmptyLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	key := genAuditKey(t)

	al, _ := NewAuditLog(path, key)
	count, err := al.Verify()
	if err != nil {
		t.Fatalf("verify empty: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}
}

func TestAuditResumesChain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	key := genAuditKey(t)

	// First session: write 2 entries.
	al1, _ := NewAuditLog(path, key)
	pid := genPeerID(t)
	al1.Append(AuditGrantCreated, pid, nil)
	al1.Append(AuditGrantExtended, pid, nil)

	// Second session: re-open and append more.
	al2, err := NewAuditLog(path, key)
	if err != nil {
		t.Fatal(err)
	}
	al2.Append(AuditGrantRevoked, pid, nil)

	// Verify the entire chain.
	count, err := al2.Verify()
	if err != nil {
		t.Fatalf("verify after resume: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3, got %d", count)
	}
}

func TestAuditEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	key := genAuditKey(t)

	al, _ := NewAuditLog(path, key)
	pid := genPeerID(t)
	al.Append(AuditGrantCreated, pid, map[string]string{"test": "value"})
	al.Append(AuditGrantRefreshed, pid, nil)

	entries, err := al.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Event != AuditGrantCreated {
		t.Fatalf("expected grant_created, got %s", entries[0].Event)
	}
	if entries[0].Metadata["test"] != "value" {
		t.Fatal("metadata not preserved")
	}
	if entries[1].Event != AuditGrantRefreshed {
		t.Fatalf("expected grant_refreshed, got %s", entries[1].Event)
	}
}
