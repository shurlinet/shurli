package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/deposit"
	"github.com/shurlinet/shurli/internal/invite"
	"github.com/shurlinet/shurli/internal/macaroon"
	"github.com/shurlinet/shurli/internal/vault"
	"github.com/shurlinet/shurli/internal/zkp"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// AdminGaterInterface is the subset of AuthorizedPeerGater needed by the admin socket.
type AdminGaterInterface interface {
	SetEnrollmentMode(enabled bool, limit int, timeout time.Duration)
	IsEnrollmentEnabled() bool
	UpdateAuthorizedPeers(authorizedPeers map[peer.ID]bool)
	GetAuthorizedPeerIDs() []peer.ID
}

// PairRequest is the JSON body for POST /v1/pair.
type PairRequest struct {
	Count          int      `json:"count"`
	TTLSeconds     int      `json:"ttl_seconds"`
	Namespace      string   `json:"namespace,omitempty"`
	ExpiresSeconds int      `json:"expires_seconds,omitempty"`
	Caveats        []string `json:"caveats,omitempty"` // macaroon caveats for unified invite
}

// PairResponse is the JSON response for POST /v1/pair.
type PairResponse struct {
	GroupID   string   `json:"group_id"`
	Codes    []string `json:"codes"`
	ExpiresAt string  `json:"expires_at"`
}

// VaultInitRequest is the JSON body for POST /v1/vault/init.
type VaultInitRequest struct {
	SeedBytes    []byte `json:"seed_bytes"`     // BIP39 entropy from unified seed
	Mnemonic     string `json:"mnemonic"`       // BIP39 phrase for seed hash verification
	Password     string `json:"password"`
	EnableTOTP   bool   `json:"enable_totp"`
	AutoSealMins int    `json:"auto_seal_minutes"`
}

// VaultInitResponse is the JSON response for POST /v1/vault/init.
type VaultInitResponse struct {
	TOTPUri string `json:"totp_uri,omitempty"`
}

// UnsealRequest is the JSON body for POST /v1/unseal.
type UnsealRequest struct {
	Passphrase string `json:"passphrase"`
	TOTPCode   string `json:"totp_code,omitempty"`
}

// SealStatusResponse is the JSON response for GET /v1/seal-status.
type SealStatusResponse struct {
	Sealed       bool   `json:"sealed"`
	TOTPEnabled  bool   `json:"totp_enabled"`
	AutoSealMins int    `json:"auto_seal_minutes"`
	Initialized  bool   `json:"initialized"`
}

// AdminServer provides a Unix socket HTTP API for the relay admin CLI.
// It runs inside the relay serve process and allows relay pair to create
// pairing groups, list them, and revoke them without direct access to
// the in-memory token store.
type AdminServer struct {
	store        *TokenStore
	gater        AdminGaterInterface
	vault        *vault.Vault
	vaultPath    string // where to persist vault on disk
	deposits     *deposit.DepositStore
	zkpAuth      *ZKPAuthHandler
	motdHandler  *MOTDHandler
	shutdownFunc func() // called by goodbye/shutdown endpoint
	relayAddr    string
	namespace    string
	httpServer   *http.Server
	listener     net.Listener
	socketPath   string
	cookiePath   string
	authToken    string
	authKeysPath string          // path to authorized_keys for hot-reload
	internalMux  *http.ServeMux  // route table reused by HandleRemoteRequest
	Metrics      *p2pnet.Metrics // nil-safe: metrics are optional
}

// NewAdminServer creates a new relay admin server.
func NewAdminServer(store *TokenStore, gater AdminGaterInterface, relayAddr, namespace, socketPath, cookiePath string) *AdminServer {
	return &AdminServer{
		store:      store,
		gater:      gater,
		relayAddr:  relayAddr,
		namespace:  namespace,
		socketPath: socketPath,
		cookiePath: cookiePath,
	}
}

// SetVault attaches a vault to the admin server for seal/unseal management.
func (s *AdminServer) SetVault(v *vault.Vault, vaultPath string) {
	s.vault = v
	s.vaultPath = vaultPath
}

// SetDepositStore attaches an invite deposit store. The macaroon root key
// is retrieved from the vault dynamically when needed (unsealed state only).
func (s *AdminServer) SetDepositStore(ds *deposit.DepositStore) {
	s.deposits = ds
}

// SetZKPAuth attaches the ZKP auth handler for tree-rebuild and tree-info endpoints.
func (s *AdminServer) SetZKPAuth(h *ZKPAuthHandler) {
	s.zkpAuth = h
}

// SetMOTDHandler attaches the MOTD handler for motd/goodbye endpoints.
func (s *AdminServer) SetMOTDHandler(h *MOTDHandler) {
	s.motdHandler = h
}

