package main

const githubBase = "https://github.com/satindergrewal/peer-up/blob/main"

// docEntry describes a source doc file to sync into website/content/docs/.
type docEntry struct {
	Source      string // Filename in docs/ (e.g., "FAQ.md")
	Output      string // Output filename (e.g., "faq.md")
	Weight      int
	Title       string
	Description string
}

// journalEntry describes an engineering journal file to sync.
type journalEntry struct {
	Source      string // Filename in docs/engineering-journal/
	Weight      int
	Title       string
	Description string
}

// Ordered by sidebar weight. Order also determines llms-full.txt concatenation.
var docEntries = []docEntry{
	{"NETWORK-TOOLS.md", "network-tools.md", 2, "Network Tools", "P2P network diagnostic commands: ping, traceroute, and resolve. Works standalone or through the daemon API."},
	{"FAQ.md", "faq.md", 3, "FAQ", "How peer-up compares to Tailscale and ZeroTier, how NAT traversal works, the security model, and troubleshooting common issues."},
	{"MONITORING.md", "monitoring.md", 6, "Monitoring", "Set up Prometheus and Grafana to visualize peer-up metrics. Pre-built dashboard, PromQL examples, audit logging, and alerting rules."},
	{"DAEMON-API.md", "daemon-api.md", 7, "Daemon API", "REST API reference for the peer-up daemon. Unix socket endpoints for managing peers, services, proxies, ping, traceroute, and more."},
	{"ARCHITECTURE.md", "architecture.md", 8, "Architecture", "Technical architecture of peer-up: libp2p foundation, circuit relay v2, DHT peer discovery, daemon design, connection gating, and naming system."},
	{"ROADMAP.md", "roadmap.md", 9, "Roadmap", "Multi-phase development roadmap for peer-up. From NAT traversal tool to decentralized P2P network infrastructure."},
	{"TESTING.md", "testing.md", 10, "Testing", "Test strategy for peer-up: unit tests, Docker integration tests, coverage targets, and CI pipeline configuration."},
}

// Ordered by weight. README.md becomes _index.md.
var journalEntries = []journalEntry{
	{"README.md", 12, "Engineering Journal", "Architecture Decision Records for peer-up. The why behind every significant design choice."},
	{"core-architecture.md", 2, "Core Architecture", "Foundational technology choices: Go, libp2p, private DHT, circuit relay v2, connection gating, single binary."},
	{"batch-a-reliability.md", 3, "Batch A - Reliability", "Timeouts, retries, DHT in the proxy path, and in-process integration tests."},
	{"batch-b-code-quality.md", 4, "Batch B - Code Quality", "Relay address deduplication, structured logging, sentinel errors, build version embedding."},
	{"batch-c-self-healing.md", 5, "Batch C - Self-Healing", "Config archive and rollback, commit-confirmed pattern, watchdog with pure-Go sd_notify."},
	{"batch-d-libp2p-features.md", 6, "Batch D - libp2p Features", "AutoNAT v2, QUIC transport ordering, Identify UserAgent, smart dialing."},
	{"batch-e-new-capabilities.md", 7, "Batch E - New Capabilities", "Relay health endpoint and headless invite/join for scripting."},
	{"batch-f-daemon-mode.md", 8, "Batch F - Daemon Mode", "Unix socket IPC, cookie authentication, RuntimeInfo interface, hot-reload authorized_keys."},
	{"batch-g-test-coverage.md", 9, "Batch G - Test Coverage", "Coverage-instrumented Docker tests, relay binary, injectable exit, post-phase audit protocol."},
	{"batch-h-observability.md", 10, "Batch H - Observability", "Prometheus metrics, nil-safe observability pattern, auth decision callback."},
	{"pre-batch-i.md", 11, "Pre-Batch I", "Makefile and build tooling, PAKE-secured invite/join, private DHT namespace isolation."},
	{"batch-i-adaptive-path.md", 12, "Batch I - Adaptive Path Selection", "Interface discovery, parallel dial racing, path quality tracking, network change monitoring, STUN hole-punching, every-peer-is-a-relay."},
	{"dev-tooling.md", 13, "Dev Tooling", "Go doc sync pipeline replacing fragile bash/sed, relay setup subcommand replacing bash config generation."},
}

// Quick-start metadata (extracted from README.md, not from docs/).
var quickStartMeta = docEntry{
	Source:      "README.md",
	Output:      "quick-start.md",
	Weight:      1,
	Title:       "Quick Start",
	Description: "Get two devices connected with peer-up in 60 seconds. Build from source, init, invite, join, and proxy any TCP service through an encrypted P2P tunnel.",
}

// Relay setup metadata (from relay-server/README.md).
var relaySetupMeta = docEntry{
	Source:      "relay-server/README.md",
	Output:      "relay-setup.md",
	Weight:      5,
	Title:       "Relay Setup",
	Description: "Complete guide to deploying your own peer-up relay server on a VPS. Ubuntu setup, systemd service, firewall rules, and health checks.",
}

// Directories whose relative links (../dir/) get rewritten to GitHub URLs.
var githubSourceDirs = []string{
	"cmd/", "pkg/", "internal/", "relay-server/", "deploy/", "test/", ".github/",
}

// Directories for quick-start root-relative links (dir/ without ../).
var quickStartLinkDirs = []string{
	"relay-server/", "docs/", "configs/", "deploy/", "cmd/", "pkg/", "internal/", "test/",
}
