package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/relay"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// MaxRequestBodySize limits the size of JSON request bodies to prevent
// unbounded memory consumption from oversized or malicious payloads.
const MaxRequestBodySize = 1 << 20 // 1 MB

// registerRoutes sets up all HTTP routes on the mux.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Read-only
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("GET /v1/services", s.handleServiceList)
	mux.HandleFunc("POST /v1/services/remote", s.handleRemoteServiceList)
	mux.HandleFunc("GET /v1/peers", s.handlePeerList)
	mux.HandleFunc("GET /v1/auth", s.handleAuthList)

	mux.HandleFunc("GET /v1/paths", s.handlePaths)
	mux.HandleFunc("GET /v1/bandwidth", s.handleBandwidth)
	mux.HandleFunc("GET /v1/relay-health", s.handleRelayHealth)

	// Mutations
	mux.HandleFunc("POST /v1/auth", s.handleAuthAdd)
	mux.HandleFunc("DELETE /v1/auth/{peer_id}", s.handleAuthRemove)
	mux.HandleFunc("POST /v1/ping", s.handlePing)
	mux.HandleFunc("POST /v1/traceroute", s.handleTraceroute)
	mux.HandleFunc("POST /v1/resolve", s.handleResolve)
	mux.HandleFunc("POST /v1/connect", s.handleConnect)
	mux.HandleFunc("DELETE /v1/connect/{id}", s.handleDisconnect)
	mux.HandleFunc("POST /v1/expose", s.handleExpose)
	mux.HandleFunc("DELETE /v1/expose/{name}", s.handleUnexpose)
	mux.HandleFunc("POST /v1/shutdown", s.handleShutdown)
	mux.HandleFunc("POST /v1/lock", s.handleLock)
	mux.HandleFunc("POST /v1/unlock", s.handleUnlock)
	mux.HandleFunc("GET /v1/lock", s.handleLockStatus)

	// Invite (PAKE handshake via daemon's P2P host)
	mux.HandleFunc("POST /v1/invite", s.handleInviteCreate)
	mux.HandleFunc("GET /v1/invite/{id}/wait", s.handleInviteWait)
	mux.HandleFunc("DELETE /v1/invite/{id}", s.handleInviteCancel)

	// Config
	mux.HandleFunc("POST /v1/config/reload", s.handleConfigReload)
	mux.HandleFunc("GET /v1/config/reload", s.handleConfigReloadStatus)

	// Plugins
	mux.HandleFunc("GET /v1/plugins", s.handlePluginList)
	mux.HandleFunc("POST /v1/plugins/disable-all", s.handlePluginDisableAll)
	mux.HandleFunc("GET /v1/plugins/{name}", s.handlePluginInfo)
	mux.HandleFunc("POST /v1/plugins/{name}/enable", s.handlePluginEnable)
	mux.HandleFunc("POST /v1/plugins/{name}/disable", s.handlePluginDisable)

	// Plugin-provided routes (registered at mux setup, gated per-request by IsRouteActive).
	if s.registry != nil {
		// Build set of core route keys for conflict detection.
		coreRouteKeys := map[string]bool{
			"GET /v1/status": true, "GET /v1/services": true, "POST /v1/services/remote": true,
			"GET /v1/peers": true, "GET /v1/auth": true, "GET /v1/paths": true,
			"GET /v1/bandwidth": true, "GET /v1/relay-health": true,
			"POST /v1/auth": true, "DELETE /v1/auth/{peer_id}": true,
			"POST /v1/ping": true, "POST /v1/traceroute": true, "POST /v1/resolve": true,
			"POST /v1/connect": true, "DELETE /v1/connect/{id}": true,
			"POST /v1/expose": true, "DELETE /v1/expose/{name}": true,
			"POST /v1/shutdown": true, "POST /v1/lock": true, "POST /v1/unlock": true, "GET /v1/lock": true,
			"POST /v1/invite": true, "GET /v1/invite/{id}/wait": true, "DELETE /v1/invite/{id}": true,
			"POST /v1/config/reload": true, "GET /v1/config/reload": true,
			"GET /v1/plugins": true, "POST /v1/plugins/disable-all": true,
			"GET /v1/plugins/{name}": true, "POST /v1/plugins/{name}/enable": true, "POST /v1/plugins/{name}/disable": true,
		}

		for _, route := range s.registry.AllRegisteredRoutes() {
			r := route
			key := r.Method + " " + r.Path
			if coreRouteKeys[key] {
				slog.Warn("plugin route conflicts with core route, skipped",
					"method", r.Method, "path", r.Path)
				continue
			}
			mux.HandleFunc(key, func(w http.ResponseWriter, req *http.Request) {
				if !s.registry.IsRouteActive(r.Method, r.Path) {
					RespondError(w, http.StatusNotFound, "plugin not available")
					return
				}
				r.Handler(w, req)
			})
		}
	}
}

// --- Format helpers ---

// WantsText returns true if the client prefers plain text output.
func WantsText(r *http.Request) bool {
	if r.URL.Query().Get("format") == "text" {
		return true
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/plain")
}

// RespondJSON writes a JSON response with the given status code.
func RespondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(DataResponse{Data: data})
}

// RespondError writes a JSON error response.
func RespondError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse{Error: msg})
}

// RespondText writes a plain text response.
func RespondText(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	fmt.Fprint(w, text)
}

