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
	CaveatTransport          = "transport"            // bitmask: lan|direct|relay (comma-separated tokens)
)

// TransportType mirrors pkg/sdk.TransportType with identical bit values so
// callers can pass the sdk value directly. Defined here to keep the macaroon
// package free of sdk imports (no import cycles).
type TransportType int

const (
	TransportLAN    TransportType = 1 << 0
	TransportDirect TransportType = 1 << 1
	TransportRelay  TransportType = 1 << 2
)

// ParseTransportMask parses a transport caveat value. Accepts comma-separated
// tokens ("lan,direct,relay") or a decimal bitmask ("7"). Whitespace around
// tokens is tolerated. Returns 0 and an error for unknown tokens or junk
// values so verification fails closed.
func ParseTransportMask(value string) (TransportType, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return 0, fmt.Errorf("empty transport value")
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n <= 0 {
			return 0, fmt.Errorf("invalid transport bitmask %d", n)
		}
		const allBits = int(TransportLAN | TransportDirect | TransportRelay)
		if n&^allBits != 0 {
			return 0, fmt.Errorf("invalid transport bitmask %d: unknown bits set", n)
		}
		return TransportType(n), nil
	}
	var mask TransportType
	for _, tok := range strings.Split(v, ",") {
		switch strings.ToLower(strings.TrimSpace(tok)) {
		case "lan":
			mask |= TransportLAN
		case "direct":
			mask |= TransportDirect
		case "relay":
			mask |= TransportRelay
		case "":
			// skip trailing/leading commas
		default:
			return 0, fmt.Errorf("unknown transport token %q", tok)
		}
	}
	if mask == 0 {
		return 0, fmt.Errorf("empty transport mask")
	}
	return mask, nil
}

// FormatTransportMask returns the canonical comma-separated token form of
// a transport bitmask. Returns an empty string for 0 (caller treats empty as
// "no caveat"). Symmetric with ParseTransportMask which rejects empty/zero.
// Unknown high bits are ignored.
func FormatTransportMask(mask TransportType) string {
	if mask == 0 {
		return ""
	}
	var parts []string
	if mask&TransportLAN != 0 {
		parts = append(parts, "lan")
	}
	if mask&TransportDirect != 0 {
		parts = append(parts, "direct")
	}
	if mask&TransportRelay != 0 {
		parts = append(parts, "relay")
	}
	return strings.Join(parts, ",")
}

// EffectiveTransportMask walks a set of caveats and returns the intersection
// (AND) of every transport caveat it finds. Returns:
//
//   - 0 when no transport caveat is present (token is unrestricted),
//   - the AND-combined mask when one or more caveats narrow transport,
//   - 0 and an error when any caveat value is malformed.
//
// This mirrors how DefaultVerifier composes caveats: each caveat further
// narrows the allowed set. A multi-hop delegation chain that adds
// transport=lan,direct to a parent transport=lan,direct,relay yields lan,direct.
//
// Display-only — not a security boundary. Use DefaultVerifier for enforcement.
func EffectiveTransportMask(caveats []string) (TransportType, error) {
	var mask TransportType
	sawAny := false
	for _, c := range caveats {
		key, value, perr := ParseCaveat(c)
		if perr != nil || key != CaveatTransport {
			continue
		}
		m, merr := ParseTransportMask(value)
		if merr != nil {
			return 0, merr
		}
		if !sawAny {
			mask = m
			sawAny = true
		} else {
			mask &= m
		}
	}
	return mask, nil
}

// TokenAllowsTransport reports whether the base64-encoded macaroon permits
// the given transport based ONLY on its transport caveats. This is a
// client-side heuristic for pre-dial decisions — it does NOT verify the
// HMAC chain. Use only for UX hints (e.g. skipping a relay attempt that
// would be rejected anyway). The authoritative check is DefaultVerifier +
// Macaroon.Verify on the server side.
//
// Semantics:
//   - token decodes and has zero transport caveats: returns true (no restriction)
//   - token has one or more transport caveats: returns true only if the
//     requested transport is permitted by EVERY caveat (AND-semantics,
//     matching how the real verifier composes them)
//   - token is malformed: returns false (fail-closed for hints)
//   - transport is 0: returns true (no restriction to enforce)
func TokenAllowsTransport(tokenBase64 string, transport TransportType) bool {
	if transport == 0 {
		return true
	}
	tok, err := DecodeBase64(tokenBase64)
	if err != nil {
		return false
	}
	for _, c := range tok.Caveats {
		key, value, perr := ParseCaveat(c)
		if perr != nil || key != CaveatTransport {
			continue
		}
		allowed, merr := ParseTransportMask(value)
		if merr != nil {
			return false // malformed caveat — fail closed
		}
		if transport&allowed == 0 {
			return false
		}
	}
	return true
}

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

		case CaveatTransport:
			// Transport caveat restricts which connection types the token
			// authorizes (lan, direct, relay). A child caveat can only
			// narrow the mask — widening is structurally impossible because
			// the macaroon verifier ANDs all caveats, so any added caveat
			// further restricts the allowed set.
			if ctx.Transport == 0 {
				return nil // no transport context, skip check
			}
			allowed, err := ParseTransportMask(value)
			if err != nil {
				return fmt.Errorf("invalid transport caveat: %w", err)
			}
			if ctx.Transport&allowed == 0 {
				return fmt.Errorf("transport %s not allowed by caveat (allowed: %s)",
					FormatTransportMask(ctx.Transport), FormatTransportMask(allowed))
			}
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
	Now          time.Time     // current time (for expiry checks)
	Network      string        // current DHT namespace
	Transport    TransportType // current stream transport (LAN/Direct/Relay); 0 = skip transport caveat
}