// SetShutdownFunc sets the callback invoked by POST /v1/goodbye/shutdown.
func (s *AdminServer) SetShutdownFunc(fn func()) {
	s.shutdownFunc = fn
}

// SetAuthKeysPath sets the path to the authorized_keys file for hot-reload.
func (s *AdminServer) SetAuthKeysPath(path string) {
	s.authKeysPath = path
}

// buildMux creates the HTTP route table. Called once by Start() and reused
// by HandleRemoteRequest so that the remote admin protocol dispatches to
// the same handler functions as the local Unix socket.
func (s *AdminServer) buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/pair", s.handleCreatePair)
	mux.HandleFunc("GET /v1/pair", s.handleListPairs)
	mux.HandleFunc("DELETE /v1/pair/{id}", s.handleRevokePair)

	// Invite deposit endpoints (require unsealed vault for mutation)
	mux.HandleFunc("POST /v1/invite", s.requireUnsealedOr(s.handleCreateInvite))
	mux.HandleFunc("GET /v1/invite", s.handleListInvites)
	mux.HandleFunc("DELETE /v1/invite/{id}", s.handleRevokeInvite)
	mux.HandleFunc("PATCH /v1/invite/{id}", s.requireUnsealedOr(s.handleModifyInvite))

	// Vault management endpoints (always available, even when sealed)
	mux.HandleFunc("POST /v1/unseal", s.handleUnseal)
	mux.HandleFunc("POST /v1/seal", s.handleSeal)
	mux.HandleFunc("GET /v1/seal-status", s.handleSealStatus)
	mux.HandleFunc("POST /v1/vault/init", s.handleVaultInit)
	mux.HandleFunc("GET /v1/vault/totp-uri", s.handleVaultTOTPUri)

	// Peer management endpoints (ACL mutations, no vault required)
	mux.HandleFunc("GET /v1/peers", s.handleListPeers)
	mux.HandleFunc("POST /v1/peers/authorize", s.handleAuthorizePeer)
	mux.HandleFunc("POST /v1/peers/deauthorize", s.handleDeauthorizePeer)

	// Auth hot-reload endpoint
	mux.HandleFunc("POST /v1/auth/reload", s.handleAuthReload)

	// ZKP tree management endpoints
	mux.HandleFunc("POST /v1/zkp/tree-rebuild", s.requireUnsealedOr(s.handleZKPTreeRebuild))
	mux.HandleFunc("GET /v1/zkp/tree-info", s.handleZKPTreeInfo)

	// ZKP circuit parameter distribution (public data, no vault-gate needed).
	// Clients fetch these to generate proofs locally.
	mux.HandleFunc("GET /v1/zkp/proving-key", s.handleZKPProvingKey)
	mux.HandleFunc("GET /v1/zkp/verifying-key", s.handleZKPVerifyingKey)

	// MOTD and goodbye endpoints
	mux.HandleFunc("GET /v1/motd", s.handleGetMOTD)
	mux.HandleFunc("PUT /v1/motd", s.handleSetMOTD)
	mux.HandleFunc("DELETE /v1/motd", s.handleClearMOTD)
	mux.HandleFunc("GET /v1/goodbye", s.handleGetGoodbye)
	mux.HandleFunc("PUT /v1/goodbye", s.handleSetGoodbye)
	mux.HandleFunc("DELETE /v1/goodbye", s.handleRetractGoodbye)
	mux.HandleFunc("POST /v1/goodbye/shutdown", s.handleGoodbyeShutdown)

	return mux
}

// Start creates the Unix socket, writes the cookie file, and starts serving.
func (s *AdminServer) Start() error {
	token, err := generateAdminCookie()
	if err != nil {
		return fmt.Errorf("failed to generate admin cookie: %w", err)
	}
	s.authToken = token

	if err := s.checkStaleSocket(); err != nil {
		return err
	}

	// Bind with restrictive umask to avoid TOCTOU race.
	oldUmask := syscall.Umask(0077)
	listener, err := net.Listen("unix", s.socketPath)
	syscall.Umask(oldUmask)
	if err != nil {
		return fmt.Errorf("failed to listen on admin socket: %w", err)
	}

	// Write cookie after socket is secured.
	if err := os.WriteFile(s.cookiePath, []byte(token), 0600); err != nil {
		listener.Close()
		os.Remove(s.socketPath)
		return fmt.Errorf("failed to write admin cookie: %w", err)
	}

	s.listener = listener
	s.internalMux = s.buildMux()

	s.httpServer = &http.Server{
		Handler:      s.authMiddleware(s.internalMux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		if err := s.httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.Error("relay admin server error", "error", err)
		}
	}()

	slog.Info("relay admin API listening", "socket", s.socketPath)
	return nil
}

