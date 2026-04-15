package main

import (
	"bytes"
	"net"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// TestDiagSnapshot_Smoke runs diagSnapshot against a minimal in-process
// libp2p host and asserts that every required section header is present.
// Catches wiring/import regressions and verifies the function does not
// panic on a fresh host with an empty peerstore.
func TestDiagSnapshot_Smoke(t *testing.T) {
	h, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.NoSecurity,
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("libp2p.New: %v", err)
	}
	defer h.Close()

	var buf bytes.Buffer
	diagSnapshot(h, &buf)
	out := buf.String()

	wantSections := []string{
		"=== diag ",
		"-- interfaces --",
		"-- /proc/self/limits --", // will print "unavailable" on macOS, still emits the header
		"-- /proc/self/status",
		"-- open file descriptors --",
		"-- /proc/net/sockstat --",
		"-- cgroup memory --",
		"-- rcmgr scopes --",
		"-- peerstore:",
		"=== end diag ===",
		"note: libp2p swarm dial backoff",
		"mtu=",   // writeInterfaces now emits mtu/flags even on loopback
		"flags=", // same
	}
	for _, want := range wantSections {
		if !strings.Contains(out, want) {
			t.Errorf("diagSnapshot output missing %q\n---output---\n%s", want, out)
		}
	}

	// rcmgr dump must show system + transient rows at minimum.
	for _, want := range []string{"system", "transient"} {
		if !strings.Contains(out, want) {
			t.Errorf("diagSnapshot rcmgr section missing %q", want)
		}
	}

	// Header must include our peer ID in short form.
	if !strings.Contains(out, "self=") {
		t.Errorf("diagSnapshot header missing self= field")
	}
}

