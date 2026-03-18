package filetransfer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shurlinet/shurli/pkg/plugin"
)

// partialManifest is the .shurli-partial checkpoint manifest for crash recovery.
// Written during active transfers so incomplete files can be identified and cleaned.
type partialManifest struct {
	TransferID string    `json:"transfer_id"`
	Filename   string    `json:"filename"`
	TempPath   string    `json:"temp_path"` // path to the incomplete .tmp file
	PeerID     string    `json:"peer_id"`
	Size       int64     `json:"size"`
	StartedAt  time.Time `json:"started_at"`
}

// writeCheckpoint creates a .shurli-partial manifest for an active transfer.
// P13 fix: TransferID is sanitized before use in file paths.
func writeCheckpoint(configDir string, m partialManifest) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}

	safeID := sanitizeTransferID(m.TransferID)
	path := filepath.Join(configDir, fmt.Sprintf(".shurli-partial-%s", safeID))
	if !isInsideDir(configDir, path) {
		return fmt.Errorf("checkpoint path escapes configDir")
	}
	return plugin.AtomicWriteFile(path, data, 0600)
}

// removeCheckpoint removes a .shurli-partial manifest after transfer completion.
// P13 fix: TransferID is sanitized before use in file paths.
func removeCheckpoint(configDir, transferID string) {
	safeID := sanitizeTransferID(transferID)
	path := filepath.Join(configDir, fmt.Sprintf(".shurli-partial-%s", safeID))
	if !isInsideDir(configDir, path) {
		return
	}
	os.Remove(path)
}

// loadCheckpoints reads all .shurli-partial manifests from the config dir.
// Used during Start() to discover interrupted transfers for cleanup or resume.
func loadCheckpoints(configDir string) ([]partialManifest, error) {
	pattern := filepath.Join(configDir, ".shurli-partial-*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}

	var manifests []partialManifest
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m partialManifest
		if err := json.Unmarshal(data, &m); err != nil {
			// Corrupt manifest - remove it.
			os.Remove(path)
			continue
		}
		manifests = append(manifests, m)
	}
	return manifests, nil
}

// isInsideDir checks whether path is inside dir after resolving symlinks and cleaning.
// Returns false if path escapes dir via traversal, symlinks, or absolute paths.
func isInsideDir(dir, path string) bool {
	cleanDir := filepath.Clean(dir)
	cleanPath := filepath.Clean(path)
	// Check prefix match with path separator to avoid "dir2" matching "dir".
	return cleanPath == cleanDir || strings.HasPrefix(cleanPath, cleanDir+string(os.PathSeparator))
}

// isForbiddenSystemPath returns true for paths under system directories that should
// never be sent via file transfer. Prevents accidental/malicious file exfiltration (P11 fix).
func isForbiddenSystemPath(path string) bool {
	forbidden := []string{"/etc/", "/var/", "/proc/", "/sys/", "/dev/", "/boot/", "/sbin/", "/usr/sbin/"}
	clean := filepath.Clean(path)
	for _, prefix := range forbidden {
		if strings.HasPrefix(clean, prefix) || clean == strings.TrimSuffix(prefix, "/") {
			return true
		}
	}
	return false
}

// sanitizeTransferID strips path separators and ".." from a TransferID to prevent
// checkpoint path traversal (P13 fix). Also enforces length limit.
func sanitizeTransferID(id string) string {
	if id == "" {
		id = "unknown"
	}
	// Replace any path separators and null bytes.
	id = strings.ReplaceAll(id, "/", "_")
	id = strings.ReplaceAll(id, "\\", "_")
	id = strings.ReplaceAll(id, "\x00", "_")
	id = strings.ReplaceAll(id, "..", "_")
	// Prevent absurdly long filenames.
	if len(id) > 255 {
		id = id[:255]
	}
	return id
}

// cleanStaleCheckpoints removes checkpoint files and their associated temp files.
// Called during Start() for any transfers that were interrupted by a crash.
// C3 fix: TempPath is validated to be inside configDir before deletion.
func cleanStaleCheckpoints(configDir string) {
	manifests, err := loadCheckpoints(configDir)
	if err != nil {
		return
	}
	for _, m := range manifests {
		// Remove the temp file ONLY if it's inside configDir (C3 fix).
		if m.TempPath != "" && isInsideDir(configDir, m.TempPath) {
			os.Remove(m.TempPath)
		}
		// Remove the checkpoint manifest.
		removeCheckpoint(configDir, m.TransferID)
	}
}