// HandleRemoteRequest dispatches a request to the internal mux without
// going through the cookie auth middleware. Used by RemoteAdminHandler
// where authentication is done via libp2p peer identity instead.
// Returns the HTTP status code and response body bytes.
func (s *AdminServer) HandleRemoteRequest(method, path string, body []byte) (int, []byte) {
	if s.internalMux == nil {
		s.internalMux = s.buildMux()
	}

	var reqBody io.Reader
	if len(body) > 0 {
		reqBody = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, "http://relay-admin"+path, reqBody)
	if err != nil {
		resp, _ := json.Marshal(map[string]string{"error": "invalid request"})
		return http.StatusBadRequest, resp
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	s.internalMux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// Stop gracefully shuts down the server and cleans up socket/cookie files.
func (s *AdminServer) Stop() {
	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.httpServer.Shutdown(ctx)
	}
	os.Remove(s.socketPath)
	os.Remove(s.cookiePath)
	slog.Info("relay admin server stopped")
}

func (s *AdminServer) checkStaleSocket() error {
	if _, err := os.Stat(s.socketPath); os.IsNotExist(err) {
		return nil
	}

	conn, err := net.DialTimeout("unix", s.socketPath, 2*time.Second)
	if err != nil {
		slog.Info("removing stale relay admin socket", "path", s.socketPath)
		os.Remove(s.socketPath)
		return nil
	}

	conn.Close()
	return fmt.Errorf("relay admin socket already in use: %s", s.socketPath)
}

func (s *AdminServer) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		expected := "Bearer " + s.authToken
		if subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		// Record admin request metrics (nil-safe).
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		if s.Metrics != nil {
			endpoint := r.Method + " " + r.URL.Path
			s.Metrics.AdminRequestTotal.WithLabelValues(endpoint, fmt.Sprintf("%d", rw.status)).Inc()
			s.Metrics.AdminRequestDurationSeconds.WithLabelValues(endpoint).Observe(time.Since(start).Seconds())
		}
	})
}

// statusRecorder wraps http.ResponseWriter to capture the status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *AdminServer) handleCreatePair(w http.ResponseWriter, r *http.Request) {
	var req PairRequest
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondAdminError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Count < 1 {
		req.Count = 1
	}
	if req.Count > 100 {
		respondAdminError(w, http.StatusBadRequest, "count exceeds maximum (100)")
		return
	}
	if req.TTLSeconds < 1 {
		req.TTLSeconds = 3600 // 1 hour default
	}

	ttl := time.Duration(req.TTLSeconds) * time.Second
	ns := req.Namespace
	if ns == "" {
		ns = s.namespace
	}

	var peerTTL time.Duration
	if req.ExpiresSeconds > 0 {
		peerTTL = time.Duration(req.ExpiresSeconds) * time.Second
	}

	// Use short 10-byte tokens for v3 short codes.
	tokens, groupID, err := s.store.CreateGroupShort(req.Count, ttl, ns, peerTTL)
	if err != nil {
		respondAdminError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create group: %v", err))
		return
	}

	// If caveats are provided and vault is available, create a macaroon deposit
	// and link it to the pairing group (unified invite).
	if len(req.Caveats) > 0 && s.vault != nil && s.deposits != nil {
		rootKey, err := s.vault.RootKey()
		if err == nil {
			m := macaroon.New(s.relayAddr, rootKey, fmt.Sprintf("invite-%s", groupID))
			for _, c := range req.Caveats {
				m.AddFirstPartyCaveat(c)
			}
			dep, err := s.deposits.Create(m, "admin", ttl)
			if err != nil {
				slog.Warn("failed to create invite deposit for group", "group", groupID, "err", err)
			} else {
				s.store.SetDepositID(groupID, dep.ID)
				s.recordDepositOp("create")
				s.recordDepositPending()
			}
		}
	}

	// Enable enrollment mode so joining peers can connect.
	if s.gater != nil {
		s.gater.SetEnrollmentMode(true, 10, 10*time.Second)
	}

	// Encode tokens into short v3 invite codes.
	codes := make([]string, len(tokens))
	for i, tok := range tokens {
		code, err := invite.EncodeShort(tok)
		if err != nil {
			respondAdminError(w, http.StatusInternalServerError, fmt.Sprintf("failed to encode code: %v", err))
			return
		}
		codes[i] = code
	}

	expiresAt := time.Now().Add(ttl)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PairResponse{
		GroupID:   groupID,
		Codes:    codes,
		ExpiresAt: expiresAt.Format(time.RFC3339),
	})

	slog.Info("pairing group created via admin", "group", groupID, "count", req.Count, "ttl", ttl)
}

