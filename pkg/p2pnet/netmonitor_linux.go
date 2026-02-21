//go:build linux

package p2pnet

import (
	"context"
	"log/slog"
	"syscall"
	"unsafe"
)

// Netlink multicast groups for address and link change notifications.
const (
	rtmgrpLink      = 0x1  // RTMGRP_LINK
	rtmgrpIPv4Addr  = 0x10 // RTMGRP_IPV4_IFADDR
	rtmgrpIPv6Addr  = 0x20 // RTMGRP_IPV6_IFADDR
)

// watchNetworkChanges uses a Netlink socket to receive kernel notifications
// when network interfaces or addresses change. Event-driven: zero CPU cost.
func watchNetworkChanges(ctx context.Context, ch chan<- struct{}) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_DGRAM, syscall.NETLINK_ROUTE)
	if err != nil {
		slog.Warn("netmonitor: netlink socket failed, falling back to polling", "error", err)
		pollNetworkChanges(ctx, ch)
		return
	}
	defer syscall.Close(fd)

	// Bind to multicast groups for link and address changes
	addr := &syscall.SockaddrNetlink{
		Family: syscall.AF_NETLINK,
		Groups: rtmgrpLink | rtmgrpIPv4Addr | rtmgrpIPv6Addr,
	}
	if err := syscall.Bind(fd, addr); err != nil {
		slog.Warn("netmonitor: netlink bind failed, falling back to polling", "error", err)
		pollNetworkChanges(ctx, ch)
		return
	}

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

		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			if isTimeoutLinux(err) {
				continue
			}
			slog.Warn("netmonitor: netlink read error", "error", err)
			continue
		}
		if n < int(unsafe.Sizeof(nlmsghdr{})) {
			continue
		}

		// Parse netlink message headers to find relevant events
		for offset := 0; offset+int(unsafe.Sizeof(nlmsghdr{})) <= n; {
			hdr := (*nlmsghdr)(unsafe.Pointer(&buf[offset]))
			if hdr.Len < uint32(unsafe.Sizeof(nlmsghdr{})) || int(hdr.Len) > n-offset {
				break
			}

			switch hdr.Type {
			case syscall.RTM_NEWADDR, syscall.RTM_DELADDR,
				syscall.RTM_NEWLINK, syscall.RTM_DELLINK:
				select {
				case ch <- struct{}{}:
				default:
				}
			}

			// Align to 4-byte boundary
			offset += int((hdr.Len + 3) & ^uint32(3))
		}
	}
}

// nlmsghdr matches the Linux netlink message header.
type nlmsghdr struct {
	Len   uint32
	Type  uint16
	Flags uint16
	Seq   uint32
	Pid   uint32
}

// isTimeoutLinux returns true if the error is a socket timeout.
func isTimeoutLinux(err error) bool {
	if errno, ok := err.(syscall.Errno); ok {
		return errno == syscall.EAGAIN || errno == syscall.EWOULDBLOCK
	}
	return false
}
