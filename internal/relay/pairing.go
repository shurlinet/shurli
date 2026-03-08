package relay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/deposit"
	"github.com/shurlinet/shurli/internal/invite"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// PairingProtocol is the protocol ID for PAKE-secured relay pairing.
const PairingProtocol = "/shurli/relay-pair/2.0.0"

func init() {
	// Validate all relay protocol constants at startup.
	p2pnet.MustValidateProtocolIDs(
		PairingProtocol,
		PeerNotifyProtocol,
		MOTDProtocol,
		UnsealProtocol,
		RemoteAdminProtocol,
		ZKPAuthProtocol,
	)
}

// Wire status bytes.
const (
	StatusOK       byte = 0x01
	StatusErr      byte = 0x00
	PeerArrived    byte = 0x02
	GroupComplete  byte = 0x03
	StatusTimeout  byte = 0x04
	WaitingMarker  byte = 0xFF
)

// Max name length in wire format.
const maxNameLen = 64

// WaitingStream tracks a peer that is waiting for group completion.
type WaitingStream struct {
	Stream network.Stream
	Idx    int
}

// PairingHandler handles the relay-side pairing protocol.
type PairingHandler struct {
	Store        *TokenStore
	Deposits     *deposit.DepositStore // for macaroon delivery in v2
	AuthKeysPath string
	Gater        GaterInterface
	Metrics      *p2pnet.Metrics // nil-safe: metrics are optional
	authMu       sync.Mutex      // serializes authorized_keys file mutations
}

// GaterInterface is the subset of AuthorizedPeerGater needed by pairing.
type GaterInterface interface {
	PromotePeer(p peer.ID)
	SetPeerExpiry(p peer.ID, expiresAt time.Time)
	SetEnrollmentMode(enabled bool, limit int, timeout time.Duration)
}

// pairingStreamDeadline is the max time allowed for a complete pairing handshake.
const pairingStreamDeadline = 30 * time.Second

// recordPairing increments the pairing counter. Nil-safe.
func (ph *PairingHandler) recordPairing(result string) {
	if ph.Metrics != nil {
		ph.Metrics.PairingTotal.WithLabelValues(result).Inc()
	}
}

// writeError sends a uniform error response.
func writeError(s network.Stream) {
	msg := []byte("pairing failed")
	buf := make([]byte, 0, 2+len(msg))
	buf = append(buf, StatusErr)
	buf = append(buf, byte(len(msg)))
	buf = append(buf, msg...)
	s.Write(buf)
}

// encodePeerInfo encodes a PeerInfo for the wire.
// Format: [1] peer ID length + [N] peer ID bytes + [1] name length + [M] name bytes.
func encodePeerInfo(p PeerInfo) []byte {
	pidBytes := []byte(p.PeerID)
	nameBytes := []byte(p.Name)
	buf := make([]byte, 0, 2+len(pidBytes)+len(nameBytes))
	buf = append(buf, byte(len(pidBytes)))
	buf = append(buf, pidBytes...)
	buf = append(buf, byte(len(nameBytes)))
	buf = append(buf, nameBytes...)
	return buf
}

// DecodePeerInfos reads peer info entries from a pairing response.
func DecodePeerInfos(data []byte, count int) ([]PeerInfo, error) {
	var peers []PeerInfo
	offset := 0
	for i := 0; i < count; i++ {
		if offset >= len(data) {
			return nil, fmt.Errorf("truncated peer info at index %d", i)
		}
		pidLen := int(data[offset])
		offset++
		if offset+pidLen > len(data) {
			return nil, fmt.Errorf("truncated peer ID at index %d", i)
		}
		pid := peer.ID(data[offset : offset+pidLen])
		offset += pidLen

		if offset >= len(data) {
			return nil, fmt.Errorf("truncated name length at index %d", i)
		}
		nameLen := int(data[offset])
		offset++
		if offset+nameLen > len(data) {
			return nil, fmt.Errorf("truncated name at index %d", i)
		}
		name := string(data[offset : offset+nameLen])
		offset += nameLen

		peers = append(peers, PeerInfo{PeerID: pid, Name: name})
	}
	return peers, nil
}