func (s *AdminServer) handleListPairs(w http.ResponseWriter, r *http.Request) {
	groups := s.store.List()

	type peerJSON struct {
		PeerID string `json:"peer_id"`
		Name   string `json:"name"`
	}
	type groupJSON struct {
		ID        string     `json:"id"`
		Namespace string     `json:"namespace,omitempty"`
		ExpiresAt string     `json:"expires_at"`
		Total     int        `json:"total"`
		Used      int        `json:"used"`
		Peers     []peerJSON `json:"peers,omitempty"`
	}

	result := make([]groupJSON, len(groups))
	for i, g := range groups {
		gj := groupJSON{
			ID:        g.ID,
			Namespace: g.Namespace,
			ExpiresAt: g.ExpiresAt.Format(time.RFC3339),
			Total:     g.Total,
			Used:      g.Used,
		}
		for _, p := range g.Peers {
			gj.Peers = append(gj.Peers, peerJSON{
				PeerID: p.PeerID.String(),
				Name:   p.Name,
			})
		}
		result[i] = gj
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *AdminServer) handleRevokePair(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	if groupID == "" {
		// Fallback: extract from URL path.
		parts := strings.Split(strings.TrimRight(r.URL.Path, "/"), "/")
		if len(parts) >= 4 {
			groupID = parts[3]
		}
	}

	if groupID == "" {
		respondAdminError(w, http.StatusBadRequest, "missing group ID")
		return
	}

	if err := s.store.Revoke(groupID); err != nil {
		respondAdminError(w, http.StatusNotFound, err.Error())
		return
	}

	// Disable enrollment if no active groups remain.
	if s.gater != nil && s.store.ActiveGroupCount() == 0 {
		s.gater.SetEnrollmentMode(false, 0, 0)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "revoked"})

	slog.Info("pairing group revoked via admin", "group", groupID)
}

// --- Invite deposit endpoints ---

func (s *AdminServer) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	if s.deposits == nil {
		respondAdminError(w, http.StatusServiceUnavailable, "invite deposits not configured")
		return
	}
	if s.vault == nil {
		respondAdminError(w, http.StatusServiceUnavailable, "vault not initialized")
		return
	}

	rootKey, err := s.vault.RootKey()
	if err != nil {
		respondAdminError(w, http.StatusServiceUnavailable, "vault is sealed: unseal first")
		return
	}

	var req struct {
		Caveats    []string `json:"caveats"`
		TTLSeconds int      `json:"ttl_seconds,omitempty"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondAdminError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Create macaroon with the vault's root key
	m := macaroon.New(s.relayAddr, rootKey, fmt.Sprintf("invite-%d", time.Now().UnixNano()))
	for _, c := range req.Caveats {
		m.AddFirstPartyCaveat(c)
	}

	var ttl time.Duration
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}

	dep, err := s.deposits.Create(m, "admin", ttl)
	if err != nil {
		respondAdminError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create deposit: %v", err))
		return
	}

	// Encode macaroon for transport
	macB64, _ := m.EncodeBase64()

	s.recordDepositOp("create")
	s.recordDepositPending()
	slog.Info("invite deposit created", "id", dep.ID, "caveats", len(req.Caveats))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"id":        dep.ID,
		"macaroon":  macB64,
		"status":    string(dep.Status),
		"expires_at": func() string {
			if dep.ExpiresAt.IsZero() {
				return ""
			}
			return dep.ExpiresAt.Format(time.RFC3339)
		}(),
	})
}

func (s *AdminServer) handleListInvites(w http.ResponseWriter, _ *http.Request) {
	if s.deposits == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]any{})
		return
	}

	all := s.deposits.List("")

	type inviteJSON struct {
		ID         string `json:"id"`
		Status     string `json:"status"`
		CreatedBy  string `json:"created_by"`
		CreatedAt  string `json:"created_at"`
		ExpiresAt  string `json:"expires_at,omitempty"`
		ConsumedBy string `json:"consumed_by,omitempty"`
		Caveats    int    `json:"caveats"`
	}

	result := make([]inviteJSON, len(all))
	for i, d := range all {
		result[i] = inviteJSON{
			ID:         d.ID,
			Status:     string(d.Status),
			CreatedBy:  d.CreatedBy,
			CreatedAt:  d.CreatedAt.Format(time.RFC3339),
			ConsumedBy: d.ConsumedBy,
			Caveats:    len(d.Macaroon.Caveats),
		}
		if !d.ExpiresAt.IsZero() {
			result[i].ExpiresAt = d.ExpiresAt.Format(time.RFC3339)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *AdminServer) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	if s.deposits == nil {
		respondAdminError(w, http.StatusServiceUnavailable, "invite deposits not configured")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		parts := strings.Split(strings.TrimRight(r.URL.Path, "/"), "/")
		if len(parts) >= 4 {
			id = parts[3]
		}
	}

	if id == "" {
		respondAdminError(w, http.StatusBadRequest, "missing invite ID")
		return
	}

	if err := s.deposits.Revoke(id); err != nil {
		if errors.Is(err, deposit.ErrDepositNotFound) {
			respondAdminError(w, http.StatusNotFound, err.Error())
		} else {
			respondAdminError(w, http.StatusConflict, err.Error())
		}
		return
	}

	s.recordDepositOp("revoke")
	s.recordDepositPending()
	slog.Info("invite deposit revoked", "id", id)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "revoked"})
}

func (s *AdminServer) handleModifyInvite(w http.ResponseWriter, r *http.Request) {
	if s.deposits == nil {
		respondAdminError(w, http.StatusServiceUnavailable, "invite deposits not configured")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		parts := strings.Split(strings.TrimRight(r.URL.Path, "/"), "/")
		if len(parts) >= 4 {
			id = parts[3]
		}
	}

	if id == "" {
		respondAdminError(w, http.StatusBadRequest, "missing invite ID")
		return
	}

	var req struct {
		AddCaveats []string `json:"add_caveats"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondAdminError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.AddCaveats) == 0 {
		respondAdminError(w, http.StatusBadRequest, "add_caveats required (attenuation only)")
		return
	}

	for _, c := range req.AddCaveats {
		if err := s.deposits.AddCaveat(id, c); err != nil {
			if errors.Is(err, deposit.ErrDepositNotFound) {
				respondAdminError(w, http.StatusNotFound, err.Error())
			} else {
				respondAdminError(w, http.StatusConflict, err.Error())
			}
			return
		}
	}

	s.recordDepositOp("modify")
	slog.Info("invite deposit modified", "id", id, "added_caveats", len(req.AddCaveats))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "modified", "added_caveats": fmt.Sprintf("%d", len(req.AddCaveats))})
}

