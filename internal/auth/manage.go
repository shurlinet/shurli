package auth

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/libp2p/go-libp2p/core/peer"
)

// PeerEntry represents an authorized peer with optional comment and attributes.
type PeerEntry struct {
	PeerID    peer.ID
	Comment   string
	ExpiresAt time.Time // zero = never expires
	Verified  string    // empty = unverified, otherwise fingerprint prefix
	Group     string    // pairing group ID (empty = manually added or invited)
	Role      string    // "admin" or "member" (empty = member, backward compatible)
}

// maxCommentLen is the maximum length for a peer comment in authorized_keys.
const maxCommentLen = 512

// sanitizeComment strips characters that could corrupt the authorized_keys
// file format or inject terminal escape sequences: all C0 control characters
// (U+0000-U+001F, includes NUL, ESC, newlines, CR) and C1 control characters
// (U+007F-U+009F). Length is capped at maxCommentLen bytes.
func sanitizeComment(s string) string {
	s = truncateUTF8(s, maxCommentLen)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r <= 0x1F || (r >= 0x7F && r <= 0x9F) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// maxAttrValueLen is the maximum length for a peer attribute value.
const maxAttrValueLen = 256

// sanitizeAttrValue strips characters that could corrupt the authorized_keys
// file format or inject log entries. Attribute values must not contain
// control characters, spaces (they delimit fields), equals signs (they
// delimit key=value), or hash (starts comments). Length is capped at
// maxAttrValueLen bytes.
func sanitizeAttrValue(s string) string {
	s = truncateUTF8(s, maxAttrValueLen)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r <= 0x1F || (r >= 0x7F && r <= 0x9F) {
			continue
		}
		if r == ' ' || r == '=' || r == '#' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// truncateUTF8 truncates s to at most maxBytes without splitting a multi-byte rune.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

// parseLine parses a single authorized_keys line into its components.
// Format: <peer-id> [key=value ...] [# comment]
// Returns the peer ID string, attributes map, and comment. Returns empty
// peer ID string for comment-only or empty lines.
func parseLine(line string) (peerIDStr string, attrs map[string]string, comment string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", nil, ""
	}

	// Split on first # to separate data from comment
	parts := strings.SplitN(trimmed, "#", 2)
	dataPart := strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		comment = strings.TrimSpace(parts[1])
	}

	if dataPart == "" {
		return "", nil, comment
	}

	// Split data part on whitespace
	fields := strings.Fields(dataPart)
	peerIDStr = fields[0]

	// Remaining fields are key=value attributes
	for _, field := range fields[1:] {
		if k, v, ok := strings.Cut(field, "="); ok {
			if attrs == nil {
				attrs = make(map[string]string)
			}
			attrs[k] = v
		}
	}

	return peerIDStr, attrs, comment
}

// formatLine reconstructs an authorized_keys line from components.
func formatLine(peerIDStr string, attrs map[string]string, comment string) string {
	var b strings.Builder
	b.WriteString(peerIDStr)

	// Write attributes in stable order: expires, verified, role, then others alphabetically
	writeAttr := func(key string) {
		if v, ok := attrs[key]; ok {
			b.WriteString("  ")
			b.WriteString(key)
			b.WriteString("=")
			b.WriteString(v)
		}
	}
	writeAttr("expires")
	writeAttr("verified")
	writeAttr("role")
	for k, v := range attrs {
		if k == "expires" || k == "verified" || k == "role" {
			continue
		}
		b.WriteString("  ")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(v)
	}

	if comment != "" {
		b.WriteString("  # ")
		b.WriteString(comment)
	}

	return b.String()
}

// SetPeerAttr sets or updates an attribute on an existing peer in the
// authorized_keys file. Uses atomic write via temp file + rename.
func SetPeerAttr(authKeysPath, peerIDStr, key, value string) error {
	targetID, err := peer.Decode(peerIDStr)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPeerID, err)
	}

	file, err := os.Open(authKeysPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}

	var newLines []string
	scanner := bufio.NewScanner(file)
	found := false

	for scanner.Scan() {
		line := scanner.Text()
		pidStr, attrs, comment := parseLine(line)

		if pidStr == "" {
			newLines = append(newLines, line)
			continue
		}

		pid, err := peer.Decode(pidStr)
		if err != nil {
			newLines = append(newLines, line)
			continue
		}

		if pid == targetID {
			found = true
			if attrs == nil {
				attrs = make(map[string]string)
			}
			if value == "" {
				delete(attrs, key)
			} else {
				attrs[key] = sanitizeAttrValue(value)
			}
			newLines = append(newLines, formatLine(pidStr, attrs, comment))
		} else {
			newLines = append(newLines, line)
		}
	}
	file.Close()

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading file: %w", err)
	}

	if !found {
		return fmt.Errorf("%w: %s", ErrPeerNotFound, targetID.String()[:16]+"...")
	}

	return atomicWriteLines(authKeysPath, newLines)
}

