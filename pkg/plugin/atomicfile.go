package plugin

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWriteFile writes data to a file atomically using temp file + fsync + rename.
// This ensures that a crash during write leaves either the old file or the new file,
// never a half-written file. Used for queue.json, shares.json, config writes.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	// Create temp file in the same directory (ensures same filesystem for rename).
	tmp, err := os.CreateTemp(dir, "."+base+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	// Clean up on any failure path.
	success := false
	defer func() {
		if !success {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	// Write data.
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	// Fsync the file to ensure data is on disk before rename.
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}

	// Set permissions before rename.
	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	// Atomic rename.
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}

	// Fsync the directory to ensure the rename is durable.
	dirFd, err := os.Open(dir)
	if err == nil {
		dirFd.Sync()
		dirFd.Close()
	}

	success = true
	return nil
}
