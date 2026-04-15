package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	libnet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	ma "github.com/multiformats/go-multiaddr"
)

// diagSnapshot writes a read-only snapshot of daemon state to w. It exists
// to distinguish the candidate hypotheses documented in Step 22 of
// project-performance-baseline-2026-03-15.md (rcmgr scope exhaustion vs
// peerstore growth vs OS/systemd resource caps vs Bug C regression vs mDNS
// dial-storm). Observe-only: nothing here mutates daemon state.
//
// The function takes host.Host (not *serveRuntime) so it can be unit
// tested against a plain libp2p host.
func diagSnapshot(h host.Host, w io.Writer) {
	nw := h.Network()
	ps := h.Peerstore()
	self := h.ID()

	writeHeader(w, self)
	writeInterfaces(w)
	writeProcLimits(w)
	writeProcStatus(w)
	writeOpenFDCount(w)
	writeSockstat(w)
	writeCgroupMemory(w)
	writeRcmgr(w, nw.ResourceManager())
	writePeerstore(w, nw, ps, self)

	fmt.Fprintln(w, "note: libp2p swarm dial backoff is unexported; not captured.")
	fmt.Fprintln(w, "note: per-address TTL class is not exposed by the libp2p public API.")
	fmt.Fprintln(w, "      addresses are tagged [live] if matched against an active Conn's")
	fmt.Fprintln(w, "      RemoteMultiaddr, otherwise [stale] (not in connected-TTL class).")
	fmt.Fprintln(w, "=== end diag ===")
}

func writeHeader(w io.Writer, self peer.ID) {
	fmt.Fprintln(w, "# PRIVATE — contains peer IDs, interface addresses, and")
	fmt.Fprintln(w, "# cgroup paths. Do NOT commit, paste into public issues,")
	fmt.Fprintln(w, "# or share outside your infrastructure. Local diagnostic only.")
	hn, _ := os.Hostname()
	fmt.Fprintf(w, "=== diag %s host=%s self=%s go=%s goroutines=%d pid=%d ===\n",
		time.Now().Format(time.RFC3339Nano), hn, shortPeerID(self),
		runtime.Version(), runtime.NumGoroutine(), os.Getpid())
}

func writeInterfaces(w io.Writer) {
	fmt.Fprintln(w, "\n-- interfaces --")
	ifs, err := net.Interfaces()
	if err != nil {
		fmt.Fprintf(w, "net.Interfaces error: %v\n", err)
		return
	}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, aerr := ifc.Addrs()
		// Flags reveal utun (point-to-point, no broadcast), tun/tap
		// interfaces, and loopback. MTU exposes tunnel overhead paths.
		fmt.Fprintf(w, "%s mtu=%d flags=%s:", ifc.Name, ifc.MTU, ifaceFlagsString(ifc.Flags))
		if aerr != nil {
			fmt.Fprintf(w, " (addrs err: %v)", aerr)
		}
		for _, a := range addrs {
			fmt.Fprintf(w, " %s", a.String())
		}
		fmt.Fprintln(w)
	}
}

// ifaceFlagsString renders net.Interface flags as a compact comma list.
// Only emits flags that are set, so the output is short on typical
// interfaces but diagnostic on unusual ones (point-to-point, no broadcast).
func ifaceFlagsString(f net.Flags) string {
	var parts []string
	if f&net.FlagUp != 0 {
		parts = append(parts, "up")
	}
	if f&net.FlagBroadcast != 0 {
		parts = append(parts, "broadcast")
	}
	if f&net.FlagLoopback != 0 {
		parts = append(parts, "loopback")
	}
	if f&net.FlagPointToPoint != 0 {
		parts = append(parts, "p2p")
	}
	if f&net.FlagMulticast != 0 {
		parts = append(parts, "multicast")
	}
	if f&net.FlagRunning != 0 {
		parts = append(parts, "running")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

func writeProcLimits(w io.Writer) {
	fmt.Fprintln(w, "\n-- /proc/self/limits --")
	b, err := os.ReadFile("/proc/self/limits")
	if err != nil {
		fmt.Fprintf(w, "unavailable: %v\n", err)
		return
	}
	fmt.Fprint(w, string(b))
}

func writeProcStatus(w io.Writer) {
	fmt.Fprintln(w, "\n-- /proc/self/status (key fields) --")
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		fmt.Fprintf(w, "unavailable: %v\n", err)
		return
	}
	for _, l := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(l, "VmRSS:"),
			strings.HasPrefix(l, "VmSize:"),
			strings.HasPrefix(l, "VmPeak:"),
			strings.HasPrefix(l, "Threads:"),
			strings.HasPrefix(l, "FDSize:"),
			strings.HasPrefix(l, "voluntary_ctxt_switches:"),
			strings.HasPrefix(l, "nonvoluntary_ctxt_switches:"):
			fmt.Fprintln(w, l)
		}
	}
}

