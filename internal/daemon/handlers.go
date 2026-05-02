package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/grants"
	"github.com/shurlinet/shurli/internal/relay"
	"github.com/shurlinet/shurli/internal/validate"
	"github.com/shurlinet/shurli/pkg/sdk"
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

	// Grants (per-peer data access)
	mux.HandleFunc("GET /v1/grants", s.handleGrantList)
	mux.HandleFunc("POST /v1/grants", s.handleGrantCreate)
	mux.HandleFunc("POST /v1/grants/revoke", s.handleGrantRevoke)
	mux.HandleFunc("POST /v1/grants/extend", s.handleGrantExtend)
	mux.HandleFunc("POST /v1/grants/delegate", s.handleGrantDelegate)
	mux.HandleFunc("GET /v1/grants/pouch", s.handlePouchList)

	// Persistent proxies (Item #24)
	mux.HandleFunc("GET /v1/proxies", s.handleProxyList)
	mux.HandleFunc("POST /v1/proxies", s.handleProxyAdd)
	mux.HandleFunc("DELETE /v1/proxies/{name}", s.handleProxyRemove)
	mux.HandleFunc("POST /v1/proxies/{name}/enable", s.handleProxyEnable)
	mux.HandleFunc("POST /v1/proxies/{name}/disable", s.handleProxyDisable)

	// Reconnect (manual backoff reset + redial)
	mux.HandleFunc("POST /v1/reconnect", s.handleReconnect)

	// Notifications
	mux.HandleFunc("GET /v1/notify/sinks", s.handleNotifySinks)
	mux.HandleFunc("POST /v1/notify/test", s.handleNotifyTest)

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
			"GET /v1/grants": true, "POST /v1/grants": true, "POST /v1/grants/revoke": true, "POST /v1/grants/extend": true, "POST /v1/grants/delegate": true, "GET /v1/grants/pouch": true,
			"POST /v1/reconnect": true,
			"GET /v1/proxies": true, "POST /v1/proxies": true,
			"DELETE /v1/proxies/{name}": true, "POST /v1/proxies/{name}/enable": true, "POST /v1/proxies/{name}/disable": true,
			"GET /v1/notify/sinks": true, "POST /v1/notify/test": true,
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
	grade := sdk.ComputeReachabilityGrade(rt.Interfaces(), rt.STUNResult())
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
		// Fall back to config-based relay name if agent version didn't provide one.
		if rs.RelayName == "" {
			rs.RelayName = rt.RelayNameFromConfig(pidStr)
		}
		// Sanitize relay name: may come from untrusted agent version or config.
		rs.RelayName = validate.SanitizeForDisplay(rs.RelayName)
		resp.Relays = append(resp.Relays, rs)
	}

	// Per-peer connection path summaries (for status display).
	if tracker := rt.PathTracker(); tracker != nil {
		paths := tracker.ListPeerPaths()
		if len(paths) > 0 {
			resp.PeerPaths = make(map[string]PeerPathSummary, len(paths))
			// Build relay name lookup from the relays we just populated.
			relayNames := make(map[string]string)
			for _, rs := range resp.Relays {
				if rs.RelayName != "" {
					relayNames[rs.PeerID] = rs.RelayName
				}
			}
			for _, p := range paths {
				summary := PeerPathSummary{PathType: string(p.PathType)}
				if p.PathType == "RELAYED" {
					rid := sdk.RelayPeerFromAddrStr(p.Address)
					if rid != "" {
						summary.RelayPeerID = rid
						summary.RelayName = relayNames[rid]
						if summary.RelayName == "" {
							summary.RelayName = validate.SanitizeForDisplay(rt.RelayNameFromConfig(rid))
						}
					}
				}
				resp.PeerPaths[p.PeerID] = summary
			}
		}
	}

	// TS-5: Include managed relay connections in path summaries (R9-I1).
	if pp := rt.PathProtector(); pp != nil {
		managedPaths := pp.ManagedPaths()
		if len(managedPaths) > 0 {
			if resp.PeerPaths == nil {
				resp.PeerPaths = make(map[string]PeerPathSummary)
			}
			for _, mp := range managedPaths {
				peerStr := mp.PeerID.String()
				// Only add if not already present from PathTracker (managed is backup).
				if _, exists := resp.PeerPaths[peerStr]; !exists {
					relayName := rt.RelayNameFromConfig(mp.RelayPeerID.String())
					resp.PeerPaths[peerStr] = PeerPathSummary{
						PathType:    "RELAYED",
						RelayPeerID: mp.RelayPeerID.String(),
						RelayName:   validate.SanitizeForDisplay(relayName),
					}
				}
			}
		}
	}

	// MOTD/goodbye messages from relays
	resp.MOTDs = rt.RelayMOTDs()

	// Expiring grants (E3 mitigation: MOTD-style notification on CLI commands)
	if gs := rt.GrantStore(); gs != nil {
		expiring := gs.ExpiringWithin(10 * time.Minute)
		reverseNames := s.buildReverseNames()
		for _, g := range expiring {
			info := GrantInfo{
				PeerID:    g.PeerIDStr,
				ExpiresAt: g.ExpiresAt.Format(time.RFC3339),
				Remaining: formatDuration(g.Remaining()),
			}
			if name, ok := reverseNames[g.PeerIDStr]; ok {
				info.Peer = name
			} else {
				info.Peer = truncatePeerID(g.PeerIDStr)
			}
			resp.ExpiringGrants = append(resp.ExpiringGrants, info)
		}
	}

	// Notifications section: configured sinks.
	if nr := rt.NotifyRouter(); nr != nil {
		resp.Notifications = &NotificationsStatus{
			Sinks: nr.Sinks(),
		}
	}

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

	// Client-side relay grant cache (Batch 4: CLI visibility).
	// Show per-relay grant status: active grants get details, relays without grants
	// get "no grant (signaling only)" so the user sees WHY a relay can't transfer data.
	if len(resp.Relays) > 0 {
		// Index cached receipts by relay peer ID for O(1) lookup.
		receiptByRelay := make(map[string]*grants.GrantReceipt)
		for _, r := range rt.GrantCacheSnapshot() {
			if !r.Expired() {
				receiptByRelay[r.RelayPeerID.String()] = r
			}
		}

		for _, rs := range resp.Relays {
			safeName := validate.SanitizeForDisplay(rs.RelayName)
			r, hasGrant := receiptByRelay[rs.PeerID]
			if !hasGrant {
				resp.RelayGrants = append(resp.RelayGrants, RelayGrantInfo{
					RelayPeerID: rs.PeerID,
					RelayName:   safeName,
				})
				continue
			}
			rgi := RelayGrantInfo{
				RelayPeerID: rs.PeerID,
				RelayName:   safeName,
				Permanent:   r.Permanent,
			}
			if r.Permanent {
				rgi.Remaining = "permanent"
			} else {
				rgi.Remaining = formatDuration(r.Remaining())
			}
			if r.SessionDataLimit == 0 {
				rgi.SessionBudget = "unlimited"
			} else {
				rgi.SessionBudget = sdk.FormatBytes(r.SessionDataLimit)
			}
			// Total session usage = accumulated session bytes + current circuit bytes.
			// All counters are individually clamped to MaxInt64 by TrackCircuitBytes/ResetCircuitCounters.
			sent := r.SessionBytesSent + r.CircuitBytesSent
			if sent < r.SessionBytesSent { // overflow
				sent = math.MaxInt64
			}
			recv := r.SessionBytesReceived + r.CircuitBytesReceived
			if recv < r.SessionBytesReceived { // overflow
				recv = math.MaxInt64
			}
			if sent > math.MaxInt64-recv {
				sent = math.MaxInt64 - recv
			}
			used := sent + recv
			if used > 0 {
				rgi.SessionUsed = sdk.FormatBytes(used)
			}
			// Always show remaining budget for non-unlimited sessions.
			if r.SessionDataLimit > 0 {
				rem := r.SessionDataLimit - used
				if rem < 0 {
					rem = 0
				}
				rgi.SessionRemaining = sdk.FormatBytes(rem)
			}
			if r.SessionDuration > 0 {
				rgi.SessionDuration = formatDuration(r.SessionDuration)
			}
			resp.RelayGrants = append(resp.RelayGrants, rgi)
		}
	}

	// PQC status: inspect all QUIC connections for post-quantum key exchange.
	pqcStatus := sdk.InspectPQC(h)
	resp.PQC = &pqcStatus

	// Proxy status (F8: single-command overview).
	resp.Proxies = s.ProxyStatusList()

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
		if resp.PQC != nil {
			fmt.Fprintf(&sb, "pqc_verified: %v\n", resp.PQC.QUICPQCVerified)
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
		if len(resp.RelayGrants) > 0 {
			fmt.Fprintf(&sb, "relay_grants: %d\n", len(resp.RelayGrants))
			for _, rg := range resp.RelayGrants {
				name := rg.RelayName
				if name == "" {
					name = truncatePeerID(rg.RelayPeerID)
				}
				if rg.Remaining == "" && !rg.Permanent {
					fmt.Fprintf(&sb, "  %s: no_grant\n", name)
					continue
				}
				fmt.Fprintf(&sb, "  %s: %s, budget=%s", name, rg.Remaining, rg.SessionBudget)
				if rg.SessionUsed != "" {
					fmt.Fprintf(&sb, " (used=%s)", rg.SessionUsed)
				}
				if rg.SessionDuration != "" {
					fmt.Fprintf(&sb, ", circuit=%s", rg.SessionDuration)
				}
				fmt.Fprintln(&sb)
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
		RespondError(w, http.StatusBadGateway, fmt.Sprintf("cannot reach peer %q: %s", req.Peer, sdk.HumanizeError(err.Error())))
		return
	}

	stream, err := pnet.OpenPluginStream(r.Context(), targetPeerID, "service-query")
	if err != nil {
		RespondError(w, http.StatusBadGateway, fmt.Sprintf("cannot open service-query stream: %v", err))
		return
	}
	defer stream.Close()

	services, err := sdk.QueryPeerServices(stream)
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
		RespondJSON(w, http.StatusOK, []*sdk.PeerPathInfo{})
		return
	}

	paths := tracker.ListPeerPaths()

	if WantsText(r) {
		// Build reverse name map: peerID string → name.
		reverseNames := make(map[string]string)
		if net := s.runtime.Network(); net != nil {
			for name, pid := range net.ListNames() {
				reverseNames[pid.String()] = name
			}
		}
		// Also check relay names from config.
		for _, p := range paths {
			if _, ok := reverseNames[p.PeerID]; !ok {
				if rn := s.runtime.RelayNameFromConfig(p.PeerID); rn != "" {
					reverseNames[p.PeerID] = rn
				}
			}
		}

		var sb strings.Builder
		for _, p := range paths {
			name := reverseNames[p.PeerID]
			nameCol := ""
			if name != "" {
				nameCol = " (" + name + ")"
			}
			rttStr := "-"
			if p.LastRTTMs > 0 {
				rttStr = fmt.Sprintf("%.1fms", p.LastRTTMs)
			}
			fmt.Fprintf(&sb, "%s%s\t%s\t%s\t%s\trtt=%s\n",
				p.PeerID, nameCol, p.PathType, p.Transport, p.IPVersion, rttStr)
		}

		// TS-5: append managed relay connections (R8-I1).
		if pp := s.runtime.PathProtector(); pp != nil {
			for _, mp := range pp.ManagedPaths() {
				peerStr := mp.PeerID.String()
				name := reverseNames[peerStr]
				nameCol := ""
				if name != "" {
					nameCol = " (" + name + ")"
				}
				relayName := reverseNames[mp.RelayPeerID.String()]
				if relayName == "" {
					relayName = s.runtime.RelayNameFromConfig(mp.RelayPeerID.String())
				}
				status := "[managed-backup]"
				if mp.Dead {
					status = "[managed-dead]"
				}
				fmt.Fprintf(&sb, "%s%s\tRELAYED\trelay=%s\tstreams=%d\t%s\n",
					peerStr, nameCol, relayName, mp.Streams, status)
			}
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
		RespondError(w, http.StatusBadGateway, fmt.Sprintf("cannot reach peer %q: %s", req.Peer, sdk.HumanizeError(err.Error())))
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

	ch := sdk.PingPeer(ctx, net.Host(), targetPeerID, protocolID, count, interval)

	var results []sdk.PingResult
	for result := range ch {
		results = append(results, result)
	}

	stats := sdk.ComputePingStats(results)

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
		RespondError(w, http.StatusBadGateway, fmt.Sprintf("cannot reach peer %q: %s", req.Peer, sdk.HumanizeError(err.Error())))
		return
	}

	result, err := sdk.TracePeer(r.Context(), net.Host(), targetPeerID)
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

// --- Persistent proxy handlers (Item #24) ---

func (s *Server) handleProxyList(w http.ResponseWriter, r *http.Request) {
	resp := ProxyListResponse{Proxies: s.ProxyStatusList()}
	RespondJSON(w, http.StatusOK, resp)
}

func (s *Server) handleProxyAdd(w http.ResponseWriter, r *http.Request) {
	var req ProxyAddRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxRequestBodySize)).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Peer == "" || req.Service == "" || req.Port == 0 {
		RespondError(w, http.StatusBadRequest, "name, peer, service, and port are required")
		return
	}
	if req.Port < 1 || req.Port > 65535 {
		RespondError(w, http.StatusBadRequest, "port must be 1-65535")
		return
	}

	if s.proxyStore == nil {
		RespondError(w, http.StatusServiceUnavailable, "proxy store not initialized")
		return
	}

	entry := &ProxyEntry{
		Name:    req.Name,
		Peer:    req.Peer,
		Service: req.Service,
		Port:    req.Port,
		Enabled: true,
	}
	if err := s.proxyStore.Add(entry); err != nil {
		RespondError(w, http.StatusConflict, err.Error())
		return
	}

	// R4: Bind to explicit 127.0.0.1.
	listenAddr := fmt.Sprintf("127.0.0.1:%d", req.Port)

	// Capture status and listen inside the lock to avoid data race with event loop.
	pnet := s.runtime.Network()
	status := "waiting"
	listen := listenAddr
	s.mu.Lock()
	proxy := s.startPersistentProxy(req.Name, req.Peer, req.Service, listenAddr, req.Port)
	if proxy != nil {
		s.proxies[req.Name] = proxy
		// Check if peer is already connected — flip to active immediately.
		if proxy.status == "waiting" {
			if targetPeerID, err := pnet.ResolveName(req.Peer); err == nil {
				if pnet.Host().Network().Connectedness(targetPeerID) == network.Connected {
					proxy.status = "active"
					proxy.connectedAt = time.Now()
				}
			}
		}
		status = proxy.status
		if proxy.Listen != "" {
			listen = proxy.Listen
		}
	}
	s.mu.Unlock()

	slog.Info("persistent proxy added", "name", req.Name, "peer", req.Peer, "service", req.Service, "port", req.Port)
	RespondJSON(w, http.StatusCreated, ProxyAddResponse{
		Name:          req.Name,
		ListenAddress: listen,
		Status:        status,
	})
}

