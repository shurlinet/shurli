package sdk

import (
	"crypto/tls"
	"log/slog"
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	quic "github.com/quic-go/quic-go"
)

// PQCStatus summarizes post-quantum cryptography state across all connections.
type PQCStatus struct {
	// QUICPQCVerified is true when at least one QUIC connection negotiated
	// a post-quantum key exchange (X25519MLKEM768 or other PQ curve).
	QUICPQCVerified bool `json:"quic_pqc_verified"`

	// Connections breaks down the key exchange curve for every active connection.
	Connections []PQCConnInfo `json:"connections,omitempty"`
}

// PQCConnInfo describes the TLS key exchange used on a single connection.
type PQCConnInfo struct {
	PeerID    string `json:"peer_id"`
	Transport string `json:"transport"` // "quic", "tcp", "relay"
	CurveID   string `json:"curve_id"`  // e.g. "X25519MLKEM768", "X25519", ""
	CurveCode uint16 `json:"curve_code"`
	PQ        bool   `json:"pq"` // true if post-quantum
}

// isPQCurve returns true if the TLS CurveID represents a post-quantum or
// hybrid post-quantum key exchange. Go 1.24+ defines X25519MLKEM768 (4588),
// SecP256r1MLKEM768 (4587), and SecP384r1MLKEM1024 (4589).
func isPQCurve(id tls.CurveID) bool {
	switch id {
	case tls.X25519MLKEM768:
		return true
	}
	// Future-proof: check the two other NIST hybrid curves added in Go 1.26.
	const (
		secP256r1MLKEM768  tls.CurveID = 4587
		secP384r1MLKEM1024 tls.CurveID = 4589
	)
	return id == secP256r1MLKEM768 || id == secP384r1MLKEM1024
}

// InspectPQC examines all active connections on the host and returns PQC status.
// For QUIC connections, it unwraps the underlying quic.Conn to read the TLS
// ConnectionState. TCP and relay connections report transport only (no TLS
// curve extraction — their Noise handshake is classical X25519).
func InspectPQC(h host.Host) PQCStatus {
	var status PQCStatus
	for _, pid := range h.Network().Peers() {
		for _, conn := range h.Network().ConnsToPeer(pid) {
			info := inspectConn(conn)
			if info.PQ {
				status.QUICPQCVerified = true
			}
			status.Connections = append(status.Connections, info)
		}
	}
	return status
}

func inspectConn(conn network.Conn) PQCConnInfo {
	short := conn.RemotePeer().String()
	if len(short) > 16 {
		short = short[:16] + "..."
	}

	info := PQCConnInfo{PeerID: short}

	// Classify transport.
	if conn.Stat().Limited {
		info.Transport = "relay"
	} else {
		info.Transport = classifyTransportFromAddr(conn.RemoteMultiaddr().String())
	}

	// Only QUIC connections carry TLS state we can inspect.
	if info.Transport != "quic" {
		return info
	}

	var qc *quic.Conn
	if !conn.As(&qc) {
		return info
	}

	cs := qc.ConnectionState()
	info.CurveID = cs.TLS.CurveID.String()
	info.CurveCode = uint16(cs.TLS.CurveID)
	info.PQ = isPQCurve(cs.TLS.CurveID)
	return info
}

// classifyTransportFromAddr returns "quic", "tcp", or "ws" from a multiaddr string.
func classifyTransportFromAddr(addr string) string {
	// QUIC multiaddrs contain /quic-v1
	for _, marker := range []string{"/quic-v1", "/quic/"} {
		if containsSubstring(addr, marker) {
			return "quic"
		}
	}
	if containsSubstring(addr, "/ws") || containsSubstring(addr, "/wss") {
		return "ws"
	}
	if containsSubstring(addr, "/tcp/") {
		return "tcp"
	}
	return "unknown"
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && searchSubstring(s, sub)
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// pqcLogger logs the first PQC-verified QUIC connection once per daemon lifetime.
// Used by connLogger to surface PQC status at startup without spamming.
var pqcLogger = &pqcFirstLog{}

type pqcFirstLog struct {
	once sync.Once
}

// LogIfPQ checks a new connection for PQC and logs once on first verification.
func (p *pqcFirstLog) LogIfPQ(conn network.Conn) {
	info := inspectConn(conn)
	if !info.PQ {
		return
	}
	p.once.Do(func() {
		slog.Info("pqc: post-quantum key exchange verified on QUIC connection",
			"curve", info.CurveID,
			"curve_code", info.CurveCode,
			"peer", info.PeerID)
	})
}
