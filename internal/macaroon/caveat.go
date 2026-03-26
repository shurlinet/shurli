package macaroon

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Known caveat keys for the Shurli capability token system.
const (
	CaveatPeerID         = "peer_id"         // libp2p peer ID this token is bound to
	CaveatService        = "service"         // comma-separated service names
	CaveatGroup          = "group"           // group scope
	CaveatAction         = "action"          // comma-separated: invite, connect, admin
	CaveatPeersMax       = "peers_max"       // max peers this token can onboard
	CaveatDelegate       = "delegate"        // "true" or "false" (legacy, still verified)
	CaveatDelegateTo     = "delegate_to"     // peer ID of the delegated bearer
	CaveatMaxDelegations    = "max_delegations"     // remaining delegation hops: 0=none, N=limited, -1=unlimited
	CaveatExpires           = "expires"             // RFC3339 timestamp
	CaveatNetwork           = "network"             // DHT namespace scope
	CaveatAutoRefresh       = "auto_refresh"        // "true" or "false"
	CaveatMaxRefreshes      = "max_refreshes"       // remaining refresh count
	CaveatMaxRefreshDuration = "max_refresh_duration" // RFC3339 absolute deadline for all refreshes
)

// ParseCaveat splits a "key=value" caveat string into its components.
func ParseCaveat(s string) (key, value string, err error) {
	k, v, ok := strings.Cut(s, "=")
	if !ok {
		return "", "", fmt.Errorf("invalid caveat format (expected key=value): %q", s)
	}
	key = strings.TrimSpace(k)
	value = strings.TrimSpace(v)
	if key == "" {
		return "", "", fmt.Errorf("empty caveat key in: %q", s)
	}
	return key, value, nil
}

// DefaultVerifier builds a CaveatVerifier that checks all known caveat types
// against the provided context. Unknown caveats are rejected (fail-closed).
func DefaultVerifier(ctx VerifyContext) CaveatVerifier {
	return func(caveat string) error {
		key, value, err := ParseCaveat(caveat)
		if err != nil {
			return err
		}

		switch key {
		case CaveatPeerID:
			if ctx.PeerID == "" {
				return nil // no peer context, skip check
			}
			if value == ctx.PeerID {
				return nil
			}
			// Allow if the presenting peer is a valid delegatee.
			if ctx.DelegateTo == ctx.PeerID && ctx.DelegateTo != "" {
				return nil
			}
			return fmt.Errorf("peer %q does not match required %q", ctx.PeerID, value)

		case CaveatService:
			if ctx.Service == "" {
				return nil // no service context, skip check
			}
			allowed := strings.Split(value, ",")
			for _, s := range allowed {
				if strings.TrimSpace(s) == ctx.Service {
					return nil
				}
			}
			return fmt.Errorf("service %q not in allowed list %q", ctx.Service, value)

		case CaveatGroup:
			if ctx.Group == "" {
				return nil
			}
			if value != ctx.Group {
				return fmt.Errorf("group %q does not match required %q", ctx.Group, value)
			}
			return nil

		case CaveatAction:
			if ctx.Action == "" {
				return nil
			}
			allowed := strings.Split(value, ",")
			for _, a := range allowed {
				if strings.TrimSpace(a) == ctx.Action {
					return nil
				}
			}
			return fmt.Errorf("action %q not in allowed list %q", ctx.Action, value)

		case CaveatPeersMax:
			max, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid peers_max value: %q", value)
			}
			if ctx.PeersUsed >= max {
				return fmt.Errorf("peers_max %d reached (used: %d)", max, ctx.PeersUsed)
			}
			return nil

		case CaveatDelegate:
			if value == "false" && ctx.IsDelegation {
				return fmt.Errorf("delegation not allowed")
			}
			return nil

		case CaveatDelegateTo:
			// delegate_to caveats are cryptographic audit trail, not enforcement points.
			// The HMAC chain proves each delegation step. Bearer enforcement is via
			// CaveatPeerID + VerifyContext.DelegateTo (set from the last delegate_to).
			// In a multi-hop chain (A->B->C->D), earlier delegate_to values (B, C)
			// must not block the current bearer (D). Always pass.
			return nil

		case CaveatMaxDelegations:
			max, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid max_delegations value: %q", value)
			}
			// -1 = unlimited, 0 = no delegation, N = N hops remaining
			if max == -1 {
				return nil // unlimited
			}
			if ctx.IsDelegation && max <= 0 {
				return fmt.Errorf("max_delegations exhausted (value: %d)", max)
			}
			// NOTE: delegate_to injection defense is NOT here. The per-caveat verifier
			// is stateless and can't distinguish "only max_delegations=0" (attack) from
			// "max_delegations=0 at end of a legitimate chain" (multi-hop). The defense
			// is in the TokenVerifier via HasPermissiveDelegation(), which checks ALL
			// max_delegations caveats together.
			return nil

		case CaveatExpires:
			t, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return fmt.Errorf("invalid expires timestamp: %q", value)
			}
			if ctx.Now.After(t) {
				return fmt.Errorf("token expired at %s", value)
			}
			return nil

		case CaveatNetwork:
			if ctx.Network == "" {
				return nil
			}
			if value != ctx.Network {
				return fmt.Errorf("network %q does not match required %q", ctx.Network, value)
			}
			return nil

		case CaveatAutoRefresh:
			// Informational caveat - always passes verification.
			return nil

		case CaveatMaxRefreshes:
			// Informational caveat stored on the grant. Enforcement is in Store.Refresh().
			return nil

		case CaveatMaxRefreshDuration:
			// Informational caveat: records the absolute refresh deadline.
			// Enforcement is in Store.Refresh(), NOT here. If this verifier
			// rejected after the deadline, it would kill valid tokens whose
			// last refresh extended expiry past the refresh deadline.
			return nil

		default:
			return fmt.Errorf("unknown caveat key: %q", key)
		}
	}
}