// requireUnsealedOr wraps a handler to return 503 when the vault is sealed.
// Endpoints that mutate state (create pairing groups, etc.) must not operate
// while the vault is sealed. Read-only endpoints bypass this guard.
func (s *AdminServer) requireUnsealedOr(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.vault != nil && s.vault.IsSealed() {
			respondAdminError(w, http.StatusServiceUnavailable, "vault is sealed: unseal first")
			return
		}
		next(w, r)
	}
}

func (s *AdminServer) handleUnseal(w http.ResponseWriter, r *http.Request) {
	if s.vault == nil {
		respondAdminError(w, http.StatusNotFound, "vault not configured")
		return
	}

	var req UnsealRequest
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondAdminError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Passphrase == "" {
		respondAdminError(w, http.StatusBadRequest, "passphrase required")
		return
	}

	if err := s.vault.Unseal(req.Passphrase, req.TOTPCode); err != nil {
		switch {
		case errors.Is(err, vault.ErrInvalidPassword):
			respondAdminError(w, http.StatusForbidden, "invalid passphrase")
		case errors.Is(err, vault.ErrInvalidTOTP):
			respondAdminError(w, http.StatusForbidden, "invalid TOTP code")
		case errors.Is(err, vault.ErrVaultAlreadyUnsealed):
			respondAdminError(w, http.StatusConflict, "vault is already unsealed")
		default:
			respondAdminError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	s.recordVaultSealOp("unseal_admin")
	s.recordVaultSealState(false)
	slog.Info("vault unsealed via admin socket")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "unsealed"})
}

func (s *AdminServer) handleSeal(w http.ResponseWriter, r *http.Request) {
	if s.vault == nil {
		respondAdminError(w, http.StatusNotFound, "vault not configured")
		return
	}

	s.vault.Seal()
	s.recordVaultSealOp("seal_admin")
	s.recordVaultSealState(true)
	slog.Info("vault sealed via admin socket")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "sealed"})
}

