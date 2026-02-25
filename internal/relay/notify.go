package relay

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/shurlinet/shurli/internal/auth"
)

// Protocol ID for peer introduction delivery.
const PeerNotifyProtocol = "/shurli/peer-notify/1.0.0"

// Wire format version.
const notifyVersion byte = 0x01

// PeerNotifier delivers peer introductions to connected peers.
// The relay acts as a post office: it knows who joined together and
// delivers introductions when peers connect. Designed generically so
// future introduction sources (peer-to-peer, multi-relay mesh) can
// reuse the same wire format and handler.
type PeerNotifier struct {
	Host         host.Host
	AuthKeysPath string
	Store        *TokenStore // for HMAC proofs
}

// NotifyPeer delivers peer introductions to a single target peer.
// It reads authorized_keys to find all peers in the same group,
// excludes the target, and sends the list over a new stream.
func (pn *PeerNotifier) NotifyPeer(ctx context.Context, targetPeerID peer.ID, groupID string) error {
	// Read authorized_keys to find group members.
	entries, err := auth.ListPeers(pn.AuthKeysPath)
	if err != nil {
		return fmt.Errorf("failed to read authorized_keys: %w", err)
	}

	// Get HMAC proofs from token store (if group is still in memory).
	proofMap := make(map[string][]byte)
	if pn.Store != nil {
		storePeers := pn.Store.GetGroupPeers(groupID, -1) // -1 = exclude none
		for _, sp := range storePeers {
			if len(sp.HMACProof) > 0 {
				proofMap[sp.PeerID.String()] = sp.HMACProof
			}
		}
	}

	// Collect group members (excluding the target).
	var peers []NotifyPeerInfo
	groupSize := 0
	for _, e := range entries {
		if e.Group != groupID {
			continue
		}
		groupSize++
		if e.PeerID == targetPeerID {
			continue
		}
		peers = append(peers, NotifyPeerInfo{
			PeerID:    e.PeerID.String(),
			Name:      e.Comment,
			HMACProof: proofMap[e.PeerID.String()],
		})
	}

	if len(peers) == 0 {
		return nil // nothing to deliver
	}

	// Open stream to target.
	streamCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	streamCtx = network.WithAllowLimitedConn(streamCtx, PeerNotifyProtocol)
	s, err := pn.Host.NewStream(streamCtx, targetPeerID, protocol.ID(PeerNotifyProtocol))
	if err != nil {
		return fmt.Errorf("failed to open stream to %s: %w", targetPeerID.String()[:16], err)
	}
	defer s.Close()

	// Write notification.
	data := EncodePeerNotify(groupID, byte(groupSize), peers)
	if _, err := s.Write(data); err != nil {
		return fmt.Errorf("failed to write notification: %w", err)
	}

	slog.Info("peer-notify: delivered introductions",
		"target", targetPeerID.String()[:16]+"...",
		"group", groupID,
		"count", len(peers))

	return nil
}

// NotifyGroupMembers delivers peer introductions to all connected group
// members except excludePeerID (typically the peer that just joined).
func (pn *PeerNotifier) NotifyGroupMembers(ctx context.Context, groupID string, excludePeerID peer.ID) {
	entries, err := auth.ListPeers(pn.AuthKeysPath)
	if err != nil {
		slog.Error("peer-notify: failed to read authorized_keys", "err", err)
		return
	}

	for _, e := range entries {
		if e.Group != groupID || e.PeerID == excludePeerID {
			continue
		}

		// Only notify connected peers.
		if pn.Host.Network().Connectedness(e.PeerID) != network.Connected {
			continue
		}

		if err := pn.NotifyPeer(ctx, e.PeerID, groupID); err != nil {
			slog.Warn("peer-notify: delivery failed",
				"target", e.PeerID.String()[:16]+"...",
				"group", groupID,
				"err", err)
		}
	}
}

// HMACProofSize is the length of the HMAC-SHA256 commitment proof.
const HMACProofSize = 32

// NotifyPeerInfo is the per-peer data in a notification message.
type NotifyPeerInfo struct {
	PeerID    string
	Name      string
	HMACProof []byte // 32-byte HMAC-SHA256(token, groupID) - proves token possession
}

// EncodePeerNotify creates the wire-format notification message.
//
// Wire format:
//
//	[1]       version (0x01)
//	[1]       group ID length
//	[N]       group ID (ASCII)
//	[1]       group size (total members including recipient)
//	[1]       peer count
//	Per peer:
//	  [2 BE]  peer ID length
//	  [N]     peer ID string
//	  [1]     name length
//	  [M]     name string
//	  [32]    HMAC proof (HMAC-SHA256, zero-padded if absent)
func EncodePeerNotify(groupID string, groupSize byte, peers []NotifyPeerInfo) []byte {
	gidBytes := []byte(groupID)
	buf := make([]byte, 0, 4+len(gidBytes)+len(peers)*96)

	buf = append(buf, notifyVersion)
	buf = append(buf, byte(len(gidBytes)))
	buf = append(buf, gidBytes...)
	buf = append(buf, groupSize)
	buf = append(buf, byte(len(peers)))

	for _, p := range peers {
		pidBytes := []byte(p.PeerID)
		var pidLen [2]byte
		binary.BigEndian.PutUint16(pidLen[:], uint16(len(pidBytes)))
		buf = append(buf, pidLen[:]...)
		buf = append(buf, pidBytes...)

		nameBytes := []byte(p.Name)
		buf = append(buf, byte(len(nameBytes)))
		buf = append(buf, nameBytes...)

		// HMAC proof (exactly 32 bytes, zero-padded if absent).
		var proof [HMACProofSize]byte
		copy(proof[:], p.HMACProof)
		buf = append(buf, proof[:]...)
	}

	return buf
}

