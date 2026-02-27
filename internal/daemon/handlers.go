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

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// maxRequestBodySize limits the size of JSON request bodies to prevent
// unbounded memory consumption from oversized or malicious payloads.
const maxRequestBodySize = 1 << 20 // 1 MB

// registerRoutes sets up all HTTP routes on the mux.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Read-only
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("GET /v1/services", s.handleServiceList)
	mux.HandleFunc("GET /v1/peers", s.handlePeerList)
	mux.HandleFunc("GET /v1/auth", s.handleAuthList)

	mux.HandleFunc("GET /v1/paths", s.handlePaths)

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
}

// --- Format helpers ---

// wantsText returns true if the client prefers plain text output.
func wantsText(r *http.Request) bool {
	if r.URL.Query().Get("format") == "text" {
		return true
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/plain")
}

// respondJSON writes a JSON response with the given status code.
func respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(DataResponse{Data: data})
}

// respondError writes a JSON error response.
func respondError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse{Error: msg})
}

// respondText writes a plain text response.
func respondText(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	fmt.Fprint(w, text)
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

	if wantsText(r) {
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
		respondText(w, http.StatusOK, sb.String())
		return
	}

	respondJSON(w, http.StatusOK, resp)
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

	if wantsText(r) {
		var sb strings.Builder
		for _, svc := range infos {
			status := "enabled"
			if !svc.Enabled {
				status = "disabled"
			}
			fmt.Fprintf(&sb, "%s\t%s\t%s\t%s\n", svc.Name, svc.LocalAddress, svc.Protocol, status)
		}
		respondText(w, http.StatusOK, sb.String())
		return
	}

	respondJSON(w, http.StatusOK, infos)
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

	if wantsText(r) {
		var sb strings.Builder
		for _, p := range peers {
			agent := p.AgentVersion
			if agent == "" {
				agent = "unknown"
			}
			fmt.Fprintf(&sb, "%s\t%s\t%d addrs\n", p.ID[:16]+"...", agent, len(p.Addresses))
		}
		respondText(w, http.StatusOK, sb.String())
		return
	}

	respondJSON(w, http.StatusOK, peers)
}

func (s *Server) handlePaths(w http.ResponseWriter, r *http.Request) {
	tracker := s.runtime.PathTracker()
	if tracker == nil {
		respondJSON(w, http.StatusOK, []*p2pnet.PeerPathInfo{})
		return
	}

	paths := tracker.ListPeerPaths()

	if wantsText(r) {
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
		respondText(w, http.StatusOK, sb.String())
		return
	}

	respondJSON(w, http.StatusOK, paths)
}

func (s *Server) handleAuthList(w http.ResponseWriter, r *http.Request) {
	authPath := s.runtime.AuthKeysPath()
	if authPath == "" {
		respondJSON(w, http.StatusOK, []AuthEntry{})
		return
	}

	peers, err := auth.ListPeers(authPath)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	entries := make([]AuthEntry, 0, len(peers))
	for _, p := range peers {
		e := AuthEntry{
			PeerID:   p.PeerID.String(),
			Comment:  p.Comment,
			Verified: p.Verified,
			Group:    p.Group,
		}
		if !p.ExpiresAt.IsZero() {
			e.ExpiresAt = p.ExpiresAt.Format(time.RFC3339)
		}
		entries = append(entries, e)
	}

	if wantsText(r) {
		var sb strings.Builder
		for _, e := range entries {
			if e.Comment != "" {
				fmt.Fprintf(&sb, "%s\t# %s\n", e.PeerID, e.Comment)
			} else {
				fmt.Fprintf(&sb, "%s\n", e.PeerID)
			}
		}
		respondText(w, http.StatusOK, sb.String())
		return
	}

	respondJSON(w, http.StatusOK, entries)
}

func (s *Server) handleAuthAdd(w http.ResponseWriter, r *http.Request) {
	var req AuthAddRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBodySize)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.PeerID == "" {
		respondError(w, http.StatusBadRequest, "peer_id is required")
		return
	}

	authPath := s.runtime.AuthKeysPath()
	if authPath == "" {
		respondError(w, http.StatusBadRequest, "connection gating is not enabled")
		return
	}

	// Add peer to authorized_keys file
	if err := auth.AddPeer(authPath, req.PeerID, req.Comment); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Hot-reload gater
	if err := s.reloadGater(); err != nil {
		slog.Error("failed to reload gater after adding peer", "error", err)
		respondError(w, http.StatusInternalServerError, "peer added but gater reload failed: "+err.Error())
		return
	}

	slog.Info("authorized peer added via API", "peer", req.PeerID[:16]+"...")
	respondJSON(w, http.StatusOK, map[string]string{"status": "added"})
}

func (s *Server) handleAuthRemove(w http.ResponseWriter, r *http.Request) {
	peerID := r.PathValue("peer_id")
	if peerID == "" {
		respondError(w, http.StatusBadRequest, "peer_id is required")
		return
	}

	authPath := s.runtime.AuthKeysPath()
	if authPath == "" {
		respondError(w, http.StatusBadRequest, "connection gating is not enabled")
		return
	}

	if err := auth.RemovePeer(authPath, peerID); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Hot-reload gater
	if err := s.reloadGater(); err != nil {
		slog.Error("failed to reload gater after removing peer", "error", err)
	}

	slog.Info("authorized peer removed via API", "peer", peerID[:16]+"...")
	respondJSON(w, http.StatusOK, map[string]string{"status": "removed"})
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
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBodySize)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Peer == "" {
		respondError(w, http.StatusBadRequest, "peer is required")
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
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot resolve peer %q: %v", req.Peer, err))
		return
	}

	// Ensure the peer is reachable (DHT lookup + relay fallback)
	if err := s.runtime.ConnectToPeer(r.Context(), targetPeerID); err != nil {
		respondError(w, http.StatusBadGateway, fmt.Sprintf("cannot reach peer %q: %v", req.Peer, err))
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

	if wantsText(r) {
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
		respondText(w, http.StatusOK, sb.String())
		return
	}

	respondJSON(w, http.StatusOK, PingResponse{Results: results, Stats: stats})
}

