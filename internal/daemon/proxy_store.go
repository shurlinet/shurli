package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
)

// proxyNameRe validates persistent proxy names: must start with a letter,
// followed by alphanumerics, hyphens, or underscores (max 64 chars).
// SEC-8: prevents path traversal, log injection, and shell injection.
var proxyNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

// ProxyEntry is a single persistent proxy stored in proxies.json.
type ProxyEntry struct {
	Name    string `json:"name"`
	Peer    string `json:"peer"`
	Service string `json:"service"`
	Port    int    `json:"port"`
	Enabled bool   `json:"enabled"`
}

// proxyStore manages persistent proxy entries on disk.
// Thread-safe via mutex. Writes are atomic (tmp+fsync+rename).
type proxyStore struct {
	mu      sync.Mutex
	path    string
	entries map[string]*ProxyEntry // keyed by name
}

// NewProxyStore creates a store backed by the given file path.
// If the file exists and is valid JSON, entries are loaded.
// If the file is missing, starts empty.
// If the file is corrupt, attempts recovery from .tmp backup.
func NewProxyStore(path string) (*proxyStore, error) {
	s := &proxyStore{
		path:    path,
		entries: make(map[string]*ProxyEntry),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// load reads proxies.json with O_NOFOLLOW to prevent symlink attacks (SEC-4).
func (s *proxyStore) load() error {
	data, err := readFileNoFollow(s.path)
	if err != nil {
		if os.IsNotExist(err) || isSymlinkError(err) {
			if isSymlinkError(err) {
				slog.Warn("proxies.json is a symlink, refusing to follow", "path", s.path)
			}
			return nil // start empty
		}
		return fmt.Errorf("read proxies.json: %w", err)
	}

	var entries []*ProxyEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		slog.Warn("proxies.json corrupt, attempting .tmp recovery", "error", err)
		return s.recoverFromTmp()
	}

	for _, e := range entries {
		s.entries[e.Name] = e
	}
	return nil
}

// recoverFromTmp tries to load from the .tmp backup file.
func (s *proxyStore) recoverFromTmp() error {
	tmpPath := s.path + ".tmp"
	data, err := readFileNoFollow(tmpPath)
	if err != nil {
		slog.Warn("proxies.json.tmp also unreadable, starting empty", "error", err)
		return nil
	}

	var entries []*ProxyEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		slog.Warn("proxies.json.tmp also corrupt, starting empty", "error", err)
		return nil
	}

	// Recover: rename tmp over corrupt file.
	if err := os.Rename(tmpPath, s.path); err != nil {
		slog.Warn("failed to recover proxies.json from .tmp", "error", err)
	}

	for _, e := range entries {
		s.entries[e.Name] = e
	}
	slog.Info("recovered proxies.json from .tmp backup", "count", len(entries))
	return nil
}

// save writes entries atomically: tmp + fsync + rename.
// The tmp file is opened with O_NOFOLLOW (SEC-4 upgrade from CVE-2026-32282).
func (s *proxyStore) save() error {
	entries := make([]*ProxyEntry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, e)
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal proxies: %w", err)
	}
	data = append(data, '\n')

	tmpPath := s.path + ".tmp"

	// O_NOFOLLOW: kernel refuses to follow symlinks atomically (no TOCTOU).
	// O_CREATE|O_WRONLY|O_TRUNC: standard write pattern.
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close tmp: %w", err)
	}

	// Atomic rename (POSIX guarantees).
	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename tmp: %w", err)
	}
	return nil
}

// Add creates a new proxy entry. Returns error if name already exists or is invalid.
func (s *proxyStore) Add(entry *ProxyEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !proxyNameRe.MatchString(entry.Name) {
		return fmt.Errorf("invalid proxy name %q: must match %s", entry.Name, proxyNameRe.String())
	}
	if _, exists := s.entries[entry.Name]; exists {
		return fmt.Errorf("proxy %q already exists", entry.Name)
	}

	s.entries[entry.Name] = entry
	return s.save()
}

// Remove deletes a proxy entry by name.
func (s *proxyStore) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.entries[name]; !exists {
		return fmt.Errorf("proxy %q not found", name)
	}
	delete(s.entries, name)
	return s.save()
}

// SetEnabled enables or disables a proxy entry.
func (s *proxyStore) SetEnabled(name string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, exists := s.entries[name]
	if !exists {
		return fmt.Errorf("proxy %q not found", name)
	}
	e.Enabled = enabled
	return s.save()
}

// Get returns a copy of the entry, or nil if not found.
func (s *proxyStore) Get(name string) *ProxyEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, exists := s.entries[name]
	if !exists {
		return nil
	}
	cp := *e
	return &cp
}

// All returns a copy of all entries.
func (s *proxyStore) All() []*ProxyEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]*ProxyEntry, 0, len(s.entries))
	for _, e := range s.entries {
		cp := *e
		out = append(out, &cp)
	}
	return out
}

// readFileNoFollow opens a file with O_NOFOLLOW and reads its full contents.
// Uses io.ReadAll to avoid partial reads from f.Read on large files.
func readFileNoFollow(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// SEC-3: Warn if file permissions are too permissive.
	if info, statErr := f.Stat(); statErr == nil {
		if mode := info.Mode().Perm(); mode&0077 != 0 {
			slog.Warn("proxies.json has permissive permissions, expected 0600",
				"path", path, "mode", fmt.Sprintf("%04o", mode))
		}
	}

	return io.ReadAll(f)
}

// isSymlinkError checks if the error is ELOOP (O_NOFOLLOW on a symlink).
func isSymlinkError(err error) bool {
	return os.IsPermission(err) || isELOOP(err)
}

// isELOOP checks for syscall.ELOOP in the error chain.
func isELOOP(err error) bool {
	for err != nil {
		if pe, ok := err.(*os.PathError); ok {
			if pe.Err == syscall.ELOOP {
				return true
			}
			err = pe.Err
			continue
		}
		break
	}
	return false
}

// ProxiesFilePath returns the path to proxies.json given a config directory.
func ProxiesFilePath(configDir string) string {
	return filepath.Join(configDir, "proxies.json")
}