func (s *Server) handleProxyRemove(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		RespondError(w, http.StatusBadRequest, "proxy name is required")
		return
	}

	if s.proxyStore == nil {
		RespondError(w, http.StatusServiceUnavailable, "proxy store not initialized")
		return
	}

	if err := s.proxyStore.Remove(name); err != nil {
		RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	// Stop the running proxy if active.
	s.mu.Lock()
	proxy, exists := s.proxies[name]
	if exists {
		delete(s.proxies, name)
	}
	s.mu.Unlock()

	if exists {
		proxy.cancel()
		if proxy.listener != nil {
			proxy.listener.GracefulClose(5 * time.Second)
		}
		<-proxy.done
	}

	slog.Info("persistent proxy removed", "name", name)
	RespondJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (s *Server) handleProxyEnable(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		RespondError(w, http.StatusBadRequest, "proxy name is required")
		return
	}

	if s.proxyStore == nil {
		RespondError(w, http.StatusServiceUnavailable, "proxy store not initialized")
		return
	}

	if err := s.proxyStore.SetEnabled(name, true); err != nil {
		RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	// Start the proxy if not already running.
	entry := s.proxyStore.Get(name)
	if entry == nil {
		RespondError(w, http.StatusNotFound, "proxy entry disappeared")
		return
	}

	listenAddr := fmt.Sprintf("127.0.0.1:%d", entry.Port)

	pnet := s.runtime.Network()
	s.mu.Lock()
	existing, exists := s.proxies[name]
	if exists && existing.status == "disabled" {
		// Replace the disabled placeholder with a live proxy.
		proxy := s.startPersistentProxy(name, entry.Peer, entry.Service, listenAddr, entry.Port)
		if proxy != nil {
			s.proxies[name] = proxy
			// Check if peer is already connected — the libp2p event already fired
			// before this proxy existed, so DetectAlreadyConnected won't catch it.
			if proxy.status == "waiting" {
				if targetPeerID, err := pnet.ResolveName(entry.Peer); err == nil {
					if pnet.Host().Network().Connectedness(targetPeerID) == network.Connected {
						proxy.status = "active"
						proxy.connectedAt = time.Now()
					}
				}
			}
		}
	}
	s.mu.Unlock()

	slog.Info("persistent proxy enabled", "name", name)
	RespondJSON(w, http.StatusOK, map[string]string{"status": "enabled"})
}

func (s *Server) handleProxyDisable(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		RespondError(w, http.StatusBadRequest, "proxy name is required")
		return
	}

	if s.proxyStore == nil {
		RespondError(w, http.StatusServiceUnavailable, "proxy store not initialized")
		return
	}

	if err := s.proxyStore.SetEnabled(name, false); err != nil {
		RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	// Stop the running proxy. Release mutex before GracefulClose (up to 5s).
	s.mu.Lock()
	proxy, exists := s.proxies[name]
	var oldListener *sdk.TCPListener
	if exists && proxy.persistent {
		proxy.cancel()
		oldListener = proxy.listener
		// Replace with disabled placeholder immediately (pre-closed done).
		s.proxies[name] = newPlaceholderProxy(
			name, proxy.Peer, proxy.Service, proxy.Listen, "disabled", proxy.Port)
	}
	s.mu.Unlock()

	// GracefulClose + wait outside the mutex.
	if exists {
		if oldListener != nil {
			oldListener.GracefulClose(5 * time.Second)
		}
		<-proxy.done
	}

	slog.Info("persistent proxy disabled", "name", name)
	RespondJSON(w, http.StatusOK, map[string]string{"status": "disabled"})
}

// --- Ephemeral proxy handlers (existing) ---

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
		RespondError(w, http.StatusBadGateway, fmt.Sprintf("cannot reach peer %q: %s", req.Peer, sdk.HumanizeError(err.Error())))
		return
	}

	// Create dial function with retry
	dialFunc := sdk.DialWithRetry(func() (sdk.ServiceConn, error) {
		return pnet.ConnectToService(targetPeerID, req.Service)
	}, 3)

	// Create TCP listener
	listener, err := sdk.NewTCPListener(req.Listen, dialFunc)
	if err != nil {
		RespondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create listener: %v", err))
		return
	}

	// Generate proxy ID
	s.mu.Lock()
	s.nextID++
	id := fmt.Sprintf("~proxy-%d", s.nextID)
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
	pathType, addr := sdk.PeerConnInfo(h, targetPeerID)

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
		RespondError(w, http.StatusBadRequest, "no relay addresses configured; add one with 'shurli relay add <address>' or run 'shurli init'")
		return
	}

	// Connect to relay and create pairing group.
	// If a specific relay was requested, resolve it; otherwise pick the first.
	h := rt.Network().Host()
	var targetAddrs []string
	if req.Relay != "" {
		// req.Relay may be a full multiaddr, a peer ID (or prefix), or a relay name.
		// Try as multiaddr first, then match by peer ID, then by config name.
		if _, parseErr := sdk.ParseRelayAddrs([]string{req.Relay}); parseErr == nil {
			targetAddrs = []string{req.Relay}
		} else {
			// Parse all configured relays and match by peer ID or name.
			for _, addr := range relayAddrs {
				addrInfo, e := sdk.ParseRelayAddrs([]string{addr})
				if e != nil || len(addrInfo) == 0 {
					continue
				}
				pidStr := addrInfo[0].ID.String()
				// Match by peer ID: exact match or prefix (min 8 chars to avoid collisions).
				if pidStr == req.Relay || (len(req.Relay) >= 8 && strings.HasPrefix(pidStr, req.Relay)) {
					targetAddrs = []string{addr}
					break
				}
				// Match by config-based relay name (case-insensitive).
				name := rt.RelayNameFromConfig(pidStr)
				if name != "" && strings.EqualFold(name, req.Relay) {
					targetAddrs = []string{addr}
					break
				}
			}
			if len(targetAddrs) == 0 {
				RespondError(w, http.StatusBadRequest, fmt.Sprintf("relay %q not found in config (try a full multiaddr, peer ID prefix, or configured relay name)", req.Relay))
				return
			}
		}
	} else {
		targetAddrs = relayAddrs
	}
	relayInfos, err := sdk.ParseRelayAddrs(targetAddrs)
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
	relayInfos, _ := sdk.ParseRelayAddrs(relayAddrs)
	if len(relayInfos) == 0 {
		RespondError(w, http.StatusInternalServerError, "no relay addresses available; add one with 'shurli relay add <address>'")
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
		relayInfos, _ := sdk.ParseRelayAddrs(relayAddrs)
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
