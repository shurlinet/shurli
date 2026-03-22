package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/grants"
	"github.com/shurlinet/shurli/internal/macaroon"
)

// handleGrantList returns all active grants.
func (s *Server) handleGrantList(w http.ResponseWriter, r *http.Request) {
	gs := s.runtime.GrantStore()
	if gs == nil {
		RespondError(w, http.StatusServiceUnavailable, "grant store not initialized")
		return
	}

	active := gs.List()

	// Build reverse lookup: peer ID string -> name.
	reverseNames := s.buildReverseNames()

	var infos []GrantInfo
	for _, g := range active {
		info := GrantInfo{
			PeerID:         g.PeerIDStr,
			Services:       g.Services,
			CreatedAt:      g.CreatedAt.Format(time.RFC3339),
			Permanent:      g.Permanent,
			MaxDelegations: g.MaxDelegations,
			AutoRefresh:    g.AutoRefresh,
			MaxRefreshes:   g.MaxRefreshes,
			RefreshesUsed:  g.RefreshesUsed,
		}

		if name, ok := reverseNames[g.PeerIDStr]; ok {
			info.Peer = name
		} else {
			info.Peer = g.PeerIDStr[:16] + "..."
		}

		if !g.Permanent {
			info.ExpiresAt = g.ExpiresAt.Format(time.RFC3339)
			info.Remaining = formatDuration(g.Remaining())
		}

		infos = append(infos, info)
	}

	if infos == nil {
		infos = []GrantInfo{} // empty array, not null
	}

	RespondJSON(w, http.StatusOK, GrantListResponse{Grants: infos})
}

// handleGrantCreate creates a new data access grant.
func (s *Server) handleGrantCreate(w http.ResponseWriter, r *http.Request) {
	gs := s.runtime.GrantStore()
	if gs == nil {
		RespondError(w, http.StatusServiceUnavailable, "grant store not initialized")
		return
	}

	var req GrantRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxRequestBodySize)).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Peer == "" {
		RespondError(w, http.StatusBadRequest, "peer is required")
		return
	}

	// Resolve peer name to ID.
	peerID, err := s.resolvePeerID(req.Peer)
	if err != nil {
		RespondError(w, http.StatusBadRequest, fmt.Sprintf("cannot resolve peer: %v", err))
		return
	}

	var duration time.Duration
	if !req.Permanent {
		if req.Duration == "" {
			req.Duration = "1h" // default
		}
		duration, err = time.ParseDuration(req.Duration)
		if err != nil {
			RespondError(w, http.StatusBadRequest, fmt.Sprintf("invalid duration: %v", err))
			return
		}
		if duration <= 0 {
			RespondError(w, http.StatusBadRequest, "duration must be positive")
			return
		}
	}

	// Apply config defaults for auto-refresh (B4).
	autoRefresh := req.AutoRefresh
	if !autoRefresh && !req.Permanent && s.runtime.GrantsAutoRefresh() {
		autoRefresh = true
	}

	if autoRefresh && req.Permanent {
		RespondError(w, http.StatusBadRequest, "auto-refresh and permanent are mutually exclusive")
		return
	}

	var grantOpts []grants.GrantOptions
	if autoRefresh {
		maxRefreshes := req.MaxRefreshes
		if maxRefreshes <= 0 {
			maxRefreshes = 3 // default: 3 refreshes when not explicitly set
		}
		maxRefreshDur := duration * time.Duration(maxRefreshes+1)
		// Cap at config max_refresh_duration if set.
		if cfgMax := s.runtime.GrantsMaxRefreshDuration(); cfgMax != "" {
			if cap, err := grants.ParseDurationExtended(cfgMax); err == nil && cap > 0 && maxRefreshDur > cap {
				maxRefreshDur = cap
			}
		}
		grantOpts = append(grantOpts, grants.GrantOptions{
			AutoRefresh:        true,
			MaxRefreshes:       maxRefreshes,
			MaxRefreshDuration: maxRefreshDur,
		})
	}

	grant, err := gs.Grant(peerID, duration, req.Services, req.Permanent, req.MaxDelegations, grantOpts...)
	if err != nil {
		RespondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create grant: %v", err))
		return
	}

	info := GrantInfo{
		PeerID:         grant.PeerIDStr,
		Services:       grant.Services,
		CreatedAt:      grant.CreatedAt.Format(time.RFC3339),
		Permanent:      grant.Permanent,
		MaxDelegations: grant.MaxDelegations,
		AutoRefresh:    grant.AutoRefresh,
		MaxRefreshes:   grant.MaxRefreshes,
		RefreshesUsed:  grant.RefreshesUsed,
	}

	reverseNames := s.buildReverseNames()
	if name, ok := reverseNames[grant.PeerIDStr]; ok {
		info.Peer = name
	} else {
		info.Peer = grant.PeerIDStr[:16] + "..."
	}

	if !grant.Permanent {
		info.ExpiresAt = grant.ExpiresAt.Format(time.RFC3339)
		info.Remaining = formatDuration(grant.Remaining())
	}

	RespondJSON(w, http.StatusCreated, info)
}