// ParseJSON reads and decodes a JSON request body with size limits.
func ParseJSON(r *http.Request, dst any) error {
	return json.NewDecoder(io.LimitReader(r.Body, MaxRequestBodySize)).Decode(dst)
}

// isShurliAgent returns true if the agent version string identifies a shurli or relay-server peer.
func isShurliAgent(agent string) bool {
	return strings.HasPrefix(agent, "shurli/") || strings.HasPrefix(agent, "relay-server/")
}

// --- Handlers ---

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	rt := s.runtime
	h := rt.Network().Host()

	// Categorize addresses
	var listenAddrs, relayAddrs []string
	for _, addr := range h.Addrs() {
		addrStr := addr.String()
		if strings.Contains(addrStr, "/p2p-circuit") {
			relayAddrs = append(relayAddrs, addrStr)
		} else {
			listenAddrs = append(listenAddrs, addrStr)
		}
	}

	resp := StatusResponse{
		PeerID:         h.ID().String(),
		Version:        rt.Version(),
		UptimeSeconds:  int(time.Since(rt.StartTime()).Seconds()),
		ConnectedPeers: len(h.Network().Peers()),
		ListenAddrs:    listenAddrs,
		RelayAddrs:     relayAddrs,
		ServicesCount:  len(rt.Network().ListServices()),
	}

	// Populate interface discovery flags if available
	if ifSummary := rt.Interfaces(); ifSummary != nil {
		resp.HasGlobalIPv6 = ifSummary.HasGlobalIPv6
		resp.HasGlobalIPv4 = ifSummary.HasGlobalIPv4
	}

	// Populate STUN results if available
	if stunResult := rt.STUNResult(); stunResult != nil {
		resp.NATType = string(stunResult.NATType)
		resp.STUNExternalAddrs = stunResult.ExternalAddrs
	}

	// Peer relay status
	resp.IsRelaying = rt.IsRelaying()

	// Reachability grade
	grade := p2pnet.ComputeReachabilityGrade(rt.Interfaces(), rt.STUNResult())
	resp.Reachability = &grade

	// Relay connectivity status
	for _, addrStr := range rt.RelayAddresses() {
		maddr, err := ma.NewMultiaddr(addrStr)
		if err != nil {
			continue
		}
		info, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			continue
		}
		pidStr := info.ID.String()
		short := pidStr
		if len(short) > 16 {
			short = short[:16] + "..."
		}

		rs := RelayStatus{
			Address:   addrStr,
			PeerID:    pidStr,
			ShortID:   short,
			Connected: h.Network().Connectedness(info.ID) == network.Connected,
		}

		// Parse relay name and agent version from peerstore.
		if av, avErr := h.Peerstore().Get(info.ID, "AgentVersion"); avErr == nil {
			if s, ok := av.(string); ok {
				rs.AgentVersion = s
				if start := strings.Index(s, "("); start >= 0 {
					if end := strings.LastIndex(s, ")"); end > start+1 {
						rs.RelayName = s[start+1 : end]
					}
				}
			}
		}
		resp.Relays = append(resp.Relays, rs)
	}

	// MOTD/goodbye messages from relays
	resp.MOTDs = rt.RelayMOTDs()

	// Config reload state (only include if reloads have happened)
	s.mu.Lock()
	if s.reloadState.TotalReloads > 0 {
		state := s.reloadState
		resp.ConfigReload = &state
	}
	s.mu.Unlock()

	// Plugin status contributions (replaces direct TransferService access).
	if s.registry != nil {
		resp.PluginStatus = s.registry.StatusContributions()
	}

	if WantsText(r) {
		var sb strings.Builder
		fmt.Fprintf(&sb, "peer_id: %s\n", resp.PeerID)
		fmt.Fprintf(&sb, "version: %s\n", resp.Version)
		fmt.Fprintf(&sb, "uptime: %ds\n", resp.UptimeSeconds)
		fmt.Fprintf(&sb, "connected_peers: %d\n", resp.ConnectedPeers)
		fmt.Fprintf(&sb, "services: %d\n", resp.ServicesCount)
		fmt.Fprintf(&sb, "global_ipv6: %v\n", resp.HasGlobalIPv6)
		fmt.Fprintf(&sb, "global_ipv4: %v\n", resp.HasGlobalIPv4)
		fmt.Fprintf(&sb, "is_relaying: %v\n", resp.IsRelaying)
		if resp.Reachability != nil {
			fmt.Fprintf(&sb, "reachability: [%s] %s - %s\n", resp.Reachability.Grade, resp.Reachability.Label, resp.Reachability.Description)
		}
		if resp.NATType != "" {
			fmt.Fprintf(&sb, "nat_type: %s\n", resp.NATType)
		}
		if len(resp.STUNExternalAddrs) > 0 {
			fmt.Fprintf(&sb, "stun_external_addrs: %d\n", len(resp.STUNExternalAddrs))
			for _, a := range resp.STUNExternalAddrs {
				fmt.Fprintf(&sb, "  %s\n", a)
			}
		}
		fmt.Fprintf(&sb, "listen_addresses: %d\n", len(resp.ListenAddrs))
		for _, a := range resp.ListenAddrs {
			fmt.Fprintf(&sb, "  %s\n", a)
		}
		fmt.Fprintf(&sb, "relay_addresses: %d\n", len(resp.RelayAddrs))
		for _, a := range resp.RelayAddrs {
			fmt.Fprintf(&sb, "  %s\n", a)
		}
		if resp.ConfigReload != nil {
			cr := resp.ConfigReload
			ago := time.Since(cr.LastReloadTime).Round(time.Second)
			if cr.LastSuccess {
				fmt.Fprintf(&sb, "config_reload: ok (%s ago)", ago)
				if len(cr.LastChanged) > 0 {
					fmt.Fprintf(&sb, " changed: %s", strings.Join(cr.LastChanged, ", "))
				}
				fmt.Fprintln(&sb)
			} else {
				fmt.Fprintf(&sb, "config_reload: FAILED (%s ago) error: %s\n", ago, cr.LastError)
				if cr.ConsecutiveFailures > 1 {
					fmt.Fprintf(&sb, "  consecutive_failures: %d\n", cr.ConsecutiveFailures)
				}
			}
			if len(cr.LastReverted) > 0 {
				fmt.Fprintf(&sb, "  reverted: %s\n", strings.Join(cr.LastReverted, ", "))
			}
		}
		for pluginName, fields := range resp.PluginStatus {
			for k, v := range fields {
				fmt.Fprintf(&sb, "%s.%s: %v\n", pluginName, k, v)
			}
		}
		RespondText(w, http.StatusOK, sb.String())
		return
	}

	RespondJSON(w, http.StatusOK, resp)
}

