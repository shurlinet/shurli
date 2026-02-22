package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const snapshotTimeFormat = "2006-01-02_150405"

// SnapshotManager manages TimeMachine-style backup snapshots in a directory.
// Each snapshot is a timestamped subdirectory containing copies of config files.
type SnapshotManager struct {
	backupDir string
}

// Snapshot represents a single timestamped backup.
type Snapshot struct {
	Name      string    // directory name, e.g. "2026-02-22_031500"
	Path      string    // full path to the snapshot directory
	Timestamp time.Time // parsed from the directory name
	Files     []string  // filenames present in the snapshot
}

// NewSnapshotManager creates a manager rooted at the given backup directory.
// Does not create the directory until Create is called.
func NewSnapshotManager(backupDir string) *SnapshotManager {
	return &SnapshotManager{backupDir: backupDir}
}

// Create takes a snapshot of the specified files from sourceDir.
// Only backs up files that actually exist (partial snapshots are OK).
// Returns the snapshot metadata. Creates backupDir if needed.
func (sm *SnapshotManager) Create(sourceDir string, filenames []string) (*Snapshot, error) {
	now := time.Now().UTC()
	name := now.Format(snapshotTimeFormat)
	snapDir := filepath.Join(sm.backupDir, name)

	// Handle timestamp collision (multiple snapshots in the same second)
	if _, err := os.Stat(snapDir); err == nil {
		for i := 1; i <= 99; i++ {
			candidate := fmt.Sprintf("%s_%02d", name, i)
			candidateDir := filepath.Join(sm.backupDir, candidate)
			if _, err := os.Stat(candidateDir); os.IsNotExist(err) {
				name = candidate
				snapDir = candidateDir
				break
			}
		}
	}

	if err := os.MkdirAll(snapDir, 0700); err != nil {
		return nil, fmt.Errorf("create snapshot dir: %w", err)
	}

	var copied []string
	for _, fname := range filenames {
		src := filepath.Join(sourceDir, fname)
		data, err := os.ReadFile(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue // skip files that don't exist
			}
			return nil, fmt.Errorf("read %s: %w", fname, err)
		}

		dst := filepath.Join(snapDir, fname)
		// Atomic write: temp file + rename
		tmp := dst + ".tmp"
		if err := os.WriteFile(tmp, data, 0600); err != nil {
			return nil, fmt.Errorf("write %s: %w", fname, err)
		}
		if err := os.Rename(tmp, dst); err != nil {
			os.Remove(tmp)
			return nil, fmt.Errorf("rename %s: %w", fname, err)
		}
		copied = append(copied, fname)
	}

	return &Snapshot{
		Name:      name,
		Path:      snapDir,
		Timestamp: now,
		Files:     copied,
	}, nil
}

// List returns all snapshots sorted by timestamp (newest first).
// Returns an empty slice (not an error) if the backup directory doesn't exist.
func (sm *SnapshotManager) List() ([]Snapshot, error) {
	entries, err := os.ReadDir(sm.backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read backup dir: %w", err)
	}

	var snapshots []Snapshot
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		ts, err := parseSnapshotName(name)
		if err != nil {
			continue // skip directories that don't match the timestamp format
		}

		snapDir := filepath.Join(sm.backupDir, name)
		files, err := listFilesInDir(snapDir)
		if err != nil {
			continue
		}

		snapshots = append(snapshots, Snapshot{
			Name:      name,
			Path:      snapDir,
			Timestamp: ts,
			Files:     files,
		})
	}

	// Sort newest first
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Timestamp.After(snapshots[j].Timestamp)
	})

	return snapshots, nil
}

// Restore copies files from a snapshot back to targetDir.
// Only restores files that exist in the snapshot.
// Uses atomic write (temp file + rename) for each file.
// The caller is responsible for creating a safety-net backup before calling this.
func (sm *SnapshotManager) Restore(snapshot *Snapshot, targetDir string) error {
	for _, fname := range snapshot.Files {
		src := filepath.Join(snapshot.Path, fname)
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read snapshot %s/%s: %w", snapshot.Name, fname, err)
		}

		dst := filepath.Join(targetDir, fname)
		tmp := dst + ".tmp"
		if err := os.WriteFile(tmp, data, 0600); err != nil {
			return fmt.Errorf("write %s: %w", fname, err)
		}
		if err := os.Rename(tmp, dst); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("rename %s: %w", fname, err)
		}
	}
	return nil
}

// parseSnapshotName parses a snapshot directory name into a timestamp.
// Handles both "2006-01-02_150405" and "2006-01-02_150405_NN" (collision suffix).
func parseSnapshotName(name string) (time.Time, error) {
	// Try exact match first
	ts, err := time.Parse(snapshotTimeFormat, name)
	if err == nil {
		return ts, nil
	}
	// Try with _NN collision suffix (e.g., "2026-02-22_031500_01")
	if len(name) > len(snapshotTimeFormat)+1 && name[len(snapshotTimeFormat)] == '_' {
		base := name[:len(snapshotTimeFormat)]
		return time.Parse(snapshotTimeFormat, base)
	}
	return time.Time{}, fmt.Errorf("not a snapshot directory: %s", name)
}

// listFilesInDir returns the names of regular files in a directory.
func listFilesInDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		files = append(files, entry.Name())
	}
	return files, nil
}
