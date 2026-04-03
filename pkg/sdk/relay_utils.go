package sdk

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// RelayGrantChecker provides relay grant information for transfer decisions.
// Implemented by grants.GrantCache via structural typing (no import needed).
type RelayGrantChecker interface {
	// GrantStatus returns grant info for a relay. ok=false if no cached/valid grant.
	GrantStatus(relayID peer.ID) (remaining time.Duration, budget int64, sessionDuration time.Duration, ok bool)
	// HasSufficientBudget checks if the session budget can handle fileSize.
	HasSufficientBudget(relayID peer.ID, fileSize int64, direction string) bool
	// TrackCircuitBytes increments the byte counter for a relay circuit.
	TrackCircuitBytes(relayID peer.ID, direction string, n int64)
	// ResetCircuitCounters resets per-circuit byte counters (new circuit).
	ResetCircuitCounters(relayID peer.ID)
}

// RelayPeerFromAddr extracts the relay peer ID from a circuit relay multiaddr.
// Returns empty peer.ID if the address is not a circuit relay address.
// Used by hasAnyActiveRelayGrant to check connections and peerstore addresses.
func RelayPeerFromAddr(addr ma.Multiaddr) peer.ID {
	var lastP2P peer.ID
	foundCircuit := false
	ma.ForEach(addr, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_P2P:
			if !foundCircuit {
				pid, err := peer.Decode(c.Value())
				if err == nil {
					lastP2P = pid
				}
			}
		case ma.P_CIRCUIT:
			foundCircuit = true
		}
		return true
	})
	if !foundCircuit {
		return ""
	}
	return lastP2P
}

// RelayPeerFromAddrStr extracts the relay peer ID string from a circuit relay
// multiaddr string. Returns empty string if the address is not a relay circuit.
func RelayPeerFromAddrStr(addrStr string) string {
	maddr, err := ma.NewMultiaddr(addrStr)
	if err != nil {
		return ""
	}
	pid := RelayPeerFromAddr(maddr)
	if pid == "" {
		return ""
	}
	return pid.String()
}