// handleGrantRevoke revokes a grant and closes peer connections.
func (s *Server) handleGrantRevoke(w http.ResponseWriter, r *http.Request) {
	gs := s.runtime.GrantStore()
	if gs == nil {
		RespondError(w, http.StatusServiceUnavailable, "grant store not initialized")
		return
	}

	var req GrantRevokeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxRequestBodySize)).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Peer == "" {
		RespondError(w, http.StatusBadRequest, "peer is required")
		return
	}

	peerID, err := s.resolvePeerID(req.Peer)
	if err != nil {
		RespondError(w, http.StatusBadRequest, fmt.Sprintf("cannot resolve peer: %v", err))
		return
	}

	if err := gs.Revoke(peerID); err != nil {
		RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	RespondJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// handleGrantExtend extends an existing grant.
func (s *Server) handleGrantExtend(w http.ResponseWriter, r *http.Request) {
	gs := s.runtime.GrantStore()
	if gs == nil {
		RespondError(w, http.StatusServiceUnavailable, "grant store not initialized")
		return
	}

	var req GrantExtendRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxRequestBodySize)).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Peer == "" {
		RespondError(w, http.StatusBadRequest, "peer is required")
		return
	}
	if req.Duration == "" && req.MaxRefreshes == nil {
		RespondError(w, http.StatusBadRequest, "duration or max_refreshes is required")
		return
	}

	peerID, err := s.resolvePeerID(req.Peer)
	if err != nil {
		RespondError(w, http.StatusBadRequest, fmt.Sprintf("cannot resolve peer: %v", err))
		return
	}

	// Update max refreshes if requested (B4).
	if req.MaxRefreshes != nil {
		if err := gs.UpdateMaxRefreshes(peerID, *req.MaxRefreshes); err != nil {
			RespondError(w, http.StatusNotFound, err.Error())
			return
		}
	}

	// Extend duration if provided.
	if req.Duration != "" {
		duration, err := time.ParseDuration(req.Duration)
		if err != nil {
			RespondError(w, http.StatusBadRequest, fmt.Sprintf("invalid duration: %v", err))
			return
		}
		if duration <= 0 {
			RespondError(w, http.StatusBadRequest, "duration must be positive")
			return
		}
		if err := gs.Extend(peerID, duration); err != nil {
			RespondError(w, http.StatusNotFound, err.Error())
			return
		}
	}

	RespondJSON(w, http.StatusOK, map[string]string{"status": "extended"})
}

