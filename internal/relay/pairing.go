package relay

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/satindergrewal/peer-up/internal/auth"
)

// Protocol ID for relay pairing.
const PairingProtocol = "/peerup/relay-pair/1.0.0"

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
	AuthKeysPath string
	Gater        GaterInterface
}

// GaterInterface is the subset of AuthorizedPeerGater needed by pairing.
type GaterInterface interface {
	PromotePeer(p peer.ID)
	SetPeerExpiry(p peer.ID, expiresAt time.Time)
}

// HandleStream processes an incoming pairing stream from a client.
// Wire format: [16] token + [1] name length + [N] name bytes.
func (ph *PairingHandler) HandleStream(s network.Stream) {
	defer s.Close()
	remotePeer := s.Conn().RemotePeer()
	short := remotePeer.String()[:16] + "..."

	// Read 16-byte token.
	var token [TokenSize]byte
	if _, err := io.ReadFull(s, token[:]); err != nil {
		slog.Warn("pairing: failed to read token", "peer", short, "err", err)
		writeError(s)
		return
	}

	// Read name.
	var nameLen [1]byte
	if _, err := io.ReadFull(s, nameLen[:]); err != nil {
		slog.Warn("pairing: failed to read name length", "peer", short, "err", err)
		writeError(s)
		return
	}
	if nameLen[0] > maxNameLen {
		slog.Warn("pairing: name too long", "peer", short, "len", nameLen[0])
		writeError(s)
		return
	}
	nameBytes := make([]byte, nameLen[0])
	if nameLen[0] > 0 {
		if _, err := io.ReadFull(s, nameBytes); err != nil {
			slog.Warn("pairing: failed to read name", "peer", short, "err", err)
			writeError(s)
			return
		}
	}
	name := string(nameBytes)

	// Validate and use the token.
	group, idx, err := ph.Store.ValidateAndUse(token[:], remotePeer, name)
	if err != nil {
		slog.Warn("pairing: validation failed", "peer", short)
		writeError(s)
		return
	}

	slog.Info("pairing: peer joined", "peer", short, "name", name, "group", group.ID)

	// Authorize the peer on the relay.
	comment := name
	if comment == "" {
		comment = "paired-" + time.Now().Format("2006-01-02")
	}
	if err := auth.AddPeer(ph.AuthKeysPath, remotePeer.String(), comment); err != nil {
		if !strings.Contains(err.Error(), "already authorized") {
			slog.Error("pairing: failed to authorize peer", "peer", short, "err", err)
			writeError(s)
			return
		}
	}

	// Promote from probation in the gater.
	if ph.Gater != nil {
		ph.Gater.PromotePeer(remotePeer)

		// Set expiry if configured.
		if group.PeerTTL > 0 {
			ph.Gater.SetPeerExpiry(remotePeer, time.Now().Add(group.PeerTTL))
		}
	}

	// Get already-joined peers in this group.
	peers := ph.Store.GetGroupPeers(group.ID, idx)
	complete := ph.Store.IsGroupComplete(group.ID)

	// Write response.
	// STATUS_OK + peer count + peer list.
	var resp []byte
	resp = append(resp, StatusOK)

	if complete || len(group.codes) == 1 {
		// All joined or solo enrollment: send peers and close.
		resp = append(resp, byte(len(peers)))
		for _, p := range peers {
			resp = append(resp, encodePeerInfo(p)...)
		}
		s.Write(resp)
		slog.Info("pairing: group complete", "group", group.ID, "peers", len(peers)+1)
		return
	}

	// Waiting for more peers: write already-joined, then hold stream open.
	// For now, write the joined peers and close. Full stream-hold is Phase 2.
	resp = append(resp, byte(len(peers)))
	for _, p := range peers {
		resp = append(resp, encodePeerInfo(p)...)
	}
	s.Write(resp)
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
		if offset+pidLen >= len(data) {
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

// EncodePairingRequest creates the wire-format request a client sends to the relay.
// Format: [16] token + [1] name length + [N] name bytes.
func EncodePairingRequest(token []byte, name string) []byte {
	if len(name) > maxNameLen {
		name = name[:maxNameLen]
	}
	buf := make([]byte, 0, TokenSize+1+len(name))
	buf = append(buf, token...)
	buf = append(buf, byte(len(name)))
	buf = append(buf, []byte(name)...)
	return buf
}

// ReadPairingResponse reads and parses the relay's pairing response.
// Returns status, peer list, and any error.
func ReadPairingResponse(r io.Reader) (status byte, peers []PeerInfo, err error) {
	var statusByte [1]byte
	if _, err := io.ReadFull(r, statusByte[:]); err != nil {
		return 0, nil, fmt.Errorf("failed to read status: %w", err)
	}
	status = statusByte[0]

	if status == StatusErr {
		// Read error message length + message.
		var msgLen [1]byte
		if _, err := io.ReadFull(r, msgLen[:]); err != nil {
			return status, nil, fmt.Errorf("pairing failed")
		}
		msg := make([]byte, msgLen[0])
		io.ReadFull(r, msg)
		return status, nil, fmt.Errorf("%s", string(msg))
	}

	if status != StatusOK {
		return status, nil, fmt.Errorf("unexpected status: 0x%02x", status)
	}

	// Read peer count.
	var countByte [1]byte
	if _, err := io.ReadFull(r, countByte[:]); err != nil {
		return status, nil, fmt.Errorf("failed to read peer count: %w", err)
	}
	count := int(countByte[0])

	if count == 0 {
		return status, nil, nil
	}

	// Read all remaining bytes for peer data.
	peerData, err := io.ReadAll(r)
	if err != nil {
		return status, nil, fmt.Errorf("failed to read peer data: %w", err)
	}

	peers, err = DecodePeerInfos(peerData, count)
	if err != nil {
		return status, nil, fmt.Errorf("failed to decode peers: %w", err)
	}

	return status, peers, nil
}