func (s *Server) handleServiceList(w http.ResponseWriter, r *http.Request) {
	services := s.runtime.Network().ListServices()

	infos := make([]ServiceInfo, 0, len(services))
	for _, svc := range services {
		infos = append(infos, ServiceInfo{
			Name:         svc.Name,
			Protocol:     svc.Protocol,
			LocalAddress: svc.LocalAddress,
			Enabled:      svc.Enabled,
		})
	}

	if WantsText(r) {
		var sb strings.Builder
		for _, svc := range infos {
			status := "enabled"
			if !svc.Enabled {
				status = "disabled"
			}
			fmt.Fprintf(&sb, "%s\t%s\t%s\t%s\n", svc.Name, svc.LocalAddress, svc.Protocol, status)
		}
		RespondText(w, http.StatusOK, sb.String())
		return
	}

	RespondJSON(w, http.StatusOK, infos)
}

func (s *Server) handleRemoteServiceList(w http.ResponseWriter, r *http.Request) {
	var req RemoteServiceRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxRequestBodySize)).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Peer == "" {
		RespondError(w, http.StatusBadRequest, "peer is required")
		return
	}

	pnet := s.runtime.Network()

	targetPeerID, err := pnet.ResolveName(req.Peer)
	if err != nil {
		RespondError(w, http.StatusBadRequest, fmt.Sprintf("cannot resolve peer %q: %v", req.Peer, err))
		return
	}

	if err := s.runtime.ConnectToPeer(r.Context(), targetPeerID); err != nil {
		RespondError(w, http.StatusBadGateway, fmt.Sprintf("cannot reach peer %q: %v", req.Peer, err))
		return
	}

	stream, err := pnet.OpenPluginStream(r.Context(), targetPeerID, "service-query")
	if err != nil {
		RespondError(w, http.StatusBadGateway, fmt.Sprintf("cannot open service-query stream: %v", err))
		return
	}
	defer stream.Close()

	services, err := p2pnet.QueryPeerServices(stream)
	if err != nil {
		RespondError(w, http.StatusInternalServerError, fmt.Sprintf("service query failed: %v", err))
		return
	}

	if WantsText(r) {
		var sb strings.Builder
		for _, svc := range services {
			fmt.Fprintf(&sb, "%-16s %s\n", svc.Name, svc.Protocol)
		}
		if len(services) == 0 {
			sb.WriteString("(no services)\n")
		}
		RespondText(w, http.StatusOK, sb.String())
		return
	}

	RespondJSON(w, http.StatusOK, RemoteServiceResponse{Services: services})
}

func (s *Server) handlePeerList(w http.ResponseWriter, r *http.Request) {
	h := s.runtime.Network().Host()
	peerIDs := h.Network().Peers()
	showAll := r.URL.Query().Get("all") == "true"

	peers := make([]PeerInfo, 0, len(peerIDs))
	for _, pid := range peerIDs {
		info := PeerInfo{ID: pid.String()}

		// Get agent version from peerstore
		if av, err := h.Peerstore().Get(pid, "AgentVersion"); err == nil {
			if agent, ok := av.(string); ok {
				info.AgentVersion = agent
			}
		}

		// By default, only show shurli and relay-server peers
		if !showAll && !isShurliAgent(info.AgentVersion) {
			continue
		}

		// Get addresses
		addrs := h.Peerstore().Addrs(pid)
		for _, a := range addrs {
			info.Addresses = append(info.Addresses, a.String())
		}

		peers = append(peers, info)
	}

	if WantsText(r) {
		var sb strings.Builder
		for _, p := range peers {
			agent := p.AgentVersion
			if agent == "" {
				agent = "unknown"
			}
			fmt.Fprintf(&sb, "%s\t%s\t%d addrs\n", p.ID[:16]+"...", agent, len(p.Addresses))
		}
		RespondText(w, http.StatusOK, sb.String())
		return
	}

	RespondJSON(w, http.StatusOK, peers)
}

