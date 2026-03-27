//go:build linux

package sdk

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// defaultGateway returns the IPv4 default gateway address by parsing
// the output of "ip route show default". Returns "" if unavailable.
func defaultGateway() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "/sbin/ip", "route", "show", "default").Output()
	if err != nil {
		slog.Debug("netmonitor: gateway detection failed", "error", err)
		return ""
	}

	// Format: "default via 10.1.60.1 dev eth0 proto dhcp metric 100"
	// Multiple default routes possible; first line = lowest metric = primary.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return ""
	}

	fields := strings.Fields(lines[0])
	for i, f := range fields {
		if f == "via" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}
