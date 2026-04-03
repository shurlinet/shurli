package sdk

import (
	"fmt"
	"strings"
)

// ParseByteSize parses a human-readable byte size string into bytes.
// Supports: "unlimited" (returns -1), plain numbers, and suffixes
// KB, MB, GB, TB (case-insensitive, binary: 1MB = 1048576).
func ParseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "unlimited") {
		return -1, nil
	}
	if s == "" {
		return 0, fmt.Errorf("empty size string")
	}

	// Scan digits only (no dots, no signs).
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	numStr := s[:i]
	suffix := strings.TrimSpace(s[i:])

	if numStr == "" {
		return 0, fmt.Errorf("no numeric value in %q", s)
	}

	var num int64
	if _, err := fmt.Sscanf(numStr, "%d", &num); err != nil {
		return 0, fmt.Errorf("invalid number %q: %w", numStr, err)
	}
	if num < 0 {
		return 0, fmt.Errorf("negative size not allowed: %d", num)
	}

	var multiplier int64
	switch strings.ToUpper(suffix) {
	case "", "B":
		multiplier = 1
	case "KB", "K":
		multiplier = 1024
	case "MB", "M":
		multiplier = 1024 * 1024
	case "GB", "G":
		multiplier = 1024 * 1024 * 1024
	case "TB", "T":
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown suffix %q", suffix)
	}

	result := num * multiplier
	if num != 0 && result/num != multiplier {
		return 0, fmt.Errorf("value overflows int64: %d%s", num, suffix)
	}
	return result, nil
}

// FormatBytes formats a byte count for user-facing display (e.g. "1.2 GB", "500 MB").
func FormatBytes(b int64) string {
	if b >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	}
	if b >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	}
	if b >= 1<<10 {
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}