func (s *Server) handleTraceroute(w http.ResponseWriter, r *http.Request) {
	var req TraceRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBodySize)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Peer == "" {
		respondError(w, http.StatusBadRequest, "peer is required")
		return
	}

	net := s.runtime.Network()
	targetPeerID, err := net.ResolveName(req.Peer)
	if err != nil {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot resolve peer %q: %v", req.Peer, err))
		return
	}

	// Ensure the peer is reachable (DHT lookup + relay fallback)
	if err := s.runtime.ConnectToPeer(r.Context(), targetPeerID); err != nil {
		respondError(w, http.StatusBadGateway, fmt.Sprintf("cannot reach peer %q: %v", req.Peer, err))
		return
	}

	result, err := p2pnet.TracePeer(r.Context(), net.Host(), targetPeerID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result.Target = req.Peer

	if wantsText(r) {
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
		respondText(w, http.StatusOK, sb.String())
		return
	}

	respondJSON(w, http.StatusOK, result)
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	var req ResolveRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBodySize)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		respondError(w, http.StatusBadRequest, "name is required")
		return
	}

	net := s.runtime.Network()

	// Try to resolve as a name first
	peerID, err := net.ResolveName(req.Name)
	source := "local_config"
	if err != nil {
		// ResolveName also tries parsing as a peer ID directly
		respondError(w, http.StatusNotFound, fmt.Sprintf("cannot resolve %q: %v", req.Name, err))
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

	if wantsText(r) {
		respondText(w, http.StatusOK, fmt.Sprintf("%s → %s (source: %s)\n", resp.Name, resp.PeerID, resp.Source))
		return
	}

	respondJSON(w, http.StatusOK, resp)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req ConnectRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBodySize)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Peer == "" || req.Service == "" || req.Listen == "" {
		respondError(w, http.StatusBadRequest, "peer, service, and listen are required")
		return
	}

	pnet := s.runtime.Network()

	// Resolve peer name
	targetPeerID, err := pnet.ResolveName(req.Peer)
	if err != nil {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot resolve peer %q: %v", req.Peer, err))
		return
	}

	// Ensure the peer is reachable (DHT lookup + relay fallback)
	if err := s.runtime.ConnectToPeer(r.Context(), targetPeerID); err != nil {
		respondError(w, http.StatusBadGateway, fmt.Sprintf("cannot reach peer %q: %v", req.Peer, err))
		return
	}

	// Create dial function with retry
	dialFunc := p2pnet.DialWithRetry(func() (p2pnet.ServiceConn, error) {
		return pnet.ConnectToService(targetPeerID, req.Service)
	}, 3)

	// Create TCP listener
	listener, err := p2pnet.NewTCPListener(req.Listen, dialFunc)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create listener: %v", err))
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
	respondJSON(w, http.StatusOK, ConnectResponse{
		ID:            id,
		ListenAddress: proxy.Listen,
		PathType:      pathType,
		Address:       addr,
	})
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondError(w, http.StatusBadRequest, "proxy id is required")
		return
	}

	s.mu.Lock()
	proxy, exists := s.proxies[id]
	if exists {
		delete(s.proxies, id)
	}
	s.mu.Unlock()

	if !exists {
		respondError(w, http.StatusNotFound, fmt.Sprintf("%v: %s", ErrProxyNotFound, id))
		return
	}

	proxy.cancel()
	if proxy.listener != nil {
		proxy.listener.Close()
	}
	<-proxy.done

	slog.Info("proxy disconnected via API", "id", id)
	respondJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}

func (s *Server) handleExpose(w http.ResponseWriter, r *http.Request) {
	var req ExposeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBodySize)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.LocalAddress == "" {
		respondError(w, http.StatusBadRequest, "name and local_address are required")
		return
	}

	if err := s.runtime.Network().ExposeService(req.Name, req.LocalAddress, nil); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("service exposed via API", "service", req.Name, "local", req.LocalAddress)
	respondJSON(w, http.StatusOK, map[string]string{"status": "exposed"})
}

func (s *Server) handleUnexpose(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		respondError(w, http.StatusBadRequest, "service name is required")
		return
	}

	if err := s.runtime.Network().UnexposeService(name); err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}

	slog.Info("service unexposed via API", "service", name)
	respondJSON(w, http.StatusOK, map[string]string{"status": "unexposed"})
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{"status": "shutting down"})

	// Signal shutdown after response is sent
	go func() {
		time.Sleep(100 * time.Millisecond) // let response flush
		close(s.shutdownCh)
	}()
}

// SocketPath returns the path to the Unix socket.
func (s *Server) SocketPath() string {
	return s.socketPath
}

// Listener returns the underlying net.Listener (for health checks).
func (s *Server) Listener() net.Listener {
	return s.listener
}
