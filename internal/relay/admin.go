package relay

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/satindergrewal/peer-up/internal/invite"
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

// AdminServer provides a Unix socket HTTP API for the relay admin CLI.
// It runs inside the relay serve process and allows relay pair to create
// pairing groups, list them, and revoke them without direct access to
// the in-memory token store.
type AdminServer struct {
	store      *TokenStore
	gater      AdminGaterInterface
	relayAddr  string
	namespace  string
	httpServer *http.Server
	listener   net.Listener
	socketPath string
	cookiePath string
	authToken  string
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
	mux.HandleFunc("POST /v1/pair", s.handleCreatePair)
	mux.HandleFunc("GET /v1/pair", s.handleListPairs)
	mux.HandleFunc("DELETE /v1/pair/{id}", s.handleRevokePair)

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
		next.ServeHTTP(w, r)
	})
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

func respondAdminError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func generateAdminCookie() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