func (s *Server) handlePaths(w http.ResponseWriter, r *http.Request) {
	tracker := s.runtime.PathTracker()
	if tracker == nil {
		RespondJSON(w, http.StatusOK, []*p2pnet.PeerPathInfo{})
		return
	}

	paths := tracker.ListPeerPaths()

	if WantsText(r) {
		var sb strings.Builder
		for _, p := range paths {
			peerShort := p.PeerID
			if len(peerShort) > 16 {
				peerShort = peerShort[:16] + "..."
			}
			rttStr := "-"
			if p.LastRTTMs > 0 {
				rttStr = fmt.Sprintf("%.1fms", p.LastRTTMs)
			}
			fmt.Fprintf(&sb, "%s\t%s\t%s\t%s\trtt=%s\n",
				peerShort, p.PathType, p.Transport, p.IPVersion, rttStr)
		}
		RespondText(w, http.StatusOK, sb.String())
		return
	}

	RespondJSON(w, http.StatusOK, paths)
}

func (s *Server) handleAuthList(w http.ResponseWriter, r *http.Request) {
	authPath := s.runtime.AuthKeysPath()
	if authPath == "" {
		RespondJSON(w, http.StatusOK, []AuthEntry{})
		return
	}

	peers, err := auth.ListPeers(authPath)
	if err != nil {
		RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	entries := make([]AuthEntry, 0, len(peers))
	for _, p := range peers {
		role := p.Role
		if role == "" {
			role = auth.RoleMember
		}
		e := AuthEntry{
			PeerID:   p.PeerID.String(),
			Comment:  p.Comment,
			Verified: p.Verified,
			Group:    p.Group,
			Role:     role,
		}
		if !p.ExpiresAt.IsZero() {
			e.ExpiresAt = p.ExpiresAt.Format(time.RFC3339)
		}
		entries = append(entries, e)
	}

	if WantsText(r) {
		var sb strings.Builder
		for _, e := range entries {
			if e.Comment != "" {
				fmt.Fprintf(&sb, "%s\t# %s\n", e.PeerID, e.Comment)
			} else {
				fmt.Fprintf(&sb, "%s\n", e.PeerID)
			}
		}
		RespondText(w, http.StatusOK, sb.String())
		return
	}

	RespondJSON(w, http.StatusOK, entries)
}

func (s *Server) handleAuthAdd(w http.ResponseWriter, r *http.Request) {
	var req AuthAddRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxRequestBodySize)).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.PeerID == "" {
		RespondError(w, http.StatusBadRequest, "peer_id is required")
		return
	}

	authPath := s.runtime.AuthKeysPath()
	if authPath == "" {
		RespondError(w, http.StatusBadRequest, "connection gating is not enabled")
		return
	}

	// Add peer to authorized_keys file
	if err := auth.AddPeer(authPath, req.PeerID, req.Comment); err != nil {
		RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Set role if specified (default: member)
	role := req.Role
	if role == "" {
		role = auth.RoleMember
	}
	if role != auth.RoleAdmin && role != auth.RoleMember {
		RespondError(w, http.StatusBadRequest, "role must be \"admin\" or \"member\"")
		return
	}
	if err := auth.SetPeerRole(authPath, req.PeerID, role); err != nil {
		slog.Error("failed to set peer role", "error", err)
	}

	// Hot-reload gater
	if err := s.reloadGater(); err != nil {
		slog.Error("failed to reload gater after adding peer", "error", err)
		RespondError(w, http.StatusInternalServerError, "peer added but gater reload failed: "+err.Error())
		return
	}

	slog.Info("authorized peer added via API", "peer", req.PeerID[:16]+"...", "role", role)
	RespondJSON(w, http.StatusOK, map[string]string{"status": "added"})
}

func (s *Server) handleAuthRemove(w http.ResponseWriter, r *http.Request) {
	peerID := r.PathValue("peer_id")
	if peerID == "" {
		RespondError(w, http.StatusBadRequest, "peer_id is required")
		return
	}

	authPath := s.runtime.AuthKeysPath()
	if authPath == "" {
		RespondError(w, http.StatusBadRequest, "connection gating is not enabled")
		return
	}

	if err := auth.RemovePeer(authPath, peerID); err != nil {
		RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Hot-reload gater
	if err := s.reloadGater(); err != nil {
		slog.Error("failed to reload gater after removing peer", "error", err)
	}

	slog.Info("authorized peer removed via API", "peer", peerID[:16]+"...")
	RespondJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// reloadGater reloads the authorized_keys file and updates the connection gater.
func (s *Server) reloadGater() error {
	gater := s.runtime.GaterForHotReload()
	if gater == nil {
		return nil
	}
	return gater.ReloadFromFile()
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	var req PingRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxRequestBodySize)).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Peer == "" {
		RespondError(w, http.StatusBadRequest, "peer is required")
		return
	}

	interval := time.Second
	if req.IntervalMs > 0 {
		interval = time.Duration(req.IntervalMs) * time.Millisecond
	}

	// Resolve peer name
	net := s.runtime.Network()
	targetPeerID, err := net.ResolveName(req.Peer)
	if err != nil {
		RespondError(w, http.StatusBadRequest, fmt.Sprintf("cannot resolve peer %q: %v", req.Peer, err))
		return
	}

	// Ensure the peer is reachable (DHT lookup + relay fallback)
	if err := s.runtime.ConnectToPeer(r.Context(), targetPeerID); err != nil {
		RespondError(w, http.StatusBadGateway, fmt.Sprintf("cannot reach peer %q: %v", req.Peer, err))
		return
	}

	protocolID := s.runtime.PingProtocolID()

	count := req.Count
	if count <= 0 {
		// For API, default to 4 pings if not specified (avoid infinite streaming over HTTP)
		count = 4
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(count)*interval+30*time.Second)
	defer cancel()

	ch := p2pnet.PingPeer(ctx, net.Host(), targetPeerID, protocolID, count, interval)

	var results []p2pnet.PingResult
	for result := range ch {
		results = append(results, result)
	}

	stats := p2pnet.ComputePingStats(results)

	if WantsText(r) {
		var sb strings.Builder
		fmt.Fprintf(&sb, "PING %s (%s):\n", req.Peer, targetPeerID.String()[:16]+"...")
		for _, pr := range results {
			if pr.Error != "" {
				fmt.Fprintf(&sb, "seq=%d error=%s\n", pr.Seq, pr.Error)
			} else {
				fmt.Fprintf(&sb, "seq=%d rtt=%.1fms path=[%s]\n", pr.Seq, pr.RttMs, pr.Path)
			}
		}
		fmt.Fprintf(&sb, "--- %s ping statistics ---\n", req.Peer)
		fmt.Fprintf(&sb, "%d sent, %d received, %.0f%% loss, rtt min/avg/max = %.1f/%.1f/%.1f ms\n",
			stats.Sent, stats.Received, stats.LossPct, stats.MinMs, stats.AvgMs, stats.MaxMs)
		RespondText(w, http.StatusOK, sb.String())
		return
	}

	RespondJSON(w, http.StatusOK, PingResponse{Results: results, Stats: stats})
}

