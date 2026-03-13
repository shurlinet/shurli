//go:build darwin

package p2pnet

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// defaultGateway returns the IPv4 default gateway address by parsing
// the output of "route -n get default". Returns "" if unavailable.
func defaultGateway() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "/sbin/route", "-n", "get", "default").Output()
	if err != nil {
		return ""
	}

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gateway:") {
			gw := strings.TrimSpace(strings.TrimPrefix(line, "gateway:"))
			if gw != "" && !strings.Contains(gw, " ") {
				return gw
			}
		}
	}
	return ""
}