func (s *AdminServer) handleSealStatus(w http.ResponseWriter, r *http.Request) {
	resp := SealStatusResponse{
		Initialized: s.vault != nil,
	}
	if s.vault != nil {
		resp.Sealed = s.vault.IsSealed()
		resp.AutoSealMins = s.vault.AutoSealMinutes()
		resp.TOTPEnabled = s.vault.TOTPEnabled()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *AdminServer) handleVaultInit(w http.ResponseWriter, r *http.Request) {
	if s.vault != nil {
		respondAdminError(w, http.StatusConflict, "vault already initialized")
		return
	}

	var req VaultInitRequest
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondAdminError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Password == "" {
		respondAdminError(w, http.StatusBadRequest, "password required")
		return
	}
	if len(req.SeedBytes) == 0 {
		respondAdminError(w, http.StatusBadRequest, "seed_bytes required")
		return
	}

	v, err := vault.Create(req.SeedBytes, req.Mnemonic, req.Password, req.EnableTOTP, req.AutoSealMins)
	if err != nil {
		respondAdminError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create vault: %v", err))
		return
	}

	if s.vaultPath != "" {
		if err := v.Save(s.vaultPath); err != nil {
			respondAdminError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save vault: %v", err))
			return
		}
	}

	s.vault = v

	var resp VaultInitResponse
	if req.EnableTOTP {
		uri, _ := v.TOTPProvisioningURI(s.relayAddr)
		resp.TOTPUri = uri
	}

	slog.Info("vault initialized via admin socket", "totp", req.EnableTOTP, "auto_seal_mins", req.AutoSealMins)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *AdminServer) handleVaultTOTPUri(w http.ResponseWriter, r *http.Request) {
	if s.vault == nil {
		respondAdminError(w, http.StatusNotFound, "vault not configured")
		return
	}
	if s.vault.IsSealed() {
		respondAdminError(w, http.StatusServiceUnavailable, "vault is sealed")
		return
	}

	uri, err := s.vault.TOTPProvisioningURI(s.relayAddr)
	if err != nil {
		respondAdminError(w, http.StatusNotFound, "TOTP not enabled")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"uri": uri})
}

// --- Peer management endpoints ---

func (s *AdminServer) handleListPeers(w http.ResponseWriter, r *http.Request) {
	if s.authKeysPath == "" {
		respondAdminError(w, http.StatusBadRequest, "no authorized_keys path configured")
		return
	}

	peers, err := auth.ListPeers(s.authKeysPath)
	if err != nil {
		respondAdminError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to list peers: %v", err))
		return
	}

	result := make([]AuthorizedPeerInfo, len(peers))
	for i, p := range peers {
		role := p.Role
		if role == "" {
			role = "member"
		}
		result[i] = AuthorizedPeerInfo{
			PeerID:    p.PeerID.String(),
			Role:      role,
			Comment:   p.Comment,
			Verified:  p.Verified,
			Group:     p.Group,
			RelayData: p.RelayData,
		}
		if !p.ExpiresAt.IsZero() {
			result[i].ExpiresAt = p.ExpiresAt.Format(time.RFC3339)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *AdminServer) handleAuthorizePeer(w http.ResponseWriter, r *http.Request) {
	if s.authKeysPath == "" {
		respondAdminError(w, http.StatusBadRequest, "no authorized_keys path configured")
		return
	}

	var req struct {
		PeerID  string `json:"peer_id"`
		Comment string `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondAdminError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.PeerID == "" {
		respondAdminError(w, http.StatusBadRequest, "peer_id is required")
		return
	}

	if err := auth.AddPeer(s.authKeysPath, req.PeerID, req.Comment); err != nil {
		respondAdminError(w, http.StatusBadRequest, fmt.Sprintf("failed to authorize peer: %v", err))
		return
	}

	// Trigger auth reload (gater + ZKP tree) like handleAuthReload does.
	s.reloadAuth()

	slog.Info("peer authorized via admin", "peer_id", req.PeerID[:min(16, len(req.PeerID))]+"...")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "authorized",
		"peer_id": req.PeerID,
	})
}

func (s *AdminServer) handleDeauthorizePeer(w http.ResponseWriter, r *http.Request) {
	if s.authKeysPath == "" {
		respondAdminError(w, http.StatusBadRequest, "no authorized_keys path configured")
		return
	}

	var req struct {
		PeerID string `json:"peer_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondAdminError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.PeerID == "" {
		respondAdminError(w, http.StatusBadRequest, "peer_id is required")
		return
	}

	if err := auth.RemovePeer(s.authKeysPath, req.PeerID); err != nil {
		respondAdminError(w, http.StatusBadRequest, fmt.Sprintf("failed to deauthorize peer: %v", err))
		return
	}

	// Trigger auth reload (gater + ZKP tree).
	s.reloadAuth()

	slog.Info("peer deauthorized via admin", "peer_id", req.PeerID[:min(16, len(req.PeerID))]+"...")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "deauthorized",
		"peer_id": req.PeerID,
	})
}

// reloadAuth reloads the gater from authorized_keys and rebuilds the ZKP tree.
// Shared by handleAuthorizePeer, handleDeauthorizePeer, and handleAuthReload.
func (s *AdminServer) reloadAuth() {
	if s.gater == nil || s.authKeysPath == "" {
		return
	}
	peers, err := auth.LoadAuthorizedKeys(s.authKeysPath)
	if err != nil {
		slog.Warn("auth reload after peer mutation failed", "err", err)
		return
	}
	s.gater.UpdateAuthorizedPeers(peers)
	if s.zkpAuth != nil {
		if err := s.zkpAuth.RebuildTree(); err != nil {
			slog.Warn("zkp tree rebuild after peer mutation failed", "err", err)
		}
	}
}

