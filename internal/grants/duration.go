package grants

import (
	"fmt"
	"time"
)

// ParseDurationExtended parses a duration string that supports d (days) and w (weeks)
// in addition to Go's standard time.ParseDuration units (h, m, s, ms, us, ns).
// Examples: "7d", "2w", "24h", "3d12h".
func ParseDurationExtended(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration string")
	}

	var result time.Duration
	remaining := s

	for len(remaining) > 0 {
		// Find the next unit suffix.
		i := 0
		for i < len(remaining) && ((remaining[i] >= '0' && remaining[i] <= '9') || remaining[i] == '.') {
			i++
		}
		if i == 0 || i >= len(remaining) {
			// Try standard ParseDuration on what's left.
			d, err := time.ParseDuration(remaining)
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q: %w", s, err)
			}
			result += d
			break
		}

		numStr := remaining[:i]
		unit := remaining[i]
		remaining = remaining[i+1:]

		switch unit {
		case 'w':
			n, err := parseFloat(numStr)
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q: %w", s, err)
			}
			result += time.Duration(n * float64(7*24*time.Hour))
		case 'd':
			n, err := parseFloat(numStr)
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q: %w", s, err)
			}
			result += time.Duration(n * float64(24*time.Hour))
		default:
			// Standard unit - put it back and let ParseDuration handle it.
			chunk := numStr + string(unit)
			// Grab any remaining standard suffix chars.
			for len(remaining) > 0 && remaining[0] != '.' && (remaining[0] < '0' || remaining[0] > '9') {
				chunk += string(remaining[0])
				remaining = remaining[1:]
			}
			d, err := time.ParseDuration(chunk)
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q: %w", s, err)
			}
			result += d
		}
	}

	if result < 0 {
		return 0, fmt.Errorf("negative duration %q", s)
	}

	return result, nil
}

func parseFloat(s string) (float64, error) {
	var n float64
	_, err := fmt.Sscanf(s, "%f", &n)
	return n, err
}
