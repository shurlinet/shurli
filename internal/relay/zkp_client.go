package relay

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/shurlinet/shurli/internal/zkp"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// ZKPAuthClient handles client-side ZKP anonymous authentication with a relay.
type ZKPAuthClient struct {
	Host    host.Host
	Prover  *zkp.Prover
	Tree    *zkp.MerkleTree
	Metrics *p2pnet.Metrics // nil-safe
}

// Authenticate opens a stream to the relay and proves membership anonymously.
// The caller's peer ID is never revealed as part of the ZKP protocol.
//
// Parameters:
//   - relayPeer: the relay's peer ID to connect to
//   - roleRequired: 0 = any, 1 = admin, 2 = member
//
// Returns nil on successful authentication.
func (c *ZKPAuthClient) Authenticate(ctx context.Context, relayPeer peer.ID, roleRequired uint64) error {
	// Open stream to relay.
	s, err := c.Host.NewStream(ctx, relayPeer, protocol.ID(ZKPAuthProtocol))
	if err != nil {
		return fmt.Errorf("opening zkp-auth stream: %w", err)
	}
	defer s.Close()

	return c.authenticateOnStream(s, roleRequired)
}

// authenticateOnStream runs the ZKP auth protocol on an already-open stream.
// Separated for testing (where we use in-memory streams instead of real connections).
func (c *ZKPAuthClient) authenticateOnStream(s network.Stream, roleRequired uint64) error {
	s.SetReadDeadline(time.Now().Add(30 * time.Second))

	// Step 1: Send request header.
	authType := zkpAuthMembership
	if roleRequired > 0 {
		authType = zkpAuthRole
	}
	if _, err := s.Write(EncodeZKPAuthRequest(authType, byte(roleRequired))); err != nil {
		return fmt.Errorf("sending auth request: %w", err)
	}

	// Step 2: Read challenge from relay.
	nonce, merkleRoot, treeDepth, err := ReadZKPChallenge(s)
	if err != nil {
		return fmt.Errorf("reading challenge: %w", err)
	}

	// Step 3: Generate proof.
	// Verify our local tree matches the relay's Merkle root before proving.
	// If roots differ, the proof will fail verification anyway, but catching
	// it here gives a clear error and avoids wasting ~1.8s on a doomed proof.
	localRoot := c.Tree.Root
	if c.Tree.Depth < zkp.MaxTreeDepth {
		var extErr error
		localRoot, extErr = zkp.ExtendRoot(c.Tree.Root, c.Tree.Depth)
		if extErr != nil {
			return fmt.Errorf("extending local root: %w", extErr)
		}
	}
	if !bytes.Equal(localRoot, merkleRoot) {
		return fmt.Errorf("local tree root does not match relay (tree depth %d): tree may be stale, rebuild required", treeDepth)
	}

	proveStart := time.Now()
	proofBytes, err := c.Prover.Prove(c.Tree, c.Host.ID(), nonce, roleRequired)
	proveDur := time.Since(proveStart)

	if err != nil {
		c.recordProve("error", proveDur)
		return fmt.Errorf("generating proof: %w", err)
	}
	c.recordProve("success", proveDur)

	// Step 4: Send proof to relay.
	// Reset write deadline for the proof transmission.
	s.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := s.Write(EncodeZKPProof(proofBytes)); err != nil {
		return fmt.Errorf("sending proof: %w", err)
	}

	// Step 5: Read auth result.
	s.SetReadDeadline(time.Now().Add(10 * time.Second))
	authorized, msg, err := ReadZKPAuthResponse(s)
	if err != nil {
		return fmt.Errorf("reading auth response: %w", err)
	}

	if !authorized {
		return fmt.Errorf("zkp auth denied: %s", msg)
	}

	return nil
}

// --- metrics helpers (nil-safe) ---

func (c *ZKPAuthClient) recordProve(result string, dur time.Duration) {
	if c.Metrics != nil {
		c.Metrics.ZKPProveTotal.WithLabelValues(result).Inc()
		c.Metrics.ZKPProveDurationSeconds.WithLabelValues(result).Observe(dur.Seconds())
	}
}