func TestClassifyLive(t *testing.T) {
	mustMA := func(s string) ma.Multiaddr {
		m, err := ma.NewMultiaddr(s)
		if err != nil {
			t.Fatalf("bad multiaddr %q: %v", s, err)
		}
		return m
	}
	// Build liveKeys the way writePeerstore does: strip /p2p/<id> via
	// peer.SplitAddr then store transport.Bytes(). Uses a real valid peer
	// ID so the /p2p/... suffix parses cleanly.
	const validPID = "12D3KooWJyFvJu8WSqm8KqjvNnLFCXvsWtU5N8QkwLJ6nJ6PvXbG"
	liveKeys := map[string]struct{}{}
	addLive := func(s string) {
		m := mustMA(s)
		transport, _ := peer.SplitAddr(m)
		if transport == nil {
			transport = m
		}
		liveKeys[string(transport.Bytes())] = struct{}{}
	}
	addLive("/ip4/10.0.0.5/tcp/4001")
	addLive("/ip4/10.0.0.9/tcp/4001/p2p/" + validPID)

	tests := []struct {
		addr string
		want string
	}{
		{"/ip4/10.0.0.5/tcp/4001", "live"},              // exact transport match
		{"/ip4/10.0.0.9/tcp/4001", "live"},              // live side had /p2p/ suffix; stripped in both
		{"/ip4/10.0.0.9/tcp/4001/p2p/" + validPID, "live"}, // peerstore unlikely to have /p2p/ but must still match
		{"/ip4/192.168.1.10/tcp/4001", "stale"},
		{"/ip4/10.0.0.5/tcp/40", "stale"}, // CRITICAL: port 40 must NOT match live port 4001
		{"/ip4/10.0.0.5/udp/4001/quic-v1", "stale"},
		{"/ip4/10.0.0.5", "stale"}, // shorter than any live key; must not falsely prefix-match
	}
	for _, tc := range tests {
		got := classifyLive(mustMA(tc.addr), liveKeys)
		if got != tc.want {
			t.Errorf("classifyLive(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

func TestParseProcSelfCgroup(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "cgroup v2 unified",
			in:   "0::/system.slice/shurli-daemon.service\n",
			want: "/system.slice/shurli-daemon.service",
		},
		{
			name: "cgroup v1 memory controller",
			in: "12:memory:/user.slice/shurli\n" +
				"5:cpu,cpuacct:/user.slice/shurli\n",
			want: "/user.slice/shurli",
		},
		{
			name: "cgroup v1 memory in combined controllers",
			in: "8:cpu,memory,blkio:/system.slice/foo.service\n" +
				"3:pids:/system.slice/foo.service\n",
			want: "/system.slice/foo.service",
		},
		{
			name: "hybrid — v2 takes precedence over v1 memory",
			in: "0::/system.slice/shurli.service\n" +
				"12:memory:/legacy/path\n",
			want: "/system.slice/shurli.service",
		},
		{
			name: "no memory controller anywhere",
			in:   "5:cpu,cpuacct:/user.slice\n7:blkio:/user.slice\n",
			want: "",
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},
		{
			name: "malformed lines ignored",
			in:   "garbage\n0::/valid/path\nalso garbage\n",
			want: "/valid/path",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseProcSelfCgroup(strings.NewReader(tc.in))
			if err != nil {
				t.Fatalf("parseProcSelfCgroup: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIfaceFlagsString(t *testing.T) {
	tests := []struct {
		name  string
		flags net.Flags
		want  string
	}{
		{"up only", net.FlagUp, "up"},
		{"up+loopback+running", net.FlagUp | net.FlagLoopback | net.FlagRunning, "up,loopback,running"},
		{
			"utun-shape point-to-point",
			net.FlagUp | net.FlagPointToPoint | net.FlagMulticast | net.FlagRunning,
			"up,p2p,multicast,running",
		},
		{
			"ethernet-shape broadcast",
			net.FlagUp | net.FlagBroadcast | net.FlagMulticast | net.FlagRunning,
			"up,broadcast,multicast,running",
		},
		{"none", 0, "-"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ifaceFlagsString(tc.flags)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDiagSnapshot_PeerFilterZeroMatch(t *testing.T) {
	// Set an env var that matches no peer in the fresh host's peerstore,
	// then assert the warning surface makes the zero-match visible.
	t.Setenv("SHURLI_DIAG_PEER", "12D3KooWNoSuchPeer000000000000000000000000000000")
	h, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.NoSecurity,
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("libp2p.New: %v", err)
	}
	defer h.Close()

	var buf bytes.Buffer
	diagSnapshot(h, &buf)
	out := buf.String()

	for _, want := range []string{
		"filter=12D3KooWNoSuchPeer",
		"WARNING: SHURLI_DIAG_PEER=",
		"matched zero peers",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("zero-match warning missing %q\n---\n%s", want, out)
		}
	}
}

func TestDiagSnapshot_PrivacyHeader(t *testing.T) {
	h, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.NoSecurity,
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("libp2p.New: %v", err)
	}
	defer h.Close()

	var buf bytes.Buffer
	diagSnapshot(h, &buf)
	out := buf.String()

	wantLines := []string{
		"# PRIVATE",
		"Do NOT commit",
		"Local diagnostic only",
	}
	for _, want := range wantLines {
		if !strings.Contains(out, want) {
			t.Errorf("diagSnapshot output missing privacy warning line %q", want)
		}
	}
}

func TestAddrClass(t *testing.T) {
	mustMA := func(s string) ma.Multiaddr {
		m, err := ma.NewMultiaddr(s)
		if err != nil {
			t.Fatalf("bad multiaddr %q: %v", s, err)
		}
		return m
	}
	tests := []struct {
		addr string
		want string
	}{
		{"/ip4/127.0.0.1/tcp/4001", "loopback"},
		{"/ip6/::1/tcp/4001", "loopback"},
		{"/ip4/10.0.0.5/tcp/4001", "lan4"},
		{"/ip4/192.168.1.1/tcp/4001", "lan4"},
		{"/ip4/172.16.0.1/tcp/4001", "lan4"},
		{"/ip4/172.20.0.1/tcp/4001", "lan4"},
		{"/ip4/172.31.255.255/tcp/4001", "lan4"},
		{"/ip4/172.32.0.1/tcp/4001", "pub4"}, // out of 172.16/12
		{"/ip4/172.15.0.1/tcp/4001", "pub4"}, // out of 172.16/12
		{"/ip4/203.0.113.7/tcp/4001", "pub4"},
		{"/ip6/fe80::1/tcp/4001", "ll6"},
		{"/ip6/fd00::1/tcp/4001", "ula6"},
		{"/ip6/2001:db8::1/tcp/4001", "pub6"},
	}
	for _, tc := range tests {
		got := addrClass(mustMA(tc.addr))
		if got != tc.want {
			t.Errorf("addrClass(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

func TestIsPrivateIPv4Block172(t *testing.T) {
	yes := []string{"/ip4/172.16.0.0", "/ip4/172.31.255.255", "/ip4/172.20.1.1"}
	no := []string{"/ip4/172.15.0.0", "/ip4/172.32.0.0", "/ip4/172.a.0.0", "/ip4/172..0.0", "/ip4/17.16.0.0"}
	for _, s := range yes {
		if !isPrivateIPv4Block172(s) {
			t.Errorf("isPrivateIPv4Block172(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if isPrivateIPv4Block172(s) {
			t.Errorf("isPrivateIPv4Block172(%q) = true, want false", s)
		}
	}
}
