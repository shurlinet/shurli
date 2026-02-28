package macaroon

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Known caveat keys for the Shurli ACL system.
const (
	CaveatService  = "service"   // comma-separated service names
	CaveatGroup    = "group"     // group scope
	CaveatAction   = "action"    // comma-separated: invite, connect, admin
	CaveatPeersMax = "peers_max" // max peers this token can onboard
	CaveatDelegate = "delegate"  // "true" or "false"
	CaveatExpires  = "expires"   // RFC3339 timestamp
	CaveatNetwork  = "network"   // DHT namespace scope
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

		default:
			return fmt.Errorf("unknown caveat key: %q", key)
		}
	}
}

// VerifyContext provides the runtime context for caveat verification.
type VerifyContext struct {
	Service      string    // current service being accessed
	Group        string    // current group scope
	Action       string    // current action being performed
	PeersUsed    int       // number of peers already onboarded by this token
	IsDelegation bool      // true if this verification is for a delegation attempt
	Now          time.Time // current time (for expiry checks)
	Network      string    // current DHT namespace
}
