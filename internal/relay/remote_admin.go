package relay

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// RemoteAdminProtocol is the libp2p protocol ID for remote relay administration.
// Replaces the old /shurli/relay-unseal/1.0.0 protocol with a general-purpose
// admin channel that supports all admin operations over P2P.
const RemoteAdminProtocol = "/shurli/relay-admin/1.0.0"

// Wire format (one request per stream, stateless):
//   Request:  [4-byte BE frame length] [JSON: RemoteAdminRequest]
//   Response: [4-byte BE frame length] [JSON: RemoteAdminResponse]
//
// Max request frame: 64 KB
// Max response frame: 6 MB (accommodates ZKP proving key ~2 MB + JSON overhead)

const (
	maxRemoteAdminRequest  = 64 * 1024
	maxRemoteAdminResponse = 6 * 1024 * 1024
	remoteAdminTimeout     = 30 * time.Second
)

// RemoteAdminRequest is the JSON request frame sent over a P2P stream.
type RemoteAdminRequest struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// RemoteAdminResponse is the JSON response frame sent back over a P2P stream.
type RemoteAdminResponse struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// RemoteAdminHandler handles remote admin requests from authorized admin peers
// over libp2p streams. It is a thin P2P-to-HTTP adapter that reuses all
// existing AdminServer handler functions.
type RemoteAdminHandler struct {
	admin        *AdminServer
	authKeysPath string
	metrics      *p2pnet.Metrics
}

// NewRemoteAdminHandler creates a handler for the remote admin protocol.
func NewRemoteAdminHandler(admin *AdminServer, authKeysPath string) *RemoteAdminHandler {
	return &RemoteAdminHandler{
		admin:        admin,
		authKeysPath: authKeysPath,
	}
}

// HandleStream processes an incoming remote admin stream.
// Flow: verify admin role -> read request frame -> dispatch -> write response frame -> close.
func (h *RemoteAdminHandler) HandleStream(s network.Stream) {
	defer s.Close()
	remotePeer := s.Conn().RemotePeer()
	short := shortPeerID(remotePeer)

	// Only admin peers can use remote admin.
	if !auth.IsAdmin(h.authKeysPath, remotePeer) {
		slog.Warn("remote-admin: rejected non-admin peer", "peer", short)
		h.recordMetric("denied")
		writeRemoteAdminResponse(s, RemoteAdminResponse{
			Status: 403,
			Body:   jsonMsg("permission denied: admin role required"),
		})
		return
	}

	s.SetReadDeadline(time.Now().Add(remoteAdminTimeout))
	s.SetWriteDeadline(time.Now().Add(remoteAdminTimeout))

	// Read request frame: [4 BE len][JSON]
	var lenBuf [4]byte
	if _, err := io.ReadFull(s, lenBuf[:]); err != nil {
		slog.Warn("remote-admin: failed to read frame length", "peer", short, "err", err)
		return
	}
	frameLen := binary.BigEndian.Uint32(lenBuf[:])
	if frameLen == 0 || frameLen > maxRemoteAdminRequest {
		slog.Warn("remote-admin: invalid frame length", "peer", short, "len", frameLen)
		writeRemoteAdminResponse(s, RemoteAdminResponse{
			Status: 400,
			Body:   jsonMsg("invalid frame length"),
		})
		return
	}

	frameBuf := make([]byte, frameLen)
	if _, err := io.ReadFull(s, frameBuf); err != nil {
		slog.Warn("remote-admin: failed to read frame", "peer", short, "err", err)
		return
	}

	var req RemoteAdminRequest
	if err := json.Unmarshal(frameBuf, &req); err != nil {
		slog.Warn("remote-admin: invalid JSON frame", "peer", short, "err", err)
		writeRemoteAdminResponse(s, RemoteAdminResponse{
			Status: 400,
			Body:   jsonMsg("invalid request JSON"),
		})
		return
	}

	if req.Method == "" || req.Path == "" {
		writeRemoteAdminResponse(s, RemoteAdminResponse{
			Status: 400,
			Body:   jsonMsg("method and path required"),
		})
		return
	}

	// Block endpoints that must remain local-only (they transmit seed
	// material, TOTP secrets, or perform vault initialization over the wire).
	if isLocalOnlyPath(req.Path) {
		slog.Warn("remote-admin: blocked local-only endpoint", "peer", short, "path", req.Path)
		h.recordMetric("blocked")
		writeRemoteAdminResponse(s, RemoteAdminResponse{
			Status: 403,
			Body:   jsonMsg("this endpoint is local-only and cannot be accessed remotely"),
		})
		return
	}

	slog.Info("remote-admin: request", "peer", short, "method", req.Method, "path", req.Path)

	// Dispatch to the admin server's internal mux (bypasses cookie auth).
	status, respBody := h.admin.HandleRemoteRequest(req.Method, req.Path, req.Body)

	h.recordMetric("ok")
	writeRemoteAdminResponse(s, RemoteAdminResponse{
		Status: status,
		Body:   respBody,
	})
}

// writeRemoteAdminResponse writes a length-prefixed JSON response frame.
func writeRemoteAdminResponse(s network.Stream, resp RemoteAdminResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	if len(data) > maxRemoteAdminResponse {
		// Truncate oversized response with an error.
		data, _ = json.Marshal(RemoteAdminResponse{
			Status: 500,
			Body:   jsonMsg("response too large"),
		})
	}

	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
	s.Write(lenBuf[:])
	s.Write(data)
}

// jsonMsg creates a JSON-encoded error message for response bodies.
func jsonMsg(msg string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return b
}

// shortPeerID returns a truncated peer ID for logging.
func shortPeerID(p peer.ID) string {
	s := p.String()
	if len(s) > 16 {
		return s[:16] + "..."
	}
	return s
}

// localOnlyPaths are admin endpoints that must never be accessible over P2P.
// They transmit seed material, TOTP provisioning URIs, or perform vault
// initialization - all operations that require physical/local access only.
var localOnlyPaths = []string{
	"/v1/vault/init",
	"/v1/vault/totp-uri",
}

// isLocalOnlyPath checks if the request path matches a local-only endpoint.
func isLocalOnlyPath(path string) bool {
	for _, p := range localOnlyPaths {
		if path == p {
			return true
		}
	}
	return false
}

// SetMetrics attaches metrics to the handler.
func (h *RemoteAdminHandler) SetMetrics(m *p2pnet.Metrics) {
	h.metrics = m
}

// recordMetric increments the remote admin request counter. Nil-safe.
func (h *RemoteAdminHandler) recordMetric(result string) {
	if h.metrics != nil {
		h.metrics.AdminRequestTotal.WithLabelValues("remote:"+result, "0").Inc()
	}
}
