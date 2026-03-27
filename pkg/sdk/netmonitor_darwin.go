//go:build darwin

package sdk

import (
	"context"
	"log/slog"
	"syscall"
	"unsafe"
)

// watchNetworkChanges uses a BSD route socket (AF_ROUTE) to receive
// kernel notifications when network interfaces or addresses change.
// This is event-driven: zero CPU when nothing changes.
func watchNetworkChanges(ctx context.Context, ch chan<- struct{}) {
	fd, err := syscall.Socket(syscall.AF_ROUTE, syscall.SOCK_RAW, syscall.AF_UNSPEC)
	if err != nil {
		slog.Warn("netmonitor: route socket failed, falling back to polling", "error", err)
		pollNetworkChanges(ctx, ch)
		return
	}
	defer syscall.Close(fd)

	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Set a read deadline so we can check ctx.Done() periodically
		tv := syscall.Timeval{Sec: 2}
		syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

		n, err := syscall.Read(fd, buf)
		if err != nil {
			// Timeout is expected - check context and loop
			if isTimeout(err) {
				continue
			}
			slog.Warn("netmonitor: route socket read error", "error", err)
			continue
		}
		if n < int(unsafe.Sizeof(routeMessageHeader{})) {
			continue
		}

		// Parse the routing message header to filter relevant events
		hdr := (*routeMessageHeader)(unsafe.Pointer(&buf[0]))
		switch hdr.Type {
		case syscall.RTM_NEWADDR, syscall.RTM_DELADDR,
			syscall.RTM_IFINFO,
			syscall.RTM_ADD, syscall.RTM_DELETE, syscall.RTM_CHANGE:
			// Address added/removed, interface state changed, or route changed.
			// Route changes (ADD/DELETE/CHANGE) catch WiFi hotspot switches
			// where only the default gateway changes (private IPv4, no global IP diff).
			slog.Debug("netmonitor: route socket event", "type", hdr.Type)
			select {
			case ch <- struct{}{}:
			default:
				// Channel already has a pending event
			}
		}
	}
}

// routeMessageHeader matches the rt_msghdr structure on macOS.
type routeMessageHeader struct {
	Msglen  uint16
	Version uint8
	Type    uint8
	// ... remaining fields not needed for filtering
}

// isTimeout returns true if the error is a socket timeout.
func isTimeout(err error) bool {
	if errno, ok := err.(syscall.Errno); ok {
		return errno == syscall.EAGAIN || errno == syscall.EWOULDBLOCK
	}
	return false
}
