package relay

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/shurlinet/shurli/internal/deposit"
	"github.com/shurlinet/shurli/internal/invite"
	"github.com/shurlinet/shurli/internal/macaroon"
	"github.com/shurlinet/shurli/internal/vault"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// AdminGaterInterface is the subset of AuthorizedPeerGater needed by the admin socket.
type AdminGaterInterface interface {
	SetEnrollmentMode(enabled bool, limit int, timeout time.Duration)
	IsEnrollmentEnabled() bool
}

// PairRequest is the JSON body for POST /v1/pair.
type PairRequest struct {
	Count      int `json:"count"`
	TTLSeconds int `json:"ttl_seconds"`
	Namespace  string `json:"namespace,omitempty"`
	ExpiresSeconds int `json:"expires_seconds,omitempty"`
}

// PairResponse is the JSON response for POST /v1/pair.
type PairResponse struct {
	GroupID   string   `json:"group_id"`
	Codes    []string `json:"codes"`
	ExpiresAt string  `json:"expires_at"`
}

// VaultInitRequest is the JSON body for POST /v1/vault/init.
type VaultInitRequest struct {
	Passphrase   string `json:"passphrase"`
	EnableTOTP   bool   `json:"enable_totp"`
	AutoSealMins int    `json:"auto_seal_minutes"`
}

// VaultInitResponse is the JSON response for POST /v1/vault/init.
type VaultInitResponse struct {
	SeedPhrase string `json:"seed_phrase"`
	TOTPUri    string `json:"totp_uri,omitempty"`
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
	relayAddr    string
	namespace    string
	httpServer   *http.Server
	listener     net.Listener
	socketPath   string
	cookiePath   string
	authToken    string
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

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/pair", s.requireUnsealedOr(s.handleCreatePair))
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

	s.httpServer = &http.Server{
		Handler:      s.authMiddleware(mux),
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

	tokens, groupID, err := s.store.CreateGroup(req.Count, ttl, ns, peerTTL)
	if err != nil {
		respondAdminError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create group: %v", err))
		return
	}

	// Enable enrollment mode so joining peers can connect.
	if s.gater != nil {
		s.gater.SetEnrollmentMode(true, 10, 15*time.Second)
	}

	// Encode tokens into v2 invite codes.
	codes := make([]string, len(tokens))
	for i, tok := range tokens {
		code, err := invite.EncodeV2(tok, s.relayAddr, ns)
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
		case errors.Is(err, vault.ErrInvalidPassphrase):
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

	if req.Passphrase == "" {
		respondAdminError(w, http.StatusBadRequest, "passphrase required")
		return
	}

	v, seedPhrase, err := vault.Create(req.Passphrase, req.EnableTOTP, req.AutoSealMins)
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

	resp := VaultInitResponse{SeedPhrase: seedPhrase}
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
