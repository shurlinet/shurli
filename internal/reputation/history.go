// Package reputation provides sovereign per-peer interaction history.
// Each peer collects its own local data. No gossip, no centralization.
// This is Layer 0 data collection that future trust algorithms
// (EigenTrust, Community Notes bridging) will consume as input.
package reputation

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// PeerRecord holds interaction history for a single peer.
type PeerRecord struct {
	PeerID          string         `json:"peer_id"`
	FirstSeen       time.Time      `json:"first_seen"`
	LastSeen        time.Time      `json:"last_seen"`
	ConnectionCount int            `json:"connection_count"`
	AvgLatencyMs    float64        `json:"avg_latency_ms"`
	PathTypes       map[string]int `json:"path_types"` // "direct":12, "relay":3
	IntroducedBy    string         `json:"introduced_by,omitempty"`
	IntroMethod     string         `json:"intro_method,omitempty"` // "relay-pairing", "invite", "manual"
}

// PeerHistory manages the local interaction history file.
type PeerHistory struct {
	mu      sync.RWMutex
	path    string
	records map[string]*PeerRecord
}

// NewPeerHistory creates or loads a peer history from the given file path.
func NewPeerHistory(path string) *PeerHistory {
	h := &PeerHistory{
		path:    path,
		records: make(map[string]*PeerRecord),
	}
	_ = h.Load() // best-effort load
	return h
}

// RecordConnection updates connection count, last_seen, path type counts,
// and running average latency for a peer.
func (h *PeerHistory) RecordConnection(peerID, pathType string, latencyMs float64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	r, ok := h.records[peerID]
	if !ok {
		r = &PeerRecord{
			PeerID:    peerID,
			FirstSeen: time.Now(),
			PathTypes: make(map[string]int),
		}
		h.records[peerID] = r
	}

	r.LastSeen = time.Now()
	r.ConnectionCount++

	if pathType != "" {
		r.PathTypes[pathType]++
	}

	// Running average: new_avg = old_avg + (value - old_avg) / count
	if latencyMs > 0 {
		r.AvgLatencyMs += (latencyMs - r.AvgLatencyMs) / float64(r.ConnectionCount)
	}
}

// RecordIntroduction records how a peer was introduced.
func (h *PeerHistory) RecordIntroduction(peerID, introducedBy, method string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	r, ok := h.records[peerID]
	if !ok {
		r = &PeerRecord{
			PeerID:    peerID,
			FirstSeen: time.Now(),
			PathTypes: make(map[string]int),
		}
		h.records[peerID] = r
	}

	r.IntroducedBy = introducedBy
	r.IntroMethod = method
}

// Get returns a copy of the record for the given peer, or nil if not found.
func (h *PeerHistory) Get(peerID string) *PeerRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()

	r, ok := h.records[peerID]
	if !ok {
		return nil
	}
	copy := *r
	copy.PathTypes = make(map[string]int, len(r.PathTypes))
	for k, v := range r.PathTypes {
		copy.PathTypes[k] = v
	}
	return &copy
}

// Count returns the number of peers tracked.
func (h *PeerHistory) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.records)
}

// Load reads the history file from disk.
func (h *PeerHistory) Load() error {
	data, err := os.ReadFile(h.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read history: %w", err)
	}

	var records map[string]*PeerRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return fmt.Errorf("failed to parse history: %w", err)
	}

	h.mu.Lock()
	h.records = records
	h.mu.Unlock()
	return nil
}

// Save writes the history file to disk atomically.
func (h *PeerHistory) Save() error {
	h.mu.RLock()
	data, err := json.MarshalIndent(h.records, "", "  ")
	h.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("failed to marshal history: %w", err)
	}

	// Atomic write via temp file + rename.
	tmpPath := h.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := os.Rename(tmpPath, h.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}
	return nil
}