func (s *Server) handleTraceroute(w http.ResponseWriter, r *http.Request) {
	var req TraceRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxRequestBodySize)).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Peer == "" {
		RespondError(w, http.StatusBadRequest, "peer is required")
		return
	}

	net := s.runtime.Network()
	targetPeerID, err := net.ResolveName(req.Peer)
	if err != nil {
		RespondError(w, http.StatusBadRequest, fmt.Sprintf("cannot resolve peer %q: %v", req.Peer, err))
		return
	}

	// Ensure the peer is reachable (DHT lookup + relay fallback)
	if err := s.runtime.ConnectToPeer(r.Context(), targetPeerID); err != nil {
		RespondError(w, http.StatusBadGateway, fmt.Sprintf("cannot reach peer %q: %v", req.Peer, err))
		return
	}

	result, err := p2pnet.TracePeer(r.Context(), net.Host(), targetPeerID)
	if err != nil {
		RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result.Target = req.Peer

	if WantsText(r) {
		var sb strings.Builder
		fmt.Fprintf(&sb, "traceroute to %s (%s):\n", req.Peer, targetPeerID.String()[:16]+"...")
		for _, hop := range result.Hops {
			peerShort := hop.PeerID
			if len(peerShort) > 16 {
				peerShort = peerShort[:16] + "..."
			}
			if hop.Error != "" {
				fmt.Fprintf(&sb, " %d  %s  %s  *\n", hop.Hop, peerShort, hop.Address)
			} else {
				name := ""
				if hop.Name != "" {
					name = " (" + hop.Name + ")"
				}
				fmt.Fprintf(&sb, " %d  %s%s  %s  %.1fms\n", hop.Hop, peerShort, name, hop.Address, hop.RttMs)
			}
		}
		fmt.Fprintf(&sb, "--- path: [%s] ---\n", result.Path)
		RespondText(w, http.StatusOK, sb.String())
		return
	}

	RespondJSON(w, http.StatusOK, result)
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	var req ResolveRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxRequestBodySize)).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		RespondError(w, http.StatusBadRequest, "name is required")
		return
	}

	net := s.runtime.Network()

	// Try to resolve as a name first
	peerID, err := net.ResolveName(req.Name)
	source := "local_config"
	if err != nil {
		// ResolveName also tries parsing as a peer ID directly
		RespondError(w, http.StatusNotFound, fmt.Sprintf("cannot resolve %q: %v", req.Name, err))
		return
	}

	// Check if the input was already a peer ID (not a name lookup)
	if _, parseErr := peer.Decode(req.Name); parseErr == nil {
		source = "peer_id"
	}

	resp := ResolveResponse{
		Name:   req.Name,
		PeerID: peerID.String(),
		Source: source,
	}

	if WantsText(r) {
		RespondText(w, http.StatusOK, fmt.Sprintf("%s → %s (source: %s)\n", resp.Name, resp.PeerID, resp.Source))
		return
	}

	RespondJSON(w, http.StatusOK, resp)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req ConnectRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxRequestBodySize)).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Peer == "" || req.Service == "" || req.Listen == "" {
		RespondError(w, http.StatusBadRequest, "peer, service, and listen are required")
		return
	}

	pnet := s.runtime.Network()

	// Resolve peer name
	targetPeerID, err := pnet.ResolveName(req.Peer)
	if err != nil {
		RespondError(w, http.StatusBadRequest, fmt.Sprintf("cannot resolve peer %q: %v", req.Peer, err))
		return
	}

	// Ensure the peer is reachable (DHT lookup + relay fallback)
	if err := s.runtime.ConnectToPeer(r.Context(), targetPeerID); err != nil {
		RespondError(w, http.StatusBadGateway, fmt.Sprintf("cannot reach peer %q: %v", req.Peer, err))
		return
	}

	// Create dial function with retry
	dialFunc := p2pnet.DialWithRetry(func() (p2pnet.ServiceConn, error) {
		return pnet.ConnectToService(targetPeerID, req.Service)
	}, 3)

	// Create TCP listener
	listener, err := p2pnet.NewTCPListener(req.Listen, dialFunc)
	if err != nil {
		RespondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create listener: %v", err))
		return
	}

	// Generate proxy ID
	s.mu.Lock()
	s.nextID++
	id := fmt.Sprintf("proxy-%d", s.nextID)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	proxy := &activeProxy{
		ID:       id,
		Peer:     req.Peer,
		Service:  req.Service,
		Listen:   listener.Addr().String(),
		listener: listener,
		cancel:   cancel,
		done:     done,
	}
	s.proxies[id] = proxy
	s.mu.Unlock()

	// Serve in background
	go func() {
		defer close(done)
		<-ctx.Done()
		listener.Close()
	}()

	go func() {
		if err := listener.Serve(); err != nil {
			// Check if this was an intentional shutdown
			select {
			case <-ctx.Done():
				// Expected - proxy was disconnected
			default:
				slog.Error("proxy listener stopped", "id", id, "error", err)
			}
		}
	}()

	// Detect connection path type for the response
	h := pnet.Host()
	pathType, addr := p2pnet.PeerConnInfo(h, targetPeerID)

	slog.Info("proxy created via API", "id", id, "peer", req.Peer, "service", req.Service, "listen", proxy.Listen, "path", pathType)
	RespondJSON(w, http.StatusOK, ConnectResponse{
		ID:            id,
		ListenAddress: proxy.Listen,
		PathType:      pathType,
		Address:       addr,
	})
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		RespondError(w, http.StatusBadRequest, "proxy id is required")
		return
	}

	s.mu.Lock()
	proxy, exists := s.proxies[id]
	if exists {
		delete(s.proxies, id)
	}
	s.mu.Unlock()

	if !exists {
		RespondError(w, http.StatusNotFound, fmt.Sprintf("%v: %s", ErrProxyNotFound, id))
		return
	}

	proxy.cancel()
	if proxy.listener != nil {
		proxy.listener.Close()
	}
	<-proxy.done

	slog.Info("proxy disconnected via API", "id", id)
	RespondJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}

