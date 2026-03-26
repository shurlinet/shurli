package grants

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// AuditEvent is the type of operation recorded in the audit log.
type AuditEvent string

const (
	AuditGrantCreated  AuditEvent = "grant_created"
	AuditGrantRevoked  AuditEvent = "grant_revoked"
	AuditGrantExtended AuditEvent = "grant_extended"
	AuditGrantRefreshed AuditEvent = "grant_refreshed"
	AuditGrantExpired  AuditEvent = "grant_expired"
)

// AuditEntry is a single integrity-chained audit log entry.
// Each entry's HMAC covers the previous entry's hash, creating a tamper-evident chain.
type AuditEntry struct {
	Timestamp time.Time         `json:"timestamp"`
	Event     AuditEvent        `json:"event"`
	PeerID    string            `json:"peer_id"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	PrevHash  string            `json:"prev_hash"` // hex-encoded hash of previous entry (empty for first)
	EntryHMAC string            `json:"entry_hmac"` // HMAC-SHA256(key, prev_hash + entry_data)
}

// AuditLog is an append-only, integrity-chained audit log for grant operations.
// Each entry's HMAC covers the previous hash + current entry data, so tampering
// with any entry breaks the chain from that point forward.
type AuditLog struct {
	mu       sync.Mutex
	path     string
	hmacKey  []byte
	lastHash string // hash of last appended entry
}

// NewAuditLog creates a new audit log. If the file exists, it reads the last
// entry's hash to continue the chain. If the file doesn't exist, it starts fresh.
func NewAuditLog(path string, hmacKey []byte) (*AuditLog, error) {
	al := &AuditLog{
		path:    path,
		hmacKey: hmacKey,
	}

	// Read last hash from existing file.
	if _, err := os.Stat(path); err == nil {
		lastHash, err := al.readLastHash()
		if err != nil {
			return nil, fmt.Errorf("audit log: read last hash: %w", err)
		}
		al.lastHash = lastHash
	}

	return al, nil
}

// Append adds a new entry to the audit log. Thread-safe.
func (al *AuditLog) Append(event AuditEvent, peerID peer.ID, metadata map[string]string) error {
	al.mu.Lock()
	defer al.mu.Unlock()

	entry := AuditEntry{
		Timestamp: time.Now().UTC(),
		Event:     event,
		PeerID:    shortPeerID(peerID),
		Metadata:  metadata,
		PrevHash:  al.lastHash,
	}

	// Compute HMAC: key over (prev_hash + entry_data_without_hmac).
	entryData := al.entryDataForHMAC(entry)
	mac := hmac.New(sha256.New, al.hmacKey)
	mac.Write([]byte(entry.PrevHash))
	mac.Write(entryData)
	entry.EntryHMAC = hex.EncodeToString(mac.Sum(nil))

	// Marshal the complete entry (with HMAC).
	jsonBytes, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("audit log: marshal entry: %w", err)
	}

	// A2 mitigation: reject symlinks before opening for write.
	if info, lErr := os.Lstat(al.path); lErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("audit log file is a symlink, refusing to write")
		}
	}

	// Append to file. Only update lastHash AFTER successful write+sync
	// to prevent in-memory/on-disk divergence on write failure.
	f, err := os.OpenFile(al.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("audit log: open file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(jsonBytes, '\n')); err != nil {
		return fmt.Errorf("audit log: write entry: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("audit log: sync: %w", err)
	}

	// Write succeeded. Now safe to update in-memory chain state.
	h := sha256.New()
	h.Write(jsonBytes)
	al.lastHash = hex.EncodeToString(h.Sum(nil))

	return nil
}

// Verify reads the entire audit log and validates the HMAC chain.
// Returns the number of valid entries and an error if the chain is broken.
func (al *AuditLog) Verify() (int, error) {
	al.mu.Lock()
	defer al.mu.Unlock()

	f, err := os.Open(al.path)
	if os.IsNotExist(err) {
		return 0, nil // no log yet
	}
	if err != nil {
		return 0, fmt.Errorf("audit log: open: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Allow large lines (entries with lots of metadata).
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	prevHash := ""
	count := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return count, fmt.Errorf("audit log: entry %d: invalid JSON: %w", count+1, err)
		}

		// Verify chain linkage.
		if entry.PrevHash != prevHash {
			return count, fmt.Errorf("audit log: entry %d: chain broken (expected prev_hash %q, got %q)",
				count+1, prevHash, entry.PrevHash)
		}

		// Verify HMAC.
		entryData := al.entryDataForHMAC(entry)
		mac := hmac.New(sha256.New, al.hmacKey)
		mac.Write([]byte(entry.PrevHash))
		mac.Write(entryData)
		expectedHMAC := hex.EncodeToString(mac.Sum(nil))

		if !hmac.Equal([]byte(entry.EntryHMAC), []byte(expectedHMAC)) {
			return count, fmt.Errorf("audit log: entry %d: HMAC verification failed (tampered)", count+1)
		}

		// Compute this entry's hash for next chain link.
		fullJSON, _ := json.Marshal(entry)
		h := sha256.New()
		h.Write(fullJSON)
		prevHash = hex.EncodeToString(h.Sum(nil))

		count++
	}

	if err := scanner.Err(); err != nil {
		return count, fmt.Errorf("audit log: read: %w", err)
	}

	return count, nil
}

// Entries reads and returns all audit log entries. Used by the CLI for display.
func (al *AuditLog) Entries() ([]AuditEntry, error) {
	al.mu.Lock()
	defer al.mu.Unlock()

	f, err := os.Open(al.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("audit log: open: %w", err)
	}
	defer f.Close()

	var entries []AuditEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue // skip malformed entries in read mode
		}
		entries = append(entries, entry)
	}

	return entries, scanner.Err()
}

// readLastHash reads the file and returns the hash of the last entry.
func (al *AuditLog) readLastHash() (string, error) {
	f, err := os.Open(al.path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var lastLine string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lastLine = line
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if lastLine == "" {
		return "", nil // empty file
	}

	// Compute hash of last entry.
	h := sha256.New()
	h.Write([]byte(lastLine))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// auditHMACPayload is the canonical form for HMAC computation.
// Excludes EntryHMAC to avoid circular dependency.
type auditHMACPayload struct {
	Timestamp time.Time         `json:"timestamp"`
	Event     AuditEvent        `json:"event"`
	PeerID    string            `json:"peer_id"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	PrevHash  string            `json:"prev_hash"`
}

// entryDataForHMAC returns the canonical bytes for HMAC computation.
// Excludes the EntryHMAC field itself to avoid circular dependency.
func (al *AuditLog) entryDataForHMAC(entry AuditEntry) []byte {
	payload := auditHMACPayload{
		Timestamp: entry.Timestamp,
		Event:     entry.Event,
		PeerID:    entry.PeerID,
		Metadata:  entry.Metadata,
		PrevHash:  entry.PrevHash,
	}
	data, _ := json.Marshal(payload)
	return data
}