// --- Auth hot-reload endpoint ---

func (s *AdminServer) handleAuthReload(w http.ResponseWriter, r *http.Request) {
	if s.gater == nil {
		respondAdminError(w, http.StatusBadRequest, "connection gating is not enabled")
		return
	}
	if s.authKeysPath == "" {
		respondAdminError(w, http.StatusBadRequest, "no authorized_keys path configured")
		return
	}

	peers, err := auth.LoadAuthorizedKeys(s.authKeysPath)
	if err != nil {
		respondAdminError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to reload authorized_keys: %v", err))
		return
	}

	s.gater.UpdateAuthorizedPeers(peers)

	// Rebuild ZKP Merkle tree if enabled (new peers change the tree).
	if s.zkpAuth != nil {
		if err := s.zkpAuth.RebuildTree(); err != nil {
			slog.Warn("auth reload: zkp tree rebuild failed", "err", err)
		}
	}

	slog.Info("authorized_keys reloaded via admin socket", "peers", len(peers))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "reloaded",
		"peers":  len(peers),
	})
}

// ZKPTreeInfoResponse is the JSON response for GET /v1/zkp/tree-info.
type ZKPTreeInfoResponse struct {
	Ready     bool   `json:"ready"`
	Root      string `json:"root,omitempty"`
	LeafCount int    `json:"leaf_count,omitempty"`
	Depth     int    `json:"depth,omitempty"`
}

func (s *AdminServer) handleZKPTreeRebuild(w http.ResponseWriter, r *http.Request) {
	if s.zkpAuth == nil {
		respondAdminError(w, http.StatusNotFound, "zkp auth not configured")
		return
	}

	if err := s.zkpAuth.RebuildTree(); err != nil {
		respondAdminError(w, http.StatusInternalServerError, fmt.Sprintf("tree rebuild failed: %v", err))
		return
	}

	root, leafCount, depth, _ := s.zkpAuth.TreeInfo()
	slog.Info("zkp tree rebuilt via admin socket", "leaves", leafCount, "depth", depth)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":     "rebuilt",
		"leaf_count": leafCount,
		"depth":      depth,
		"root":       fmt.Sprintf("%x", root),
	})
}

