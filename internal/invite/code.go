package invite

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// InviteData holds the payload encoded in an invite code.
type InviteData struct {
	Version   byte     // protocol version (VersionV1 = PAKE invite, VersionV2 = relay pairing)
	Token     [8]byte  // v1 PAKE token (8 bytes)
	TokenV2   []byte   // v2 pairing token (16 bytes)
	RelayAddr string   // full relay multiaddr (e.g., /ip4/.../tcp/.../p2p/...)
	PeerID    peer.ID  // inviter's peer ID (v1 only; empty for v2)
	Network   string   // DHT namespace (empty = global network)
}

// GenerateToken creates a cryptographically random 8-byte token.
func GenerateToken() ([8]byte, error) {
	var token [8]byte
	_, err := rand.Read(token[:])
	return token, err
}

// TokenHex returns the hex string of a token.
func TokenHex(t [8]byte) string {
	return fmt.Sprintf("%x", t[:])
}

// Encode serializes invite data into a compact, dash-separated base32 code.
//
// v1 binary format (PAKE invite):
//
//	[1] version (0x01)
//	[8] token
//	[4] relay IPv4
//	[2] relay TCP port (big-endian)
//	[1] relay peer ID length
//	[N] relay peer ID (raw multihash bytes)
//	[1] namespace length (0 = global network)
//	[L] namespace bytes (if length > 0)
//	[M] inviter peer ID (raw multihash bytes, remaining bytes)
func Encode(data *InviteData) (string, error) {
	maddr, err := ma.NewMultiaddr(data.RelayAddr)
	if err != nil {
		return "", fmt.Errorf("invalid relay address: %w", err)
	}

	ipStr, err := maddr.ValueForProtocol(ma.P_IP4)
	if err != nil {
		return "", fmt.Errorf("relay address must contain IPv4: %w", err)
	}
	ip := net.ParseIP(ipStr).To4()
	if ip == nil {
		return "", fmt.Errorf("invalid IPv4 address: %s", ipStr)
	}

	portStr, err := maddr.ValueForProtocol(ma.P_TCP)
	if err != nil {
		return "", fmt.Errorf("relay address must contain TCP port: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return "", fmt.Errorf("invalid port: %s", portStr)
	}

	p2pStr, err := maddr.ValueForProtocol(ma.P_P2P)
	if err != nil {
		return "", fmt.Errorf("relay address must include /p2p/ peer ID: %w", err)
	}
	relayPeerID, err := peer.Decode(p2pStr)
	if err != nil {
		return "", fmt.Errorf("invalid relay peer ID: %w", err)
	}

	relayIDBytes := []byte(relayPeerID)
	inviterIDBytes := []byte(data.PeerID)

	// Validate namespace length
	if len(data.Network) > 63 {
		return "", fmt.Errorf("network namespace too long: %d bytes (max 63)", len(data.Network))
	}

	nsBytes := []byte(data.Network)
	buf := make([]byte, 0, 1+8+4+2+1+len(relayIDBytes)+1+len(nsBytes)+len(inviterIDBytes))
	buf = append(buf, VersionV1)
	buf = append(buf, data.Token[:]...)
	buf = append(buf, ip...)
	buf = append(buf, byte(port>>8), byte(port))
	buf = append(buf, byte(len(relayIDBytes)))
	buf = append(buf, relayIDBytes...)
	buf = append(buf, byte(len(nsBytes)))
	buf = append(buf, nsBytes...)
	buf = append(buf, inviterIDBytes...)

	encoded := encoding.EncodeToString(buf)

	// Group with dashes every 4 characters for readability
	var groups []string
	for i := 0; i < len(encoded); i += 4 {
		end := i + 4
		if end > len(encoded) {
			end = len(encoded)
		}
		groups = append(groups, encoded[i:end])
	}
	return strings.Join(groups, "-"), nil
}

// EncodeV2 serializes a v2 relay pairing invite code.
//
// v2 binary format (relay pairing, no inviter peer ID):
//
//	[1]  version (0x02)
//	[16] token (128-bit random)
//	[4]  relay IPv4
//	[2]  relay TCP port (big-endian)
//	[1]  relay peer ID length
//	[N]  relay peer ID (raw multihash bytes)
//	[1]  namespace length (0 = global network)
//	[L]  namespace bytes (if length > 0)
func EncodeV2(token []byte, relayAddr string, network string) (string, error) {
	if len(token) != 16 {
		return "", fmt.Errorf("v2 token must be 16 bytes, got %d", len(token))
	}

	maddr, err := ma.NewMultiaddr(relayAddr)
	if err != nil {
		return "", fmt.Errorf("invalid relay address: %w", err)
	}

	ipStr, err := maddr.ValueForProtocol(ma.P_IP4)
	if err != nil {
		return "", fmt.Errorf("relay address must contain IPv4: %w", err)
	}
	ip := net.ParseIP(ipStr).To4()
	if ip == nil {
		return "", fmt.Errorf("invalid IPv4 address: %s", ipStr)
	}

	portStr, err := maddr.ValueForProtocol(ma.P_TCP)
	if err != nil {
		return "", fmt.Errorf("relay address must contain TCP port: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return "", fmt.Errorf("invalid port: %s", portStr)
	}

	p2pStr, err := maddr.ValueForProtocol(ma.P_P2P)
	if err != nil {
		return "", fmt.Errorf("relay address must include /p2p/ peer ID: %w", err)
	}
	relayPeerID, err := peer.Decode(p2pStr)
	if err != nil {
		return "", fmt.Errorf("invalid relay peer ID: %w", err)
	}

	relayIDBytes := []byte(relayPeerID)

	if len(network) > 63 {
		return "", fmt.Errorf("network namespace too long: %d bytes (max 63)", len(network))
	}

	nsBytes := []byte(network)
	buf := make([]byte, 0, 1+16+4+2+1+len(relayIDBytes)+1+len(nsBytes))
	buf = append(buf, VersionV2)
	buf = append(buf, token...)
	buf = append(buf, ip...)
	buf = append(buf, byte(port>>8), byte(port))
	buf = append(buf, byte(len(relayIDBytes)))
	buf = append(buf, relayIDBytes...)
	buf = append(buf, byte(len(nsBytes)))
	buf = append(buf, nsBytes...)

	encoded := encoding.EncodeToString(buf)

	var groups []string
	for i := 0; i < len(encoded); i += 4 {
		end := i + 4
		if end > len(encoded) {
			end = len(encoded)
		}
		groups = append(groups, encoded[i:end])
	}
	return strings.Join(groups, "-"), nil
}

// Decode parses a dash-separated base32 invite code back into InviteData.
func Decode(code string) (*InviteData, error) {
	clean := strings.ReplaceAll(code, "-", "")
	clean = strings.ReplaceAll(clean, " ", "")
	clean = strings.ToUpper(clean)

	raw, err := encoding.DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("invalid invite code: %w", err)
	}

	// Minimum: version(1) + token(8) + ip(4) + port(2) + relayIDLen(1) = 16
	if len(raw) < 16 {
		return nil, fmt.Errorf("invite code too short")
	}

	ver := raw[0]
	switch ver {
	case VersionV1:
		return decodeV1(raw)
	case VersionV2:
		return decodeV2(raw)
	default:
		if ver > VersionV2 {
			return nil, fmt.Errorf("invite code version %d is newer than supported; please upgrade shurli", ver)
		}
		return nil, fmt.Errorf("unsupported invite code version: %d", ver)
	}
}

// decodeV1 parses a v1 PAKE invite code (includes namespace field).
func decodeV1(raw []byte) (*InviteData, error) {
	var data InviteData
	data.Version = VersionV1
	copy(data.Token[:], raw[1:9])

	ip := net.IPv4(raw[9], raw[10], raw[11], raw[12])
	port := int(raw[13])<<8 | int(raw[14])

	relayIDLen := int(raw[15])
	offset := 16 + relayIDLen

	// [1] namespace length + [L] namespace bytes
	if len(raw) < offset+1 {
		return nil, fmt.Errorf("invite code truncated (missing namespace length)")
	}

	nsLen := int(raw[offset])
	offset++

	if len(raw) < offset+nsLen+1 {
		return nil, fmt.Errorf("invite code truncated (namespace data)")
	}

	if nsLen > 0 {
		data.Network = string(raw[offset : offset+nsLen])
	}
	offset += nsLen

	relayPeerID := peer.ID(raw[16 : 16+relayIDLen])
	inviterPeerID := peer.ID(raw[offset:])

	if err := validatePeerIDs(relayPeerID, inviterPeerID); err != nil {
		return nil, err
	}

	data.RelayAddr = fmt.Sprintf("/ip4/%s/tcp/%d/p2p/%s", ip.String(), port, relayPeerID.String())
	data.PeerID = inviterPeerID

	return &data, nil
}

// decodeV2 parses a v2 relay pairing invite code (no inviter peer ID, 16-byte token).
func decodeV2(raw []byte) (*InviteData, error) {
	// Minimum: version(1) + token(16) + ip(4) + port(2) + relayIDLen(1) = 24
	if len(raw) < 24 {
		return nil, fmt.Errorf("v2 invite code too short")
	}

	var data InviteData
	data.Version = VersionV2
	data.TokenV2 = make([]byte, 16)
	copy(data.TokenV2, raw[1:17])

	ip := net.IPv4(raw[17], raw[18], raw[19], raw[20])
	port := int(raw[21])<<8 | int(raw[22])

	relayIDLen := int(raw[23])
	offset := 24 + relayIDLen

	if len(raw) < offset+1 {
		return nil, fmt.Errorf("v2 invite code truncated (missing namespace length)")
	}

	nsLen := int(raw[offset])
	offset++

	if len(raw) < offset+nsLen {
		return nil, fmt.Errorf("v2 invite code truncated (namespace data)")
	}

	if nsLen > 0 {
		data.Network = string(raw[offset : offset+nsLen])
	}
	offset += nsLen

	// v2 has no trailing inviter peer ID, so check for trailing junk
	if offset != len(raw) {
		return nil, fmt.Errorf("v2 invite code has %d trailing bytes", len(raw)-offset)
	}

	relayPeerID := peer.ID(raw[24 : 24+relayIDLen])
	if err := relayPeerID.Validate(); err != nil {
		return nil, fmt.Errorf("invalid relay peer ID in invite code: %w", err)
	}
	if err := strictMultihashLen([]byte(relayPeerID)); err != nil {
		return nil, fmt.Errorf("invalid relay peer ID in invite code: %w", err)
	}

	data.RelayAddr = fmt.Sprintf("/ip4/%s/tcp/%d/p2p/%s", ip.String(), port, relayPeerID.String())
	return &data, nil
}

// validatePeerIDs checks both relay and inviter peer IDs for validity.
func validatePeerIDs(relayPeerID, inviterPeerID peer.ID) error {
	if err := relayPeerID.Validate(); err != nil {
		return fmt.Errorf("invalid relay peer ID in invite code: %w", err)
	}
	if err := inviterPeerID.Validate(); err != nil {
		return fmt.Errorf("invalid inviter peer ID in invite code: %w", err)
	}

	// Strict check: reject trailing data after the multihash.
	if err := strictMultihashLen([]byte(relayPeerID)); err != nil {
		return fmt.Errorf("invalid relay peer ID in invite code: %w", err)
	}
	if err := strictMultihashLen([]byte(inviterPeerID)); err != nil {
		return fmt.Errorf("invalid inviter peer ID in invite code: %w", err)
	}

	return nil
}

// strictMultihashLen verifies that buf is exactly one multihash with no
// trailing bytes. A multihash is: <varint-code><varint-length><digest>.
// We read both varints, then check that code+length+digest consume the
// entire buffer with nothing left over.
//
// This replaces the github.com/multiformats/go-multihash dependency.
// The multihash wire format is defined at https://multiformats.io/multihash/
// Original library: MIT License, Copyright (c) 2014 Juan Batiz-Benet.
// See THIRD_PARTY_NOTICES in the repo root.
func strictMultihashLen(buf []byte) error {
	if len(buf) < 2 {
		return fmt.Errorf("multihash too short: %d bytes", len(buf))
	}

	// Read the hash function code varint.
	_, n1 := binary.Uvarint(buf)
	if n1 <= 0 {
		return fmt.Errorf("multihash: invalid code varint")
	}

	// Read the digest length varint.
	digestLen, n2 := binary.Uvarint(buf[n1:])
	if n2 <= 0 {
		return fmt.Errorf("multihash: invalid length varint")
	}

	// The total consumed bytes must equal the buffer length exactly.
	expected := n1 + n2 + int(digestLen)
	if len(buf) != expected {
		return fmt.Errorf("multihash has %d trailing bytes", len(buf)-expected)
	}
	return nil
}
