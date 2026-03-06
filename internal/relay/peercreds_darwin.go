package relay

import "golang.org/x/sys/unix"

// LOCAL_PEEREPID is the macOS sockopt for getting the peer's PID.
// Not exported by x/sys/unix.
const localPeerEPID = 0x001

// getPeerCreds extracts PID/UID from a Unix socket on macOS.
// UID from LOCAL_PEERCRED (Xucred), PID from LOCAL_PEEREPID.
func getPeerCreds(fd uintptr) *peerCreds {
	cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return nil
	}
	pc := &peerCreds{UID: cred.Uid}
	// Get PID via LOCAL_PEEREPID (separate getsockopt on macOS).
	if pid, err := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, localPeerEPID); err == nil {
		pc.PID = int32(pid)
	}
	return pc
}