// ReadPeerNotify reads and parses a peer notification message from a stream.
// Returns the peer list, group ID, and group size.
func ReadPeerNotify(r io.Reader) (peers []NotifyPeerInfo, groupID string, groupSize int, err error) {
	// Version byte.
	var ver [1]byte
	if _, err := io.ReadFull(r, ver[:]); err != nil {
		return nil, "", 0, fmt.Errorf("failed to read version: %w", err)
	}
	if ver[0] != notifyVersion {
		return nil, "", 0, fmt.Errorf("unsupported version: 0x%02x", ver[0])
	}

	// Group ID (length-prefixed).
	var gidLen [1]byte
	if _, err := io.ReadFull(r, gidLen[:]); err != nil {
		return nil, "", 0, fmt.Errorf("failed to read group ID length: %w", err)
	}
	gidBytes := make([]byte, gidLen[0])
	if _, err := io.ReadFull(r, gidBytes); err != nil {
		return nil, "", 0, fmt.Errorf("failed to read group ID: %w", err)
	}
	groupID = string(gidBytes)

	// Group size.
	var gsizeByte [1]byte
	if _, err := io.ReadFull(r, gsizeByte[:]); err != nil {
		return nil, "", 0, fmt.Errorf("failed to read group size: %w", err)
	}
	groupSize = int(gsizeByte[0])

	// Peer count.
	var countByte [1]byte
	if _, err := io.ReadFull(r, countByte[:]); err != nil {
		return nil, "", 0, fmt.Errorf("failed to read peer count: %w", err)
	}
	count := int(countByte[0])

	for i := 0; i < count; i++ {
		// Peer ID (2-byte BE length prefix).
		var pidLenBuf [2]byte
		if _, err := io.ReadFull(r, pidLenBuf[:]); err != nil {
			return nil, groupID, groupSize, fmt.Errorf("truncated peer ID length at %d: %w", i, err)
		}
		pidLen := int(binary.BigEndian.Uint16(pidLenBuf[:]))
		pidBytes := make([]byte, pidLen)
		if _, err := io.ReadFull(r, pidBytes); err != nil {
			return nil, groupID, groupSize, fmt.Errorf("truncated peer ID at %d: %w", i, err)
		}

		// Name (1-byte length prefix).
		var nameLen [1]byte
		if _, err := io.ReadFull(r, nameLen[:]); err != nil {
			return nil, groupID, groupSize, fmt.Errorf("truncated name length at %d: %w", i, err)
		}
		nameBytes := make([]byte, nameLen[0])
		if nameLen[0] > 0 {
			if _, err := io.ReadFull(r, nameBytes); err != nil {
				return nil, groupID, groupSize, fmt.Errorf("truncated name at %d: %w", i, err)
			}
		}

		// HMAC proof (32 bytes).
		var proof [HMACProofSize]byte
		if _, err := io.ReadFull(r, proof[:]); err != nil {
			return nil, groupID, groupSize, fmt.Errorf("truncated HMAC proof at %d: %w", i, err)
		}

		peers = append(peers, NotifyPeerInfo{
			PeerID:    string(pidBytes),
			Name:      string(nameBytes),
			HMACProof: proof[:],
		})
	}

	return peers, groupID, groupSize, nil
}

// RunReconnectNotifier subscribes to peer identification events and pushes
// introductions when an authorized peer with a group attribute reconnects.
// Uses EvtPeerIdentificationCompleted (not EvtPeerConnectednessChanged) so
// the peer's supported protocols are known before opening a stream.
func RunReconnectNotifier(ctx context.Context, h host.Host, notifier *PeerNotifier, authKeysPath string) {
	sub, err := h.EventBus().Subscribe(new(event.EvtPeerIdentificationCompleted))
	if err != nil {
		slog.Error("reconnect-notifier: subscribe failed", "err", err)
		return
	}
	defer sub.Close()

	// Dedup: skip re-notifying the same peer within a short window.
	// Prevents burst logging when peers reconnect rapidly.
	recentlyNotified := make(map[peer.ID]time.Time)
	const dedupeWindow = 30 * time.Second

	// Periodic cleanup prevents unbounded map growth on long-running relays.
	cleanupTicker := time.NewTicker(5 * time.Minute)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-cleanupTicker.C:
			for pid, ts := range recentlyNotified {
				if time.Since(ts) > dedupeWindow {
					delete(recentlyNotified, pid)
				}
			}
		case evt, ok := <-sub.Out():
			if !ok {
				return
			}
			e := evt.(event.EvtPeerIdentificationCompleted)
			slog.Debug("reconnect-notifier: peer identified",
				"peer", e.Peer.String()[:16]+"...")

			// Skip if recently notified.
			if last, ok := recentlyNotified[e.Peer]; ok && time.Since(last) < dedupeWindow {
				continue
			}

			// Look up identified peer in authorized_keys.
			entries, err := auth.ListPeers(authKeysPath)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if entry.PeerID == e.Peer && entry.Group != "" {
					recentlyNotified[e.Peer] = time.Now()
					go func(pid peer.ID, gid string) {
						if err := notifier.NotifyPeer(ctx, pid, gid); err != nil {
							slog.Warn("reconnect-notifier: delivery failed",
								"peer", pid.String()[:16]+"...",
								"group", gid, "err", err)
						}
					}(e.Peer, entry.Group)
					break
				}
			}
		}
	}
}
