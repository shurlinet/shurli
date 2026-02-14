package invite

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	mh "github.com/multiformats/go-multihash"
)

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// InviteData holds the payload encoded in an invite code.
type InviteData struct {
	Token     [8]byte
	RelayAddr string  // full relay multiaddr (e.g., /ip4/.../tcp/.../p2p/...)
	PeerID    peer.ID // inviter's peer ID
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
// Binary format:
//
//	[1] version (0x01)
//	[8] token
//	[4] relay IPv4
//	[2] relay TCP port (big-endian)
//	[1] relay peer ID length
//	[N] relay peer ID (raw multihash bytes)
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

	buf := make([]byte, 0, 1+8+4+2+1+len(relayIDBytes)+len(inviterIDBytes))
	buf = append(buf, 0x01) // version
	buf = append(buf, data.Token[:]...)
	buf = append(buf, ip...)
	buf = append(buf, byte(port>>8), byte(port))
	buf = append(buf, byte(len(relayIDBytes)))
	buf = append(buf, relayIDBytes...)
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

	if raw[0] != 0x01 {
		return nil, fmt.Errorf("unsupported invite code version: %d", raw[0])
	}

	var data InviteData
	copy(data.Token[:], raw[1:9])

	ip := net.IPv4(raw[9], raw[10], raw[11], raw[12])
	port := int(raw[13])<<8 | int(raw[14])

	relayIDLen := int(raw[15])
	if len(raw) < 16+relayIDLen+1 {
		return nil, fmt.Errorf("invite code truncated")
	}

	relayPeerID := peer.ID(raw[16 : 16+relayIDLen])
	inviterPeerID := peer.ID(raw[16+relayIDLen:])

	// Validate peer IDs: basic multihash check
	if err := relayPeerID.Validate(); err != nil {
		return nil, fmt.Errorf("invalid relay peer ID in invite code: %w", err)
	}
	if err := inviterPeerID.Validate(); err != nil {
		return nil, fmt.Errorf("invalid inviter peer ID in invite code: %w", err)
	}

	// Strict check: reject trailing data after the inviter multihash.
	// Go's base32 NoPadding decoder silently accepts extra characters,
	// so we must verify the byte slice is exactly one valid multihash.
	if err := strictMultihashLen([]byte(relayPeerID)); err != nil {
		return nil, fmt.Errorf("invalid relay peer ID in invite code: %w", err)
	}
	if err := strictMultihashLen([]byte(inviterPeerID)); err != nil {
		return nil, fmt.Errorf("invalid inviter peer ID in invite code: %w", err)
	}

	data.RelayAddr = fmt.Sprintf("/ip4/%s/tcp/%d/p2p/%s", ip.String(), port, relayPeerID.String())
	data.PeerID = inviterPeerID

	return &data, nil
}

// strictMultihashLen verifies that buf is exactly one multihash with no
// trailing bytes. multihash.Decode is lenient about trailing data, so we
// re-encode from the parsed digest and compare lengths.
func strictMultihashLen(buf []byte) error {
	dm, err := mh.Decode(buf)
	if err != nil {
		return err
	}
	canonical, err := mh.Encode(dm.Digest, dm.Code)
	if err != nil {
		return err
	}
	if len(buf) != len(canonical) {
		return fmt.Errorf("multihash has %d trailing bytes", len(buf)-len(canonical))
	}
	return nil
}
