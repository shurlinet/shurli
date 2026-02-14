package auth

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
)

// PeerEntry represents an authorized peer with optional comment.
type PeerEntry struct {
	PeerID  peer.ID
	Comment string
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

// AddPeer validates and appends a peer ID to the authorized_keys file.
// Returns nil if the peer was added, or an error if invalid/duplicate.
func AddPeer(authKeysPath, peerIDStr, comment string) error {
	peerID, err := peer.Decode(peerIDStr)
	if err != nil {
		return fmt.Errorf("invalid peer ID: %w", err)
	}

	// Check for duplicates if file exists
	if _, err := os.Stat(authKeysPath); err == nil {
		existing, err := LoadAuthorizedKeys(authKeysPath)
		if err != nil {
			return fmt.Errorf("failed to read existing file: %w", err)
		}
		if existing[peerID] {
			return fmt.Errorf("peer already authorized: %s", peerID.String()[:16]+"...")
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
		return fmt.Errorf("invalid peer ID: %w", err)
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
		trimmed := strings.TrimSpace(line)

		// Keep empty lines and full-line comments
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			newLines = append(newLines, line)
			continue
		}

		// Extract peer ID part
		parts := strings.SplitN(trimmed, "#", 2)
		peerPart := strings.TrimSpace(parts[0])

		if peerPart == "" {
			newLines = append(newLines, line)
			continue
		}

		peerID, err := peer.Decode(peerPart)
		if err != nil {
			// Invalid peer ID line — keep it
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
		return fmt.Errorf("peer not found: %s", targetID.String()[:16]+"...")
	}

	// Atomic write via temp file + rename.
	// Use os.CreateTemp in the same directory to avoid predictable temp paths
	// (symlink attack) and ensure same-filesystem rename.
	dir := filepath.Dir(authKeysPath)
	tempFile, err := os.CreateTemp(dir, ".authorized_keys.*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()

	// Ensure correct permissions on temp file
	if err := tempFile.Chmod(0600); err != nil {
		tempFile.Close()
		os.Remove(tempPath)
		return fmt.Errorf("failed to set temp file permissions: %w", err)
	}

	for _, line := range newLines {
		if _, err := tempFile.WriteString(line + "\n"); err != nil {
			tempFile.Close()
			os.Remove(tempPath)
			return fmt.Errorf("failed to write temp file: %w", err)
		}
	}
	tempFile.Close()

	if err := os.Rename(tempPath, authKeysPath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to update file: %w", err)
	}

	return nil
}

// ListPeers reads the authorized_keys file and returns all peer entries.
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
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "#", 2)
		peerIDStr := strings.TrimSpace(parts[0])
		if peerIDStr == "" {
			continue
		}

		peerID, err := peer.Decode(peerIDStr)
		if err != nil {
			continue // skip invalid lines
		}

		var comment string
		if len(parts) > 1 {
			comment = strings.TrimSpace(parts[1])
		}

		entries = append(entries, PeerEntry{PeerID: peerID, Comment: comment})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading file: %w", err)
	}

	return entries, nil
}