// fileOwnership captures the UID/GID of a file before a write operation.
// Used by atomicWriteLines and SaveIntegrityHash to restore ownership
// when running as root on files owned by a service user.
type fileOwnership struct {
	uid int
	gid int
}

// WriteFilePreserveOwnership is like os.WriteFile but preserves file ownership
// when running as root on files owned by a different user. This prevents
// root-run CLI commands from flipping ownership on service user files.
func WriteFilePreserveOwnership(path string, data []byte, perm os.FileMode) error {
	orig := captureOwnership(path)
	if err := os.WriteFile(path, data, perm); err != nil {
		return err
	}
	restoreOwnership(path, orig)
	return nil
}

// captureOwnership returns the UID/GID of the file at path.
// Returns nil if the file doesn't exist or stat fails (new file, no restore needed).
func captureOwnership(path string) *fileOwnership {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	return platformOwnership(info)
}

// restoreOwnership restores the UID/GID of path if we're running as root
// and the original owner was a different user. This prevents root from
// flipping ownership on files owned by service users (e.g., relay running
// as "satinder" but admin runs "sudo shurli relay deauthorize").
func restoreOwnership(path string, orig *fileOwnership) {
	if orig == nil || os.Getuid() != 0 {
		return
	}
	if orig.uid == 0 {
		return // file was already root-owned, nothing to restore
	}
	if err := os.Chown(path, orig.uid, orig.gid); err != nil {
		slog.Warn("failed to restore file ownership", "path", filepath.Base(path), "err", err)
	}
}

// atomicWriteLines writes lines to a file atomically via temp file + rename.
// Preserves file ownership when running as root.
func atomicWriteLines(path string, lines []string) error {
	orig := captureOwnership(path)

	dir := filepath.Dir(path)
	tempFile, err := os.CreateTemp(dir, ".authorized_keys.*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()

	if err := tempFile.Chmod(0600); err != nil {
		tempFile.Close()
		os.Remove(tempPath)
		return fmt.Errorf("failed to set temp file permissions: %w", err)
	}

	for _, line := range lines {
		if _, err := tempFile.WriteString(line + "\n"); err != nil {
			tempFile.Close()
			os.Remove(tempPath)
			return fmt.Errorf("failed to write temp file: %w", err)
		}
	}
	if err := tempFile.Sync(); err != nil {
		tempFile.Close()
		os.Remove(tempPath)
		return fmt.Errorf("failed to sync temp file: %w", err)
	}
	tempFile.Close()

	if err := os.Rename(tempPath, path); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to update file: %w", err)
	}

	restoreOwnership(path, orig)
	SaveIntegrityHash(path)
	return nil
}

// AddPeer validates and appends a peer ID to the authorized_keys file.
// Returns nil if the peer was added, or an error if invalid/duplicate.
func AddPeer(authKeysPath, peerIDStr, comment string) error {
	peerID, err := peer.Decode(peerIDStr)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPeerID, err)
	}

	// Check for duplicates if file exists
	if _, err := os.Stat(authKeysPath); err == nil {
		existing, err := LoadAuthorizedKeys(authKeysPath)
		if err != nil {
			return fmt.Errorf("failed to read existing file: %w", err)
		}
		if existing[peerID] {
			return fmt.Errorf("%w: %s", ErrPeerAlreadyAuthorized, peerID.String()[:16]+"...")
		}
	}

	// Sanitize comment: strip newlines, carriage returns, and null bytes
	// to prevent line injection into authorized_keys.
	comment = sanitizeComment(comment)

	entry := peerID.String()
	if comment != "" {
		entry = fmt.Sprintf("%s  # %s", entry, comment)
	}
	entry += "\n"

	f, err := os.OpenFile(authKeysPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}

	if _, err := f.WriteString(entry); err != nil {
		f.Close()
		return fmt.Errorf("failed to write entry: %w", err)
	}
	f.Close()

	SaveIntegrityHash(authKeysPath)
	return nil
}

