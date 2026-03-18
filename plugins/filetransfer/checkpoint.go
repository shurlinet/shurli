package filetransfer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
func writeCheckpoint(configDir string, m partialManifest) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}

	path := filepath.Join(configDir, fmt.Sprintf(".shurli-partial-%s", m.TransferID))
	return plugin.AtomicWriteFile(path, data, 0600)
}

// removeCheckpoint removes a .shurli-partial manifest after transfer completion.
func removeCheckpoint(configDir, transferID string) {
	path := filepath.Join(configDir, fmt.Sprintf(".shurli-partial-%s", transferID))
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

// cleanStaleCheckpoints removes checkpoint files and their associated temp files.
// Called during Start() for any transfers that were interrupted by a crash.
func cleanStaleCheckpoints(configDir string) {
	manifests, err := loadCheckpoints(configDir)
	if err != nil {
		return
	}
	for _, m := range manifests {
		// Remove the temp file if it exists.
		if m.TempPath != "" {
			os.Remove(m.TempPath)
		}
		// Remove the checkpoint manifest.
		removeCheckpoint(configDir, m.TransferID)
	}
}
