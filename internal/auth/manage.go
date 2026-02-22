package auth

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// PeerEntry represents an authorized peer with optional comment and attributes.
type PeerEntry struct {
	PeerID    peer.ID
	Comment   string
	ExpiresAt time.Time // zero = never expires
	Verified  string    // empty = unverified, otherwise fingerprint prefix
}

// sanitizeComment strips characters that could corrupt the authorized_keys
// file format: newlines (line injection), carriage returns, and null bytes.
func sanitizeComment(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == 0 {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
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

	// Write attributes in stable order: expires, verified, then others alphabetically
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
	for k, v := range attrs {
		if k == "expires" || k == "verified" {
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
				attrs[key] = value
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

// atomicWriteLines writes lines to a file atomically via temp file + rename.
func atomicWriteLines(path string, lines []string) error {
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
	tempFile.Close()

	if err := os.Rename(tempPath, path); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to update file: %w", err)
	}

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
	defer f.Close()

	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("failed to write entry: %w", err)
	}

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

		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading file: %w", err)
	}

	return entries, nil
}