func (s *Server) handleExpose(w http.ResponseWriter, r *http.Request) {
	var req ExposeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxRequestBodySize)).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.LocalAddress == "" {
		RespondError(w, http.StatusBadRequest, "name and local_address are required")
		return
	}

	if err := s.runtime.Network().ExposeService(req.Name, req.LocalAddress, nil); err != nil {
		RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("service exposed via API", "service", req.Name, "local", req.LocalAddress)
	RespondJSON(w, http.StatusOK, map[string]string{"status": "exposed"})
}

func (s *Server) handleUnexpose(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		RespondError(w, http.StatusBadRequest, "service name is required")
		return
	}

	if err := s.runtime.Network().UnexposeService(name); err != nil {
		RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	slog.Info("service unexposed via API", "service", name)
	RespondJSON(w, http.StatusOK, map[string]string{"status": "unexposed"})
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	RespondJSON(w, http.StatusOK, map[string]string{"status": "shutting down"})

	// Signal shutdown after response is sent
	go func() {
		time.Sleep(100 * time.Millisecond) // let response flush
		close(s.shutdownCh)
	}()
}

func (s *Server) handleLock(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.locked = true
	s.mu.Unlock()
	slog.Info("daemon locked via API")
	RespondJSON(w, http.StatusOK, map[string]string{"status": "locked"})
}

func (s *Server) handleUnlock(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.locked = false
	s.mu.Unlock()
	slog.Info("daemon unlocked via API")
	RespondJSON(w, http.StatusOK, map[string]string{"status": "unlocked"})
}

func (s *Server) handleLockStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	locked := s.locked
	s.mu.Unlock()
	RespondJSON(w, http.StatusOK, map[string]bool{"locked": locked})
}

func (s *Server) handleConfigReload(w http.ResponseWriter, r *http.Request) {
	reloader := s.runtime.ConfigReloader()
	if reloader == nil {
		RespondError(w, http.StatusNotImplemented, "config reload not supported")
		return
	}

	s.mu.Lock()
	s.reloadState.TotalReloads++
	s.mu.Unlock()

	result, err := reloader.ReloadConfig()

	s.mu.Lock()
	s.reloadState.LastReloadTime = time.Now()
	if err != nil {
		s.reloadState.LastSuccess = false
		s.reloadState.LastError = err.Error()
		s.reloadState.LastChanged = nil
		s.reloadState.LastReverted = nil
		s.reloadState.ConsecutiveFailures++
		s.reloadState.TotalFailures++
		failures := s.reloadState.ConsecutiveFailures
		s.mu.Unlock()

		if failures >= 3 {
			slog.Warn("config reload: repeated failures",
				"consecutive", failures, "error", err)
		}

		RespondError(w, http.StatusInternalServerError, fmt.Sprintf("config reload failed: %v", err))
		return
	}

	s.reloadState.LastSuccess = true
	s.reloadState.LastError = ""
	s.reloadState.LastChanged = result.Changed
	s.reloadState.LastReverted = result.Reverted
	s.reloadState.ConsecutiveFailures = 0
	s.mu.Unlock()

	// Notify plugins of config changes.
	if s.registry != nil {
		s.registry.NotifyConfigReload()
	}

	slog.Info("config reloaded via API", "changed", result.Changed)
	if len(result.Reverted) > 0 {
		slog.Warn("config reload: some changes reverted", "reverted", result.Reverted)
	}
	RespondJSON(w, http.StatusOK, result)
}

