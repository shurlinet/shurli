package sdk

import (
	"fmt"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
)

// NameResolver resolves names to peer IDs.
// An optional fallback Resolver is consulted when local names don't match.
type NameResolver struct {
	names    map[string]peer.ID
	fallback Resolver // nil = no fallback, try peer.Decode only
	mu       sync.RWMutex
}

// NewNameResolver creates a new name resolver
func NewNameResolver() *NameResolver {
	return &NameResolver{
		names: make(map[string]peer.ID),
	}
}

// newNameResolverFrom wraps a custom Resolver so it can be used as the
// primary resolver while still supporting local Register/LoadFromMap.
// Local names take priority; the custom resolver is the fallback.
func newNameResolverFrom(custom Resolver) *NameResolver {
	return &NameResolver{
		names:    make(map[string]peer.ID),
		fallback: custom,
	}
}

// Register registers a name → peer ID mapping.
// Names are normalized: trimmed of whitespace and lowercased for consistent lookup.
func (r *NameResolver) Register(name string, peerID peer.ID) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.names[name] = peerID
	return nil
}

// Unregister removes a name mapping.
// Name is normalized (trimmed + lowercased) to match stored keys.
func (r *NameResolver) Unregister(name string) {
	name = strings.ToLower(strings.TrimSpace(name))

	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.names, name)
}

// Resolve resolves a name to a peer ID.
// Name is normalized (trimmed + lowercased) for direct map lookup.
// If the name is not found, tries to parse it as a direct peer ID.
func (r *NameResolver) Resolve(name string) (peer.ID, error) {
	normalized := strings.ToLower(strings.TrimSpace(name))

	// Try local name mapping first (normalized key lookup).
	r.mu.RLock()
	if peerID, exists := r.names[normalized]; exists {
		r.mu.RUnlock()
		return peerID, nil
	}
	r.mu.RUnlock()

	// Try custom fallback resolver if configured.
	if r.fallback != nil {
		if peerID, err := r.fallback.Resolve(name); err == nil {
			return peerID, nil
		}
	}

	// Try to parse as direct peer ID (case-sensitive, peer IDs are exact).
	peerID, err := peer.Decode(name)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNameNotFound, name)
	}

	return peerID, nil
}

// List returns all registered name mappings
func (r *NameResolver) List() map[string]peer.ID {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Create a copy to avoid races
	names := make(map[string]peer.ID, len(r.names))
	for name, peerID := range r.names {
		names[name] = peerID
	}

	return names
}

// LoadFromMap loads name mappings from a map (additive - existing names preserved).
// Names are normalized: trimmed of whitespace and lowercased, consistent with Register().
func (r *NameResolver) LoadFromMap(names map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for name, peerIDStr := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		peerID, err := peer.Decode(peerIDStr)
		if err != nil {
			return fmt.Errorf("invalid peer ID for name %s: %w", name, err)
		}

		r.names[name] = peerID
	}

	return nil
}

// ReplaceFromMap replaces all name mappings with the given map.
// Unlike LoadFromMap, this clears existing names first so that removed
// names don't persist in memory after a config reload.
func (r *NameResolver) ReplaceFromMap(names map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	fresh := make(map[string]peer.ID, len(names))
	for name, peerIDStr := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		peerID, err := peer.Decode(peerIDStr)
		if err != nil {
			return fmt.Errorf("invalid peer ID for name %s: %w", name, err)
		}
		fresh[name] = peerID
	}

	r.names = fresh
	return nil
}
