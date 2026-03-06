package relay

import (
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/zkp"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// ZKPAuthProtocol is the libp2p protocol ID for ZKP anonymous authentication.
const ZKPAuthProtocol = "/shurli/zkp-auth/1.0.0"

// Wire format:
//
//   CLIENT -> RELAY:
//     [1 version]        protocol version (0x01)
//     [1 auth_type]      0x01 = membership, 0x02 = role
//     [1 role_required]  0x00 = any, 0x01 = admin, 0x02 = member
//
//   RELAY -> CLIENT:
//     [1 status]         0x01 = challenge, 0x00 = error
//     [8 nonce]          challenge nonce (BE uint64)
//     [32 merkle_root]   current Merkle root
//     [1 tree_depth]     tree depth (for root extension)
//
//   CLIENT -> RELAY:
//     [2 BE proof_len]   proof byte count
//     [N proof]          serialized PLONK proof
//
//   RELAY -> CLIENT:
//     [1 status]         0x01 = authorized, 0x00 = rejected
//     [1 msg_len]        message byte count
//     [N message]        status message

const (
	zkpWireVersion    byte = 0x01
	zkpStatusOK       byte = 0x01
	zkpStatusErr      byte = 0x00
	zkpAuthMembership byte = 0x01
	zkpAuthRole       byte = 0x02
	maxProofLen            = 4096 // proof is ~520 bytes; generous upper bound
)

// zkpPeerRateLimit is the minimum interval between auth attempts per peer.
const zkpPeerRateLimit = 5 * time.Second

// zkpPeerRateLimitPressure is the elevated rate limit when the challenge store
// is under memory pressure (>80% capacity). Slows fill rate for graceful degradation.
const zkpPeerRateLimitPressure = 30 * time.Second

// maxPeerSeenEntries is the hard cap on peerSeen map size.
// When exceeded, the oldest half is evicted. Prevents unbounded growth
// from many unique peers attempting ZKP auth over time.
const maxPeerSeenEntries = 10000

// ZKPAuthHandler handles ZKP anonymous authentication streams on the relay.
type ZKPAuthHandler struct {
	Metrics *p2pnet.Metrics // nil-safe

	mu           sync.RWMutex
	tree         *zkp.MerkleTree
	verifier     *zkp.Verifier
	challenges   *zkp.ChallengeStore
	authKeysPath string
	keysDir      string          // directory containing provingKey.bin and verifyingKey.bin
	scores       map[peer.ID]int // reputation scores committed in tree leaves; nil = all zeros

	rateMu   sync.Mutex
	peerSeen map[string]time.Time // peer ID string -> last auth attempt
}

// NewZKPAuthHandler creates a handler. On first run, it auto-bootstraps
// the PLONK circuit keys (compile + SRS + setup, ~2-5s one-time cost).
// The tree must be initialized via RebuildTree before the handler can
// serve requests.
func NewZKPAuthHandler(authKeysPath string, keysDir string) (*ZKPAuthHandler, error) {
	// Auto-bootstrap: compile circuit and generate keys if not cached.
	if err := zkp.SetupKeys(keysDir); err != nil {
		return nil, fmt.Errorf("bootstrapping zkp keys: %w", err)
	}

	v, err := zkp.NewVerifier(keysDir)
	if err != nil {
		return nil, fmt.Errorf("creating zkp verifier: %w", err)
	}

	h := &ZKPAuthHandler{
		verifier:     v,
		challenges:   zkp.NewChallengeStore(zkp.DefaultChallengeTTL),
		authKeysPath: authKeysPath,
		keysDir:      keysDir,
		peerSeen:     make(map[string]time.Time),
	}

	return h, nil
}

// KeysDir returns the directory containing the PLONK circuit key files.
func (h *ZKPAuthHandler) KeysDir() string {
	return h.keysDir
}

// SetScores updates the reputation scores committed in tree leaves.
// Call RebuildTree after updating scores to commit the new values.
// Passing nil clears all scores (all peers default to 0).
func (h *ZKPAuthHandler) SetScores(scores map[peer.ID]int) {
	h.mu.Lock()
	h.scores = scores
	h.mu.Unlock()
}

// RebuildTree rebuilds the Merkle tree from authorized_keys with
// committed reputation scores. Safe to call while serving requests.
func (h *ZKPAuthHandler) RebuildTree() error {
	start := time.Now()

	h.mu.RLock()
	scores := h.scores
	h.mu.RUnlock()

	tree, err := zkp.BuildMerkleTreeWithScores(h.authKeysPath, scores)
	if err != nil {
		h.recordTreeRebuild("error", time.Since(start))
		return fmt.Errorf("building merkle tree: %w", err)
	}

	h.mu.Lock()
	h.tree = tree
	h.mu.Unlock()

	dur := time.Since(start)
	h.recordTreeRebuild("success", dur)
	if h.Metrics != nil {
		h.Metrics.ZKPTreeLeaves.Set(float64(tree.LeafCount()))
	}
	slog.Info("zkp: merkle tree rebuilt", "leaves", tree.LeafCount(), "depth", tree.Depth, "duration", dur.Round(time.Microsecond))
	return nil
}

// TreeInfo returns the current tree state. Returns nil fields if no tree is built.
func (h *ZKPAuthHandler) TreeInfo() (root []byte, leafCount int, depth int, ok bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.tree == nil {
		return nil, 0, 0, false
	}
	return h.tree.Root, h.tree.LeafCount(), h.tree.Depth, true
}

// Challenges returns the underlying ChallengeStore for lifecycle management
// (e.g., starting the cleanup goroutine).
func (h *ZKPAuthHandler) Challenges() *zkp.ChallengeStore {
	return h.challenges
}

// HandleStream processes an incoming ZKP auth stream.
func (h *ZKPAuthHandler) HandleStream(s network.Stream) {
	defer s.Close()

	// Per-peer rate limiting: reject rapid-fire auth attempts.
	// Under memory pressure, the rate limit increases from 5s to 30s.
	peerStr := s.Conn().RemotePeer().String()
	rateLimit := zkpPeerRateLimit
	if h.challenges.UnderPressure() {
		rateLimit = zkpPeerRateLimitPressure
	}
	h.rateMu.Lock()
	if last, ok := h.peerSeen[peerStr]; ok && time.Since(last) < rateLimit {
		h.rateMu.Unlock()
		h.recordAuth("rate_limited")
		writeZKPResponse(s, zkpStatusErr, "rate limited")
		return
	}
	h.peerSeen[peerStr] = time.Now()
	// Hard cap: evict oldest entries if map grows too large.
	if len(h.peerSeen) > maxPeerSeenEntries {
		h.evictOldestPeerSeen()
	}
	h.rateMu.Unlock()

	s.SetReadDeadline(time.Now().Add(30 * time.Second))

	// Step 1: Read client request header (3 bytes).
	var header [3]byte
	if _, err := io.ReadFull(s, header[:]); err != nil {
		slog.Debug("zkp-auth: failed to read header", "err", err)
		writeZKPResponse(s, zkpStatusErr, "protocol error")
		return
	}

	wireVersion := header[0]
	authType := header[1]
	roleRequired := header[2]

	if wireVersion != zkpWireVersion {
		slog.Debug("zkp-auth: unsupported version", "version", wireVersion)
		h.recordAuth("unsupported_version")
		writeZKPResponse(s, zkpStatusErr, "unsupported protocol version")
		return
	}

	if authType != zkpAuthMembership && authType != zkpAuthRole {
		slog.Debug("zkp-auth: invalid auth type", "type", authType)
		h.recordAuth("invalid_auth_type")
		writeZKPResponse(s, zkpStatusErr, "invalid auth type")
		return
	}

	// Validate role_required field.
	if roleRequired > 2 {
		slog.Debug("zkp-auth: invalid role", "role", roleRequired)
		h.recordAuth("invalid_role")
		writeZKPResponse(s, zkpStatusErr, "invalid role")
		return
	}

	// Step 2: Check tree readiness and issue challenge.
	h.mu.RLock()
	tree := h.tree
	h.mu.RUnlock()

	if tree == nil {
		slog.Warn("zkp-auth: tree not built")
		h.recordAuth("tree_not_ready")
		writeZKPResponse(s, zkpStatusErr, "tree not initialized")
		return
	}

	challenge, err := h.challenges.Issue(tree.Root)
	if err != nil {
		slog.Error("zkp-auth: failed to issue challenge", "err", err)
		h.recordAuth("challenge_error")
		writeZKPResponse(s, zkpStatusErr, "internal error")
		return
	}
	if h.Metrics != nil {
		h.Metrics.ZKPChallengesPending.Set(float64(h.challenges.Pending()))
	}

	// Step 3: Send challenge to client.
	// [1 status] [8 nonce BE] [32 root] [1 depth]
	challengeMsg := make([]byte, 1+8+32+1)
	challengeMsg[0] = zkpStatusOK
	binary.BigEndian.PutUint64(challengeMsg[1:9], challenge.Nonce)
	copy(challengeMsg[9:41], tree.Root)
	challengeMsg[41] = byte(tree.Depth)
	if _, err := s.Write(challengeMsg); err != nil {
		slog.Debug("zkp-auth: failed to send challenge", "err", err)
		return
	}

	// Step 4: Read proof from client.
	// Reset deadline for proof generation time (proving takes ~1.8s).
	s.SetReadDeadline(time.Now().Add(10 * time.Second))

	var proofLenBuf [2]byte
	if _, err := io.ReadFull(s, proofLenBuf[:]); err != nil {
		slog.Debug("zkp-auth: failed to read proof length", "err", err)
		h.recordAuth("proof_read_error")
		writeZKPResponse(s, zkpStatusErr, "protocol error")
		return
	}
	proofLen := binary.BigEndian.Uint16(proofLenBuf[:])
	if proofLen == 0 || proofLen > maxProofLen {
		slog.Debug("zkp-auth: invalid proof length", "len", proofLen)
		h.recordAuth("invalid_proof_length")
		writeZKPResponse(s, zkpStatusErr, "invalid proof length")
		return
	}

	proofBytes := make([]byte, proofLen)
	if _, err := io.ReadFull(s, proofBytes); err != nil {
		slog.Debug("zkp-auth: failed to read proof", "err", err)
		h.recordAuth("proof_read_error")
		writeZKPResponse(s, zkpStatusErr, "protocol error")
		return
	}

	// Step 5: Consume nonce (single-use, checks expiry).
	if _, err := h.challenges.Consume(challenge.Nonce, tree.Root); err != nil {
		slog.Debug("zkp-auth: challenge consume failed", "err", err)
		h.recordAuth("challenge_invalid")
		writeZKPResponse(s, zkpStatusErr, err.Error())
		return
	}
	if h.Metrics != nil {
		h.Metrics.ZKPChallengesPending.Set(float64(h.challenges.Pending()))
	}

	// Step 6: Verify proof.
	verifyStart := time.Now()
	verifyErr := h.verifier.Verify(proofBytes, tree.Root, challenge.Nonce, uint64(roleRequired), tree.Depth)
	verifyDur := time.Since(verifyStart)

	if verifyErr != nil {
		slog.Info("zkp-auth: proof rejected", "err", verifyErr, "verify_ms", verifyDur.Milliseconds())
		h.recordVerify("invalid", verifyDur)
		h.recordAuth("proof_invalid")
		writeZKPResponse(s, zkpStatusErr, "proof verification failed")
		return
	}

	h.recordVerify("success", verifyDur)
	h.recordAuth("success")
	slog.Info("zkp-auth: peer authorized anonymously", "verify_ms", verifyDur.Milliseconds())
	writeZKPResponse(s, zkpStatusOK, "authorized")
}

// writeZKPResponse writes the final status response.
// Format: [1 status] [1 msg_len] [N message]
func writeZKPResponse(s network.Stream, status byte, msg string) {
	if len(msg) > 255 {
		msg = msg[:255]
	}
	buf := make([]byte, 2+len(msg))
	buf[0] = status
	buf[1] = byte(len(msg))
	copy(buf[2:], msg)
	s.Write(buf)
}

// evictOldestPeerSeen removes the oldest half of peerSeen entries.
// Called under rateMu lock when the map exceeds maxPeerSeenEntries.
func (h *ZKPAuthHandler) evictOldestPeerSeen() {
	cutoff := time.Now().Add(-5 * zkpPeerRateLimit)
	for k, t := range h.peerSeen {
		if t.Before(cutoff) {
			delete(h.peerSeen, k)
		}
	}
	// If still over cap after time-based eviction, force-evict half.
	if len(h.peerSeen) > maxPeerSeenEntries {
		i := 0
		half := len(h.peerSeen) / 2
		for k := range h.peerSeen {
			if i >= half {
				break
			}
			delete(h.peerSeen, k)
			i++
		}
	}
}

// CleanPeerSeen evicts stale entries from the per-peer rate limit map.
// Entries older than 10x the rate limit window are removed.
// Safe to call from a periodic goroutine.
func (h *ZKPAuthHandler) CleanPeerSeen() {
	cutoff := time.Now().Add(-10 * zkpPeerRateLimit)
	h.rateMu.Lock()
	for k, t := range h.peerSeen {
		if t.Before(cutoff) {
			delete(h.peerSeen, k)
		}
	}
	h.rateMu.Unlock()
}

// --- metrics helpers (nil-safe) ---

func (h *ZKPAuthHandler) recordAuth(result string) {
	if h.Metrics != nil {
		h.Metrics.ZKPAuthTotal.WithLabelValues(result).Inc()
	}
}

func (h *ZKPAuthHandler) recordVerify(result string, dur time.Duration) {
	if h.Metrics != nil {
		h.Metrics.ZKPVerifyTotal.WithLabelValues(result).Inc()
		h.Metrics.ZKPVerifyDurationSeconds.WithLabelValues(result).Observe(dur.Seconds())
	}
}

func (h *ZKPAuthHandler) recordTreeRebuild(result string, dur time.Duration) {
	if h.Metrics != nil {
		h.Metrics.ZKPTreeRebuildTotal.WithLabelValues(result).Inc()
		h.Metrics.ZKPTreeRebuildDurationSeconds.WithLabelValues(result).Observe(dur.Seconds())
	}
}

// --- Wire encoding helpers for client side ---

// EncodeZKPAuthRequest builds the 3-byte client request header.
func EncodeZKPAuthRequest(authType byte, roleRequired byte) []byte {
	return []byte{zkpWireVersion, authType, roleRequired}
}

// ReadZKPChallenge reads the relay's challenge response.
// Returns: nonce, merkleRoot, treeDepth, error.
func ReadZKPChallenge(r io.Reader) (uint64, []byte, int, error) {
	var header [1]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, 0, err
	}
	if header[0] != zkpStatusOK {
		// Error response: status already consumed, read [1 msg_len] [N message].
		var msgLenBuf [1]byte
		if _, err := io.ReadFull(r, msgLenBuf[:]); err != nil {
			return 0, nil, 0, fmt.Errorf("challenge rejected")
		}
		if msgLenBuf[0] > 0 {
			msgBuf := make([]byte, msgLenBuf[0])
			if _, err := io.ReadFull(r, msgBuf); err != nil {
				return 0, nil, 0, fmt.Errorf("challenge rejected")
			}
			return 0, nil, 0, fmt.Errorf("challenge rejected: %s", string(msgBuf))
		}
		return 0, nil, 0, fmt.Errorf("challenge rejected")
	}

	// Read [8 nonce] [32 root] [1 depth]
	var payload [41]byte
	if _, err := io.ReadFull(r, payload[:]); err != nil {
		return 0, nil, 0, fmt.Errorf("reading challenge payload: %w", err)
	}

	nonce := binary.BigEndian.Uint64(payload[0:8])
	root := make([]byte, 32)
	copy(root, payload[8:40])
	depth := int(payload[40])

	return nonce, root, depth, nil
}

// EncodeZKPProof builds the length-prefixed proof message.
func EncodeZKPProof(proofBytes []byte) []byte {
	buf := make([]byte, 2+len(proofBytes))
	binary.BigEndian.PutUint16(buf[0:2], uint16(len(proofBytes)))
	copy(buf[2:], proofBytes)
	return buf
}

// ReadZKPAuthResponse reads the final auth status.
// Returns: authorized (bool), message, error.
func ReadZKPAuthResponse(r io.Reader) (bool, string, error) {
	ok, msg, err := readZKPMessage(r)
	if err != nil {
		return false, "", err
	}
	return ok, msg, nil
}

// readZKPMessage reads [1 status] [1 msg_len] [N message].
func readZKPMessage(r io.Reader) (bool, string, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return false, "", err
	}
	status := header[0]
	msgLen := header[1]

	if msgLen > 0 {
		msgBuf := make([]byte, msgLen)
		if _, err := io.ReadFull(r, msgBuf); err != nil {
			return false, "", err
		}
		return status == zkpStatusOK, string(msgBuf), nil
	}

	return status == zkpStatusOK, "", nil
}