func (s *Server) handleConfigReloadStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	state := s.reloadState
	s.mu.Unlock()

	if WantsText(r) {
		var sb strings.Builder
		if state.TotalReloads == 0 {
			fmt.Fprintln(&sb, "No config reloads performed.")
		} else {
			fmt.Fprintf(&sb, "last_reload: %s\n", state.LastReloadTime.Format(time.RFC3339))
			fmt.Fprintf(&sb, "last_success: %v\n", state.LastSuccess)
			if state.LastError != "" {
				fmt.Fprintf(&sb, "last_error: %s\n", state.LastError)
			}
			if len(state.LastChanged) > 0 {
				fmt.Fprintf(&sb, "last_changed: %s\n", strings.Join(state.LastChanged, ", "))
			}
			if len(state.LastReverted) > 0 {
				fmt.Fprintf(&sb, "last_reverted: %s\n", strings.Join(state.LastReverted, ", "))
			}
			fmt.Fprintf(&sb, "consecutive_failures: %d\n", state.ConsecutiveFailures)
			fmt.Fprintf(&sb, "total_reloads: %d\n", state.TotalReloads)
			fmt.Fprintf(&sb, "total_failures: %d\n", state.TotalFailures)
		}
		RespondText(w, http.StatusOK, sb.String())
		return
	}

	RespondJSON(w, http.StatusOK, state)
}

func (s *Server) handleBandwidth(w http.ResponseWriter, r *http.Request) {
	bt := s.runtime.BandwidthTracker()
	if bt == nil {
		RespondJSON(w, http.StatusOK, BandwidthStats{})
		return
	}

	totals := bt.Totals()
	resp := BandwidthStats{
		TotalIn:  totals.TotalIn,
		TotalOut: totals.TotalOut,
		RateIn:   totals.RateIn,
		RateOut:  totals.RateOut,
		ByPeer:   make(map[string]BandwidthPeer),
	}

	for pid, stats := range bt.AllPeerStats() {
		short := pid.String()
		if len(short) > 16 {
			short = short[:16]
		}
		resp.ByPeer[short] = BandwidthPeer{
			TotalIn:  stats.TotalIn,
			TotalOut: stats.TotalOut,
			RateIn:   stats.RateIn,
			RateOut:  stats.RateOut,
		}
	}

	if WantsText(r) {
		var sb strings.Builder
		fmt.Fprintf(&sb, "total_in: %d\n", resp.TotalIn)
		fmt.Fprintf(&sb, "total_out: %d\n", resp.TotalOut)
		fmt.Fprintf(&sb, "rate_in: %.1f B/s\n", resp.RateIn)
		fmt.Fprintf(&sb, "rate_out: %.1f B/s\n", resp.RateOut)
		if len(resp.ByPeer) > 0 {
			fmt.Fprintf(&sb, "peers: %d\n", len(resp.ByPeer))
			for peer, stats := range resp.ByPeer {
				fmt.Fprintf(&sb, "  %s\tin=%d\tout=%d\trate_in=%.1f\trate_out=%.1f\n",
					peer, stats.TotalIn, stats.TotalOut, stats.RateIn, stats.RateOut)
			}
		}
		RespondText(w, http.StatusOK, sb.String())
		return
	}

	RespondJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRelayHealth(w http.ResponseWriter, r *http.Request) {
	rh := s.runtime.RelayHealth()
	if rh == nil {
		RespondJSON(w, http.StatusOK, RelayHealthResponse{})
		return
	}

	ranked := rh.Ranked()
	resp := RelayHealthResponse{
		Relays: make([]RelayHealthEntry, len(ranked)),
	}
	for i, s := range ranked {
		short := s.PeerID.String()
		if len(short) > 16 {
			short = short[:16]
		}
		resp.Relays[i] = RelayHealthEntry{
			PeerID:      short,
			Score:       s.Score,
			RTTMs:       s.RTTMs,
			SuccessRate: s.SuccessRate,
			ProbeCount:  s.ProbeCount,
			IsStatic:    s.IsStatic,
		}
	}

	if WantsText(r) {
		var sb strings.Builder
		fmt.Fprintf(&sb, "relays: %d\n", len(resp.Relays))
		for _, e := range resp.Relays {
			static := ""
			if e.IsStatic {
				static = " [static]"
			}
			fmt.Fprintf(&sb, "  %s\tscore=%.2f\trtt=%.0fms\tsuccess=%.1f%%\tprobes=%d%s\n",
				e.PeerID, e.Score, e.RTTMs, e.SuccessRate*100, e.ProbeCount, static)
		}
		RespondText(w, http.StatusOK, sb.String())
		return
	}

	RespondJSON(w, http.StatusOK, resp)
}

// IsLocked returns whether sensitive operations are currently locked.
func (s *Server) IsLocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked
}

// SocketPath returns the path to the Unix socket.
func (s *Server) SocketPath() string {
	return s.socketPath
}

// Listener returns the underlying net.Listener (for health checks).
func (s *Server) Listener() net.Listener {
	return s.listener
}

// File sharing and transfer handlers moved to plugins/filetransfer/handlers.go.
// Routes registered dynamically via plugin.AllRegisteredRoutes().

// --- Invite handlers (async, relay-delegated) ---