func (s *AdminServer) handleZKPTreeInfo(w http.ResponseWriter, r *http.Request) {
	if s.zkpAuth == nil {
		respondAdminError(w, http.StatusNotFound, "zkp auth not configured")
		return
	}

	root, leafCount, depth, ok := s.zkpAuth.TreeInfo()
	resp := ZKPTreeInfoResponse{Ready: ok}
	if ok {
		resp.Root = fmt.Sprintf("%x", root)
		resp.LeafCount = leafCount
		resp.Depth = depth
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// maxProvingKeySize is the upper bound for a PLONK proving key file (5 MB).
// Actual size is ~2 MB; this guards against corrupted or replaced files.
const maxProvingKeySize = 5 * 1024 * 1024

// maxVerifyingKeySize is the upper bound for a PLONK verifying key file (256 KB).
// Actual size is ~34 KB; this guards against corrupted or replaced files.
const maxVerifyingKeySize = 256 * 1024

func (s *AdminServer) handleZKPProvingKey(w http.ResponseWriter, r *http.Request) {
	if s.zkpAuth == nil {
		respondAdminError(w, http.StatusNotFound, "zkp auth not configured")
		return
	}
	path := zkp.ProvingKeyPath(s.zkpAuth.KeysDir())
	fi, err := os.Stat(path)
	if err != nil {
		respondAdminError(w, http.StatusNotFound, "proving key not found")
		return
	}
	if fi.Size() > maxProvingKeySize {
		respondAdminError(w, http.StatusInternalServerError, "proving key file exceeds size limit")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=provingKey.bin")
	http.ServeFile(w, r, path)
}

func (s *AdminServer) handleZKPVerifyingKey(w http.ResponseWriter, r *http.Request) {
	if s.zkpAuth == nil {
		respondAdminError(w, http.StatusNotFound, "zkp auth not configured")
		return
	}
	path := zkp.VerifyingKeyPath(s.zkpAuth.KeysDir())
	fi, err := os.Stat(path)
	if err != nil {
		respondAdminError(w, http.StatusNotFound, "verifying key not found")
		return
	}
	if fi.Size() > maxVerifyingKeySize {
		respondAdminError(w, http.StatusInternalServerError, "verifying key file exceeds size limit")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=verifyingKey.bin")
	http.ServeFile(w, r, path)
}

// --- MOTD and goodbye endpoints ---

// MOTDStatusResponse is the JSON response for GET /v1/motd and GET /v1/goodbye.
type MOTDStatusResponse struct {
	MOTD           string `json:"motd"`
	Goodbye        string `json:"goodbye"`
	GoodbyeActive  bool   `json:"goodbye_active"`
}

func (s *AdminServer) handleGetMOTD(w http.ResponseWriter, _ *http.Request) {
	resp := MOTDStatusResponse{}
	if s.motdHandler != nil {
		resp.MOTD = s.motdHandler.MOTD()
		resp.Goodbye = s.motdHandler.Goodbye()
		resp.GoodbyeActive = s.motdHandler.HasActiveGoodbye()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *AdminServer) handleSetMOTD(w http.ResponseWriter, r *http.Request) {
	if s.motdHandler == nil {
		respondAdminError(w, http.StatusNotFound, "motd handler not configured")
		return
	}

	var req struct {
		Message string `json:"message"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondAdminError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Message == "" {
		respondAdminError(w, http.StatusBadRequest, "message required")
		return
	}

	s.motdHandler.SetMOTD(req.Message)
	slog.Info("motd set via admin", "len", len(req.Message))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "set"})
}

func (s *AdminServer) handleClearMOTD(w http.ResponseWriter, _ *http.Request) {
	if s.motdHandler == nil {
		respondAdminError(w, http.StatusNotFound, "motd handler not configured")
		return
	}
	s.motdHandler.ClearMOTD()
	slog.Info("motd cleared via admin")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "cleared"})
}

func (s *AdminServer) handleGetGoodbye(w http.ResponseWriter, _ *http.Request) {
	resp := MOTDStatusResponse{}
	if s.motdHandler != nil {
		resp.MOTD = s.motdHandler.MOTD()
		resp.Goodbye = s.motdHandler.Goodbye()
		resp.GoodbyeActive = s.motdHandler.HasActiveGoodbye()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *AdminServer) handleSetGoodbye(w http.ResponseWriter, r *http.Request) {
	if s.motdHandler == nil {
		respondAdminError(w, http.StatusNotFound, "motd handler not configured")
		return
	}

	var req struct {
		Message string `json:"message"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondAdminError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Message == "" {
		respondAdminError(w, http.StatusBadRequest, "message required")
		return
	}

	if err := s.motdHandler.SetGoodbye(req.Message); err != nil {
		respondAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("goodbye set via admin", "msg", req.Message)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "set"})
}

func (s *AdminServer) handleRetractGoodbye(w http.ResponseWriter, _ *http.Request) {
	if s.motdHandler == nil {
		respondAdminError(w, http.StatusNotFound, "motd handler not configured")
		return
	}

	if err := s.motdHandler.RetractGoodbye(); err != nil {
		respondAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("goodbye retracted via admin")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "retracted"})
}

func (s *AdminServer) handleGoodbyeShutdown(w http.ResponseWriter, r *http.Request) {
	if s.motdHandler == nil {
		respondAdminError(w, http.StatusNotFound, "motd handler not configured")
		return
	}

	var req struct {
		Message string `json:"message"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondAdminError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Message == "" {
		req.Message = "Relay shutting down"
	}

	if err := s.motdHandler.SetGoodbye(req.Message); err != nil {
		respondAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("goodbye shutdown initiated via admin", "msg", req.Message)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "goodbye_sent_shutting_down"})

	// Trigger shutdown asynchronously so the response can be sent first.
	if s.shutdownFunc != nil {
		go func() {
			time.Sleep(2 * time.Second) // allow goodbye delivery
			s.shutdownFunc()
		}()
	}
}

func respondAdminError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// recordDepositOp increments the deposit operations counter. Nil-safe.
func (s *AdminServer) recordDepositOp(operation string) {
	if s.Metrics != nil {
		s.Metrics.DepositOpsTotal.WithLabelValues(operation).Inc()
	}
}

// recordDepositPending sets the pending deposit gauge. Nil-safe.
func (s *AdminServer) recordDepositPending() {
	if s.Metrics != nil && s.deposits != nil {
		s.Metrics.DepositPending.Set(float64(len(s.deposits.List("pending"))))
	}
}

// recordVaultSealOp increments the vault seal operations counter. Nil-safe.
func (s *AdminServer) recordVaultSealOp(trigger string) {
	if s.Metrics != nil {
		s.Metrics.VaultSealOpsTotal.WithLabelValues(trigger).Inc()
	}
}

// recordVaultSealState sets the vault sealed gauge. Nil-safe.
func (s *AdminServer) recordVaultSealState(sealed bool) {
	if s.Metrics != nil {
		if sealed {
			s.Metrics.VaultSealed.Set(1)
		} else {
			s.Metrics.VaultSealed.Set(0)
		}
	}
}

func generateAdminCookie() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