// ExtractDelegateTo scans a token's caveats for the LAST delegate_to value.
// In a multi-hop delegation chain, multiple delegate_to caveats accumulate.
// The last one identifies the current bearer. Earlier ones are audit trail.
// Returns the delegate peer ID if found, empty string otherwise.
// Used by callers to populate VerifyContext.DelegateTo before verification.
func ExtractDelegateTo(caveats []string) string {
	var last string
	for _, c := range caveats {
		key, value, err := ParseCaveat(c)
		if err != nil {
			continue
		}
		if key == CaveatDelegateTo {
			last = value
		}
	}
	return last
}

// HasPermissiveDelegation checks if caveats contain at least one max_delegations
// value that permits delegation (> 0 or -1). Returns false if no max_delegations
// caveat exists or all values are 0. Used as a belt-and-suspenders check for
// pre-B3 tokens that lack max_delegations and could have delegate_to injected.
func HasPermissiveDelegation(caveats []string) bool {
	for _, c := range caveats {
		key, value, err := ParseCaveat(c)
		if err != nil {
			continue
		}
		if key != CaveatMaxDelegations {
			continue
		}
		v, err := strconv.Atoi(value)
		if err != nil {
			continue
		}
		if v != 0 {
			return true // -1 (unlimited) or N > 0
		}
	}
	return false
}

// ExtractEarliestExpires scans a token's caveats for the earliest expires value.
// In a delegation chain, multiple expires caveats may accumulate. The earliest
// one is the effective expiry. Returns zero time if no expires caveat is found.
func ExtractEarliestExpires(caveats []string) time.Time {
	var earliest time.Time
	for _, c := range caveats {
		key, value, err := ParseCaveat(c)
		if err != nil {
			continue
		}
		if key != CaveatExpires {
			continue
		}
		t, err := time.Parse(time.RFC3339, value)
		if err != nil {
			continue
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest
}

// VerifyContext provides the runtime context for caveat verification.
type VerifyContext struct {
	PeerID       string    // libp2p peer ID of the requesting peer
	Service      string    // current service being accessed
	Group        string    // current group scope
	Action       string    // current action being performed
	PeersUsed    int       // number of peers already onboarded by this token
	IsDelegation bool      // true if this verification is for a delegation attempt
	DelegateTo   string    // if set, the delegate_to peer ID from the token (allows peer_id bypass)
	Now          time.Time // current time (for expiry checks)
	Network      string    // current DHT namespace
}