func (s *Server) handleInviteCreate(w http.ResponseWriter, r *http.Request) {
	var req InviteCreateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxRequestBodySize)).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ttl := req.TTLSeconds
	if ttl <= 0 {
		ttl = 86400 // 24 hours default
	}

	count := req.Count
	if count <= 0 {
		count = 1
	}

	s.mu.Lock()
	if s.pendingInvite != nil {
		s.mu.Unlock()
		RespondError(w, http.StatusConflict, "an invite is already active; cancel it first")
		return
	}
	s.mu.Unlock()

	rt := s.runtime
	relayAddrs := rt.RelayAddresses()
	if len(relayAddrs) == 0 {
		RespondError(w, http.StatusBadRequest, "no relay addresses configured; cannot create invite")
		return
	}

	// Connect to relay and create pairing group
	h := rt.Network().Host()
	relayInfos, err := p2pnet.ParseRelayAddrs(relayAddrs)
	if err != nil || len(relayInfos) == 0 {
		RespondError(w, http.StatusInternalServerError, "failed to parse relay addresses")
		return
	}

	relayClient := relay.NewRemoteAdminClient(h, relayInfos[0].ID)
	pairResp, err := relayClient.CreateGroup(count, ttl, 0, rt.DiscoveryNetwork())
	if err != nil {
		RespondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create invite on relay: %v", err))
		return
	}

	// Record group membership locally so the peer-notify handler accepts
	// introductions for this group when the joiner completes pairing.
	if authPath := rt.AuthKeysPath(); authPath != "" {
		relayIDStr := relayInfos[0].ID.String()
		if err := auth.SetPeerAttr(authPath, relayIDStr, "group", pairResp.GroupID); err != nil {
			slog.Warn("invite: failed to record group on relay entry", "err", err)
		}
	}

	// Create invite session for tracking
	_, cancel := context.WithCancel(context.Background())
	inv := &activeInvite{
		id:      fmt.Sprintf("inv-%d", time.Now().UnixNano()),
		groupID: pairResp.GroupID,
		codes:   pairResp.Codes,
		cancel:  cancel,
	}

	s.mu.Lock()
	s.pendingInvite = inv
	s.mu.Unlock()

	slog.Info("invite created via API", "id", inv.id, "group", pairResp.GroupID, "codes", len(pairResp.Codes), "ttl", ttl)
	RespondJSON(w, http.StatusOK, InviteCreateResponse{
		InviteID:  inv.id,
		Codes:     pairResp.Codes,
		GroupID:   pairResp.GroupID,
		TTL:       ttl,
		ExpiresAt: pairResp.ExpiresAt,
	})
}

func (s *Server) handleInviteWait(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	s.mu.Lock()
	inv := s.pendingInvite
	s.mu.Unlock()

	if inv == nil || inv.id != id {
		RespondError(w, http.StatusNotFound, "no active invite with that ID")
		return
	}

	// Extend write deadline for long-poll
	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Now().Add(15 * time.Minute))

	rt := s.runtime
	h := rt.Network().Host()
	relayAddrs := rt.RelayAddresses()
	relayInfos, _ := p2pnet.ParseRelayAddrs(relayAddrs)
	if len(relayInfos) == 0 {
		RespondError(w, http.StatusInternalServerError, "no relay addresses available")
		return
	}

	relayClient := relay.NewRemoteAdminClient(h, relayInfos[0].ID)

	// Poll relay for group status every 5 seconds
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			groups, err := relayClient.ListGroups()
			if err != nil {
				continue // retry on transient errors
			}

			// Find our group
			found := false
			for _, g := range groups {
				if g.ID == inv.groupID {
					found = true
					if g.Used >= g.Total {
						// All codes used - complete
						s.mu.Lock()
						if s.pendingInvite == inv {
							s.pendingInvite = nil
						}
						s.mu.Unlock()
						inv.cancel()

						RespondJSON(w, http.StatusOK, InviteWaitResponse{
							Status: "complete",
							Used:   g.Used,
							Total:  g.Total,
						})
						return
					}
					// Partial - still waiting
					break
				}
			}

			if !found {
				// Group not found - expired or revoked
				s.mu.Lock()
				if s.pendingInvite == inv {
					s.pendingInvite = nil
				}
				s.mu.Unlock()
				inv.cancel()

				RespondJSON(w, http.StatusOK, InviteWaitResponse{
					Status: "expired",
				})
				return
			}

		case <-r.Context().Done():
			RespondError(w, http.StatusGatewayTimeout, "client disconnected")
			return
		}
	}
}

func (s *Server) handleInviteCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	s.mu.Lock()
	inv := s.pendingInvite
	if inv != nil && inv.id == id {
		s.pendingInvite = nil
		s.mu.Unlock()

		// Revoke the group on the relay
		rt := s.runtime
		h := rt.Network().Host()
		relayAddrs := rt.RelayAddresses()
		relayInfos, _ := p2pnet.ParseRelayAddrs(relayAddrs)
		if len(relayInfos) > 0 {
			relayClient := relay.NewRemoteAdminClient(h, relayInfos[0].ID)
			if err := relayClient.RevokeGroup(inv.groupID); err != nil {
				slog.Error("failed to revoke invite group on relay", "group", inv.groupID, "error", err)
			}
		}

		inv.cancel()
		slog.Info("invite cancelled via API", "id", id, "group", inv.groupID)
		RespondJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
		return
	}
	s.mu.Unlock()

	RespondError(w, http.StatusNotFound, "no active invite with that ID")
}