// RemovePeer removes a peer ID from the authorized_keys file using atomic write.
// Returns nil if the peer was removed, or an error if not found/invalid.
func RemovePeer(authKeysPath, peerIDStr string) error {
	targetID, err := peer.Decode(peerIDStr)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPeerID, err)
	}

	file, err := os.Open(authKeysPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}

	var newLines []string
	scanner := bufio.NewScanner(file)
	found := false

	for scanner.Scan() {
		line := scanner.Text()
		pidStr, _, _ := parseLine(line)

		if pidStr == "" {
			newLines = append(newLines, line)
			continue
		}

		peerID, err := peer.Decode(pidStr)
		if err != nil {
			newLines = append(newLines, line)
			continue
		}

		if peerID == targetID {
			found = true
			continue // skip this line
		}

		newLines = append(newLines, line)
	}
	file.Close()

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading file: %w", err)
	}

	if !found {
		return fmt.Errorf("%w: %s", ErrPeerNotFound, targetID.String()[:16]+"...")
	}

	return atomicWriteLines(authKeysPath, newLines)
}

// ListPeers reads the authorized_keys file and returns all peer entries
// including attributes (expires, verified).
func ListPeers(authKeysPath string) ([]PeerEntry, error) {
	file, err := os.Open(authKeysPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no file = no peers
		}
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	var entries []PeerEntry
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		pidStr, attrs, comment := parseLine(scanner.Text())
		if pidStr == "" {
			continue
		}

		peerID, err := peer.Decode(pidStr)
		if err != nil {
			continue // skip invalid lines
		}

		entry := PeerEntry{PeerID: peerID, Comment: comment}

		if v, ok := attrs["expires"]; ok {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				entry.ExpiresAt = t
			}
		}
		if v, ok := attrs["verified"]; ok {
			entry.Verified = v
		}
		if v, ok := attrs["group"]; ok {
			entry.Group = v
		}
		if v, ok := attrs["role"]; ok {
			entry.Role = v
		}
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading file: %w", err)
	}

	return entries, nil
}

// PeerComment returns the comment (friendly name) for a peer in authorized_keys.
// Returns empty string if the peer is not found or on error.
func PeerComment(authKeysPath string, peerID peer.ID) string {
	entries, err := ListPeers(authKeysPath)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.PeerID == peerID {
			return e.Comment
		}
	}
	return ""
}


// GetPeerAttr returns the value of a specific attribute for a peer.
// Returns empty string if the peer or attribute is not found.
// Uses string comparison on encoded peer IDs to avoid costly decoding per line.
func GetPeerAttr(authKeysPath, peerIDStr, key string) string {
	// Validate the target peer ID once upfront.
	targetID, err := peer.Decode(peerIDStr)
	if err != nil {
		return ""
	}
	canonical := targetID.String()

	file, err := os.Open(authKeysPath)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		pidStr, attrs, _ := parseLine(scanner.Text())
		if pidStr == canonical {
			if attrs == nil {
				return ""
			}
			return attrs[key]
		}
	}
	return ""
}

// --- Integrity monitoring ---

// hashFilePath returns the path to the integrity hash file for an authorized_keys file.
func hashFilePath(authKeysPath string) string {
	return authKeysPath + ".sha256"
}

// ComputeFileHash returns the SHA-256 hex digest of a file.
func ComputeFileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), nil
}

// SaveIntegrityHash computes and saves the SHA-256 hash of the authorized_keys file.
// Called after every mutation (add, remove, set-attr) to maintain the integrity record.
// Preserves file ownership when running as root.
func SaveIntegrityHash(authKeysPath string) {
	hash, err := ComputeFileHash(authKeysPath)
	if err != nil {
		slog.Warn("integrity: failed to hash authorized_keys", "err", err)
		return
	}
	hashPath := hashFilePath(authKeysPath)
	orig := captureOwnership(hashPath)
	if err := os.WriteFile(hashPath, []byte(hash+"\n"), 0600); err != nil {
		slog.Warn("integrity: failed to save hash", "err", err)
		return
	}
	restoreOwnership(hashPath, orig)
}

// VerifyIntegrity checks the authorized_keys file against its stored hash.
// Returns true if the hash matches or no hash file exists (first run).
// Returns false if the file was modified out-of-band (tampering detected).
func VerifyIntegrity(authKeysPath string) bool {
	stored, err := os.ReadFile(hashFilePath(authKeysPath))
	if err != nil {
		if os.IsNotExist(err) {
			return true // no hash yet, first run
		}
		slog.Warn("integrity: failed to read hash file", "err", err)
		return true // can't verify, don't block
	}

	current, err := ComputeFileHash(authKeysPath)
	if err != nil {
		slog.Warn("integrity: failed to hash authorized_keys", "err", err)
		return true
	}

	expected := strings.TrimSpace(string(stored))
	if current != expected {
		slog.Error("integrity: authorized_keys modified out-of-band (tampering detected)",
			"expected", expected[:16]+"...", "actual", current[:16]+"...")
		return false
	}
	return true
}
