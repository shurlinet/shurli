package relay

import "golang.org/x/sys/unix"

// getPeerCreds extracts PID/UID from a Unix socket via SO_PEERCRED.
func getPeerCreds(fd uintptr) *peerCreds {
	cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return nil
	}
	return &peerCreds{PID: cred.Pid, UID: cred.Uid}
}