func writeOpenFDCount(w io.Writer) {
	fmt.Fprintln(w, "\n-- open file descriptors --")
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		fmt.Fprintf(w, "/proc/self/fd unavailable: %v\n", err)
		return
	}
	// Enumerating /proc/self/fd briefly opens its own dir fd, so the count
	// may be off-by-one high relative to steady state. Documented so a
	// reader interpreting the number does not chase a phantom FD leak.
	fmt.Fprintf(w, "open_fds=%d (includes the readdir fd itself)\n", len(entries))
}

// writeSockstat dumps /proc/net/sockstat and /proc/net/sockstat6. These
// files give OS-level TCP/UDP/raw socket counts and memory usage, which
// are independent of libp2p rcmgr and directly relevant to hypothesis (c)
// (systemd/kernel resource exhaustion) and the interplay between rcmgr
// scope exhaustion and kernel socket exhaustion. Linux only.
func writeSockstat(w io.Writer) {
	fmt.Fprintln(w, "\n-- /proc/net/sockstat --")
	if b, err := os.ReadFile("/proc/net/sockstat"); err == nil {
		fmt.Fprint(w, string(b))
	} else {
		fmt.Fprintf(w, "unavailable: %v\n", err)
	}
	if b, err := os.ReadFile("/proc/net/sockstat6"); err == nil {
		fmt.Fprintln(w, "-- /proc/net/sockstat6 --")
		fmt.Fprint(w, string(b))
	}
}

// writeCgroupMemory reads systemd-scoped memory limits and usage. Handles
// cgroup v2 (unified) and cgroup v1 (memory controller). Linux only.
func writeCgroupMemory(w io.Writer) {
	fmt.Fprintln(w, "\n-- cgroup memory --")
	cgPath, err := readProcSelfCgroup()
	if err != nil {
		fmt.Fprintf(w, "read /proc/self/cgroup: %v\n", err)
		return
	}
	if cgPath == "" {
		fmt.Fprintln(w, "no cgroup path")
		return
	}
	fmt.Fprintf(w, "cgroup=%s\n", cgPath)

	tryRead := func(label, path string) {
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "%s=%s", label, strings.TrimSpace(string(b)))
		fmt.Fprintln(w)
	}
	v2Base := filepath.Join("/sys/fs/cgroup", cgPath)
	tryRead("memory.current (v2)", filepath.Join(v2Base, "memory.current"))
	tryRead("memory.max (v2)", filepath.Join(v2Base, "memory.max"))
	tryRead("memory.swap.current (v2)", filepath.Join(v2Base, "memory.swap.current"))
	tryRead("pids.current (v2)", filepath.Join(v2Base, "pids.current"))
	tryRead("pids.max (v2)", filepath.Join(v2Base, "pids.max"))

	v1Base := filepath.Join("/sys/fs/cgroup/memory", cgPath)
	tryRead("memory.usage_in_bytes (v1)", filepath.Join(v1Base, "memory.usage_in_bytes"))
	tryRead("memory.limit_in_bytes (v1)", filepath.Join(v1Base, "memory.limit_in_bytes"))
}