// handleGrantDelegate creates a delegated sub-token from a pouch entry and delivers it.
func (s *Server) handleGrantDelegate(w http.ResponseWriter, r *http.Request) {
	pouch := s.runtime.GrantPouch()
	if pouch == nil {
		RespondError(w, http.StatusServiceUnavailable, "grant pouch not initialized")
		return
	}

	proto := s.runtime.GrantProtocol()
	if proto == nil {
		RespondError(w, http.StatusServiceUnavailable, "grant protocol not initialized")
		return
	}

	var req GrantDelegateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxRequestBodySize)).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Peer == "" {
		RespondError(w, http.StatusBadRequest, "peer (grant issuer) is required")
		return
	}
	if req.To == "" {
		RespondError(w, http.StatusBadRequest, "to (delegation target) is required")
		return
	}

	// Resolve both peer names.
	issuerID, err := s.resolvePeerID(req.Peer)
	if err != nil {
		RespondError(w, http.StatusBadRequest, fmt.Sprintf("cannot resolve issuer peer: %v", err))
		return
	}

	targetID, err := s.resolvePeerID(req.To)
	if err != nil {
		RespondError(w, http.StatusBadRequest, fmt.Sprintf("cannot resolve target peer: %v", err))
		return
	}

	var duration time.Duration
	if req.Duration != "" {
		duration, err = time.ParseDuration(req.Duration)
		if err != nil {
			RespondError(w, http.StatusBadRequest, fmt.Sprintf("invalid duration: %v", err))
			return
		}
		if duration <= 0 {
			RespondError(w, http.StatusBadRequest, "duration must be positive")
			return
		}
	}

	// Create the attenuated sub-token.
	subToken, err := pouch.Delegate(issuerID, targetID, duration, req.Services, req.MaxDelegations)
	if err != nil {
		RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Deliver the sub-token to the target peer.
	// When no explicit duration, extract the earliest expires from the sub-token's
	// inherited caveats so the receiver's pouch stores the correct expiry metadata.
	var expiresAt time.Time
	if duration > 0 {
		expiresAt = time.Now().Add(duration)
	} else {
		expiresAt = macaroon.ExtractEarliestExpires(subToken.Caveats)
	}

	status := "delivered"
	deliveryErr := proto.DeliverGrant(r.Context(), targetID, subToken, req.Services, expiresAt, false)
	if deliveryErr != nil {
		// Peer might be offline. Enqueue for later delivery.
		if qErr := proto.EnqueueGrant(targetID, subToken, req.Services, expiresAt, false); qErr != nil {
			RespondError(w, http.StatusInternalServerError, fmt.Sprintf("delivery failed and queue error: %v; %v", deliveryErr, qErr))
			return
		}
		status = "queued"
	}

	reverseNames := s.buildReverseNames()
	targetName := req.To
	if name, ok := reverseNames[targetID.String()]; ok {
		targetName = name
	}

	RespondJSON(w, http.StatusCreated, map[string]string{
		"status": status,
		"target": targetName,
	})
}

// resolvePeerID resolves a peer name or ID string to a peer.ID.
func (s *Server) resolvePeerID(nameOrID string) (peer.ID, error) {
	// Try as peer ID first.
	if pid, err := peer.Decode(nameOrID); err == nil {
		return pid, nil
	}

	// Try as peer name via the network's name resolver.
	pnet := s.runtime.Network()
	if pnet == nil {
		return "", fmt.Errorf("network not available")
	}

	pid, err := pnet.ResolveName(nameOrID)
	if err != nil {
		return "", fmt.Errorf("unknown peer %q", nameOrID)
	}
	return pid, nil
}

// buildReverseNames builds a peer ID string -> name map from the network's name resolver.
func (s *Server) buildReverseNames() map[string]string {
	reverse := make(map[string]string)
	pnet := s.runtime.Network()
	if pnet == nil {
		return reverse
	}
	for name, pid := range pnet.ListNames() {
		key := pid.String()
		if existing, ok := reverse[key]; !ok || len(name) < len(existing) {
			reverse[key] = name
		}
	}
	return reverse
}

// formatDuration returns a human-readable duration string.
func formatDuration(d time.Duration) string {
	if d < 0 {
		return "expired"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m > 0 {
			return fmt.Sprintf("%dh%dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	if h > 0 {
		return fmt.Sprintf("%dd%dh", days, h)
	}
	return fmt.Sprintf("%dd", days)
}
