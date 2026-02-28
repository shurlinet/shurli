package auth

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Role constants for peer authorization tiers.
const (
	// RoleAdmin grants full network management: create invites, manage peers,
	// modify policy. The first peer to join via relay pairing is auto-promoted.
	RoleAdmin = "admin"

	// RoleMember grants standard network access: connect, use services, verify peers.
	// This is the default when no role attribute is set (backward compatible).
	RoleMember = "member"
)

// GetPeerRole returns the role attribute for a peer from the authorized_keys file.
// Returns RoleMember if the peer has no explicit role (backward compatible default).
func GetPeerRole(authKeysPath string, peerID peer.ID) string {
	entries, err := ListPeers(authKeysPath)
	if err != nil {
		return RoleMember
	}
	for _, e := range entries {
		if e.PeerID == peerID {
			if e.Role == "" {
				return RoleMember
			}
			return e.Role
		}
	}
	return RoleMember
}

// SetPeerRole sets the role attribute on an authorized peer.
func SetPeerRole(authKeysPath, peerIDStr, role string) error {
	if role != RoleAdmin && role != RoleMember {
		return fmt.Errorf("invalid role %q: must be %q or %q", role, RoleAdmin, RoleMember)
	}
	return SetPeerAttr(authKeysPath, peerIDStr, "role", role)
}

// IsAdmin checks whether a peer has the admin role.
func IsAdmin(authKeysPath string, peerID peer.ID) bool {
	return GetPeerRole(authKeysPath, peerID) == RoleAdmin
}

// CountAdmins returns the number of peers with role=admin in the authorized_keys file.
func CountAdmins(authKeysPath string) (int, error) {
	entries, err := ListPeers(authKeysPath)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, e := range entries {
		if e.Role == RoleAdmin {
			count++
		}
	}
	return count, nil
}