// readProcSelfCgroup returns the unified cgroup v2 path, or on v1 the
// memory controller path. Empty string if neither is present.
func readProcSelfCgroup() (string, error) {
	f, err := os.Open("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	defer f.Close()
	return parseProcSelfCgroup(f)
}

// parseProcSelfCgroup parses /proc/self/cgroup content from r. Split from
// readProcSelfCgroup so it can be unit tested against crafted byte inputs
// without touching the real procfs.
//
// Format per line (see cgroups(7)): "<hier-id>:<controllers>:<path>".
// Cgroup v2 unified hierarchy produces "0::<path>".
// Cgroup v1 produces "N:<comma-separated-controllers>:<path>".
// Preference: v2 path if present, else the v1 memory-controller path.
func parseProcSelfCgroup(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	var v1Memory, v2 string
	for sc.Scan() {
		line := sc.Text()
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "0" && parts[1] == "" {
			v2 = parts[2]
			continue
		}
		for _, c := range strings.Split(parts[1], ",") {
			if c == "memory" {
				v1Memory = parts[2]
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if v2 != "" {
		return v2, nil
	}
	return v1Memory, nil
}

func writeRcmgr(w io.Writer, rm libnet.ResourceManager) {
	fmt.Fprintln(w, "\n-- rcmgr scopes --")
	state, ok := rm.(rcmgr.ResourceManagerState)
	if !ok {
		fmt.Fprintf(w, "rcmgr type %T does not expose ResourceManagerState\n", rm)
		return
	}
	st := state.Stat()
	writeScope(w, "system", st.System)
	writeScope(w, "transient", st.Transient)
	svcNames := make([]string, 0, len(st.Services))
	for name := range st.Services {
		svcNames = append(svcNames, name)
	}
	sort.Strings(svcNames)
	for _, name := range svcNames {
		writeScope(w, "svc:"+name, st.Services[name])
	}
	protoNames := make([]string, 0, len(st.Protocols))
	for p := range st.Protocols {
		protoNames = append(protoNames, string(p))
	}
	sort.Strings(protoNames)
	for _, name := range protoNames {
		s := st.Protocols[protocol.ID(name)]
		if s.NumConnsInbound+s.NumConnsOutbound+s.NumStreamsInbound+s.NumStreamsOutbound == 0 {
			continue
		}
		writeScope(w, "proto:"+name, s)
	}
	pids := make([]peer.ID, 0, len(st.Peers))
	for pid := range st.Peers {
		pids = append(pids, pid)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i].String() < pids[j].String() })
	for _, pid := range pids {
		s := st.Peers[pid]
		if s.NumConnsInbound+s.NumConnsOutbound+s.NumStreamsInbound+s.NumStreamsOutbound == 0 {
			continue
		}
		writeScope(w, "peer:"+shortPeerID(pid), s)
	}
}

func writeScope(w io.Writer, label string, s libnet.ScopeStat) {
	fmt.Fprintf(w, "%-30s conns=in:%d/out:%d streams=in:%d/out:%d fd=%d mem=%d\n",
		label, s.NumConnsInbound, s.NumConnsOutbound,
		s.NumStreamsInbound, s.NumStreamsOutbound, s.NumFD, s.Memory)
}

func writePeerstore(w io.Writer, nw libnet.Network, ps peerstoreLike, self peer.ID) {
	peerFilter := strings.TrimSpace(os.Getenv("SHURLI_DIAG_PEER"))
	peers := ps.PeersWithAddrs()
	sort.Slice(peers, func(i, j int) bool {
		return len(ps.Addrs(peers[i])) > len(ps.Addrs(peers[j]))
	})
	fmt.Fprintf(w, "\n-- peerstore: %d peers (total active conns=%d", len(peers), len(nw.Conns()))
	if peerFilter != "" {
		fmt.Fprintf(w, ", filter=%s", peerFilter)
	}
	fmt.Fprintln(w, ") --")
	matched := 0
	for _, p := range peers {
		if p == self {
			continue
		}
		if peerFilter != "" && p.String() != peerFilter {
			continue
		}
		matched++
		addrs := ps.Addrs(p)
		conns := nw.ConnsToPeer(p)

		circuits := 0
		// liveKeys holds the transport-only multiaddr bytes of every active
		// Conn to this peer (peer ID suffix stripped via peer.SplitAddr) so
		// we can compare against peerstore addrs using multiaddr equality
		// instead of a fragile string prefix match.
		liveKeys := make(map[string]struct{}, len(conns))
		for _, c := range conns {
			rm := c.RemoteMultiaddr()
			if strings.Contains(rm.String(), "p2p-circuit") {
				circuits++
			}
			transport, _ := peer.SplitAddr(rm)
			if transport != nil {
				liveKeys[string(transport.Bytes())] = struct{}{}
			} else {
				liveKeys[string(rm.Bytes())] = struct{}{}
			}
		}
		fmt.Fprintf(w, "%s addrs=%d conns=%d circuits=%d\n",
			shortPeerID(p), len(addrs), len(conns), circuits)
		for _, a := range addrs {
			tag := classifyLive(a, liveKeys)
			fmt.Fprintf(w, "  [%s %s] %s\n", tag, addrClass(a), a.String())
		}
		for _, c := range conns {
			fmt.Fprintf(w, "  conn local=%s remote=%s streams=%d\n",
				c.LocalMultiaddr().String(), c.RemoteMultiaddr().String(),
				len(c.GetStreams()))
		}
	}
	if peerFilter != "" && matched == 0 {
		fmt.Fprintf(w, "WARNING: SHURLI_DIAG_PEER=%q matched zero peers.\n", peerFilter)
		fmt.Fprintln(w, "  Hint: the filter requires an exact peer ID match. Unset the env")
		fmt.Fprintln(w, "  var to dump all peers, or verify the peer ID is present in the")
		fmt.Fprintln(w, "  peerstore (the peer must have been known to the daemon).")
	}
}

// classifyLive returns "live" if addr a is one of the transport-only
// multiaddrs currently used by an active Conn to the same peer, else
// "stale". liveKeys is keyed by the byte-encoded multiaddr AFTER the
// /p2p/<id> suffix has been stripped — peer.SplitAddr normalises both
// sides so comparison is multiaddr-component-aware, not string-prefix.
//
// Addresses tagged "live" are in the ConnectedAddrTTL class (effectively
// infinite while the conn is up). Addresses tagged "stale" are in a
// finite TTL class (RecentlyConnected / Temp / Discovered). This is the
// coarsest honest classification the libp2p public API supports without
// reflection into unexported peerstore internals.
func classifyLive(a ma.Multiaddr, liveKeys map[string]struct{}) string {
	transport, _ := peer.SplitAddr(a)
	if transport == nil {
		transport = a
	}
	if _, ok := liveKeys[string(transport.Bytes())]; ok {
		return "live"
	}
	return "stale"
}

func addrClass(a ma.Multiaddr) string {
	s := a.String()
	switch {
	case strings.Contains(s, "p2p-circuit"):
		return "circuit"
	case strings.HasPrefix(s, "/ip4/127.") || strings.HasPrefix(s, "/ip6/::1"):
		return "loopback"
	case strings.HasPrefix(s, "/ip4/10.") ||
		strings.HasPrefix(s, "/ip4/192.168.") ||
		isPrivateIPv4Block172(s):
		return "lan4"
	case strings.HasPrefix(s, "/ip6/fe80"):
		return "ll6"
	case strings.HasPrefix(s, "/ip6/fc") || strings.HasPrefix(s, "/ip6/fd"):
		return "ula6"
	case strings.HasPrefix(s, "/ip4/"):
		return "pub4"
	case strings.HasPrefix(s, "/ip6/"):
		return "pub6"
	}
	return "other"
}

// isPrivateIPv4Block172 recognises 172.16.0.0/12 without enumerating every
// second octet by prefix — parses the second octet and range-checks.
func isPrivateIPv4Block172(s string) bool {
	const p = "/ip4/172."
	if !strings.HasPrefix(s, p) {
		return false
	}
	rest := s[len(p):]
	dot := strings.IndexByte(rest, '.')
	if dot <= 0 {
		return false
	}
	oct := rest[:dot]
	var n int
	for _, c := range []byte(oct) {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
		if n > 255 {
			return false
		}
	}
	return n >= 16 && n <= 31
}

func shortPeerID(p peer.ID) string {
	s := p.String()
	if len(s) > 14 {
		return s[:4] + ".." + s[len(s)-6:]
	}
	return s
}

// peerstoreLike is the minimal subset of the libp2p peerstore interface
// that diagSnapshot needs. It exists to keep the signature decoupled from
// the heavier peerstore.Peerstore type for testing.
type peerstoreLike interface {
	PeersWithAddrs() peer.IDSlice
	Addrs(peer.ID) []ma.Multiaddr
}