// PairingResponse is the JSON payload encrypted and sent to the joiner via PAKE.
type PairingResponse struct {
	GroupID  string     `json:"group_id"`
	Peers    []PeerInfo `json:"peers"`
	Macaroon string     `json:"macaroon,omitempty"` // serialized macaroon JSON
}

// HandleStream processes a v2 PAKE-secured pairing stream.
//
// Wire format:
//
//	Joiner -> Relay: [32] SHA-256(token) + [32] X25519 pubkey
//	Relay  -> Joiner: [1] status + [32] X25519 pubkey
//	Joiner -> Relay: Encrypt(name)
//	Relay  -> Joiner: Encrypt(PairingResponse JSON)
//
// Returns the joined peer ID and group ID on success (for notification triggers).
func (ph *PairingHandler) HandleStream(s network.Stream) (peer.ID, string) {
	defer s.Close()
	s.SetDeadline(time.Now().Add(pairingStreamDeadline))
	remotePeer := s.Conn().RemotePeer()
	short := remotePeer.String()[:16] + "..."

	// Read [32] token hash + [32] joiner X25519 public key.
	var buf [64]byte
	if _, err := io.ReadFull(s, buf[:]); err != nil {
		slog.Warn("pairing: failed to read handshake", "peer", short, "err", err)
		s.Write([]byte{StatusErr})
		return "", ""
	}
	var tokenHash [32]byte
	copy(tokenHash[:], buf[:32])
	joinerPub := buf[32:64]

	// Look up the token by hash and atomically claim it (InProgress flag).
	group, idx, rawToken, err := ph.Store.ValidateForPAKE(tokenHash)
	if err != nil {
		slog.Warn("pairing: token lookup failed", "peer", short)
		ph.recordPairing("failure")
		s.Write([]byte{StatusErr})
		return "", ""
	}

	// On any failure after ValidateForPAKE, release the in-progress claim.
	pakeClaimedSlot := true
	defer func() {
		if pakeClaimedSlot {
			ph.Store.ClearInProgress(group.ID, idx)
		}
		// Zeroize the raw token copy (S-7: defense-in-depth).
		for i := range rawToken {
			rawToken[i] = 0
		}
	}()

	// Create relay-side PAKE session.
	session, err := invite.NewPAKESession()
	if err != nil {
		slog.Error("pairing: session creation failed", "err", err)
		s.Write([]byte{StatusErr})
		return "", ""
	}

	// Send [1] StatusOK + [32] relay X25519 public key.
	resp := append([]byte{StatusOK}, session.PublicKey()...)
	if _, err := s.Write(resp); err != nil {
		slog.Warn("pairing: failed to send pubkey", "peer", short, "err", err)
		return "", ""
	}

	// Complete PAKE with the raw token as salt.
	// Channel binding: include relay's peer ID in HKDF info to prevent relay swap attacks.
	relayBinding := []byte(s.Conn().LocalPeer())
	if err := session.Complete(joinerPub, rawToken, relayBinding); err != nil {
		slog.Warn("pairing: key exchange failed", "peer", short, "err", err)
		ph.Store.RecordFailedAttemptByHash(tokenHash)
		return "", ""
	}

	// Read encrypted joiner name.
	nameBytes, err := session.Decrypt(s)
	if err != nil {
		// Token mismatch causes AEAD decryption failure.
		slog.Warn("pairing: invalid token (decryption failed)", "peer", short)
		ph.Store.RecordFailedAttemptByHash(tokenHash)
		ph.recordPairing("failure")
		return "", ""
	}
	name := string(nameBytes)
	if len(name) > maxNameLen {
		name = name[:maxNameLen]
	}

	// Mark the token as used (also clears InProgress flag).
	if err := ph.Store.MarkUsed(group.ID, idx, remotePeer, name); err != nil {
		slog.Error("pairing: failed to mark token used", "peer", short, "err", err)
		return "", ""
	}
	pakeClaimedSlot = false // MarkUsed succeeded; don't ClearInProgress in defer

	slog.Info("pairing: peer joined", "peer", short, "name", name, "group", group.ID)

	// Compute HMAC group commitment proof.
	mac := hmac.New(sha256.New, rawToken)
	mac.Write([]byte(group.ID))
	proof := mac.Sum(nil)
	ph.Store.SetHMACProof(group.ID, idx, proof)

	// Authorize the peer on the relay (mutex prevents file races on concurrent pairing).
	ph.authMu.Lock()
	comment := name
	if comment == "" {
		comment = "paired-" + time.Now().Format("2006-01-02")
	}
	if err := auth.AddPeer(ph.AuthKeysPath, remotePeer.String(), comment); err != nil {
		if !strings.Contains(err.Error(), "already authorized") {
			ph.authMu.Unlock()
			slog.Error("pairing: failed to authorize peer", "peer", short, "err", err)
			return "", ""
		}
	}

	// Annotate peer with group ID.
	auth.SetPeerAttr(ph.AuthKeysPath, remotePeer.String(), "group", group.ID)

	// Auto-assign role.
	adminCount, _ := auth.CountAdmins(ph.AuthKeysPath)
	if adminCount == 0 {
		auth.SetPeerRole(ph.AuthKeysPath, remotePeer.String(), auth.RoleAdmin)
		slog.Info("pairing: first peer promoted to admin", "peer", short)
	} else {
		auth.SetPeerRole(ph.AuthKeysPath, remotePeer.String(), auth.RoleMember)
	}
	ph.authMu.Unlock()

	// Promote from probation in the gater.
	if ph.Gater != nil {
		ph.Gater.PromotePeer(remotePeer)
		if group.PeerTTL > 0 {
			ph.Gater.SetPeerExpiry(remotePeer, time.Now().Add(group.PeerTTL))
		}
	}

	// Build response payload.
	// Include peers who used tokens in this group.
	peers := ph.Store.GetGroupPeers(group.ID, idx)

	// In async invites, the creator never uses a token, so they're not in
	// GetGroupPeers. Include the creator so the joiner learns about them.
	if creator := group.CreatedBy; creator != "" && creator != remotePeer {
		creatorName := auth.PeerComment(ph.AuthKeysPath, creator)
		if creatorName == "" {
			creatorName = "inviter"
		}
		peers = append([]PeerInfo{{PeerID: creator, Name: creatorName}}, peers...)
	}

	pairResp := PairingResponse{
		GroupID: group.ID,
		Peers:   peers,
	}

	// Consume linked macaroon deposit if available.
	depositID := ph.Store.GetDepositID(group.ID, idx)
	if depositID != "" && ph.Deposits != nil {
		m, err := ph.Deposits.Consume(depositID, remotePeer.String())
		if err != nil {
			slog.Warn("pairing: deposit consume failed", "deposit", depositID, "err", err)
		} else if m != nil {
			mJSON, _ := json.Marshal(m)
			pairResp.Macaroon = string(mJSON)
		}
	}

	// Send encrypted response.
	respJSON, err := json.Marshal(pairResp)
	if err != nil {
		slog.Error("pairing: failed to marshal response", "err", err)
		return "", ""
	}
	if err := session.WriteEncrypted(s, respJSON); err != nil {
		slog.Warn("pairing: failed to send response", "peer", short, "err", err)
		return "", ""
	}

	ph.recordPairing("success")

	// Auto-disable enrollment if all groups are fully consumed.
	if ph.Gater != nil && ph.Store.AllGroupsUsed() {
		ph.Gater.SetEnrollmentMode(false, 0, 0)
		slog.Info("pairing: all groups complete, enrollment disabled")
	}

	return remotePeer, group.ID
}

