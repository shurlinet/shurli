package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ArchivePath returns the last-known-good archive path for a config file.
// Example: /home/user/.config/shurli/config.yaml → /home/user/.config/shurli/.config.last-good.yaml
func ArchivePath(configPath string) string {
	dir := filepath.Dir(configPath)
	base := filepath.Base(configPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	return filepath.Join(dir, "."+name+".last-good"+ext)
}

// Archive copies configPath to its last-known-good archive location.
// The write is atomic (write to temp file, then rename) to prevent
// partial writes from corrupting the archive.
func Archive(configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("archive: read config: %w", err)
	}

	archivePath := ArchivePath(configPath)

	// Atomic write: temp file + rename
	tmp := archivePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("archive: write temp: %w", err)
	}
	if err := os.Rename(tmp, archivePath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("archive: rename: %w", err)
	}
	return nil
}

// Rollback restores the last-known-good archive over the current config.
// Returns ErrNoArchive if no archive exists.
func Rollback(configPath string) error {
	archivePath := ArchivePath(configPath)
	data, err := os.ReadFile(archivePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNoArchive, archivePath)
		}
		return fmt.Errorf("rollback: read archive: %w", err)
	}

	// Atomic write to config path
	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("rollback: write temp: %w", err)
	}
	if err := os.Rename(tmp, configPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rollback: rename: %w", err)
	}
	return nil
}

// HasArchive checks if a last-known-good archive exists for the given config.
func HasArchive(configPath string) bool {
	_, err := os.Stat(ArchivePath(configPath))
	return err == nil
}
