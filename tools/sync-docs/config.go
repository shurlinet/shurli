package main

const githubBase = "https://github.com/shurlinet/shurli/blob/main"

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
	// FAQ.md removed - FAQ is now split into sub-pages under docs/faq/.
	// Synced via faqEntries below, same pattern as journalEntries.
	{"MONITORING.md", "monitoring.md", 6, "Monitoring", "Set up Prometheus and Grafana to visualize Shurli metrics. Pre-built dashboard, PromQL examples, audit logging, and alerting rules."},
	{"DAEMON-API.md", "daemon-api.md", 7, "Daemon API", "REST API reference for the Shurli daemon. Unix socket endpoints for managing peers, services, proxies, ping, traceroute, and more."},
	{"ARCHITECTURE.md", "architecture.md", 8, "Architecture", "Technical architecture of Shurli: libp2p foundation, circuit relay v2, DHT peer discovery, daemon design, connection gating, and naming system."},
	// ROADMAP.md is excluded from auto-sync. The website splits it into 3 pages
	// under website/content/docs/roadmap/ (overview, completed, planned) for better
	// readability. The source docs/ROADMAP.md stays as one file for GitHub readers.
	// When ROADMAP.md changes, update the website pages manually.
	{"TESTING.md", "testing.md", 10, "Testing", "Test strategy for Shurli: unit tests, Docker integration tests, coverage targets, and CI pipeline configuration."},
}

// Ordered by weight. README.md becomes _index.md.
var journalEntries = []journalEntry{
	{"README.md", 12, "Engineering Journal", "Architecture Decision Records for Shurli. The why behind every significant design choice."},
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
	{"post-i-2-trust-and-delivery.md", 13, "Post-I-2 - Trust & Delivery", "Peer introduction delivery, HMAC group commitment, relay admin socket, SAS verification, reachability grades, sovereign interaction history."},
	{"pre-phase5-hardening.md", 14, "Pre-Phase 5 Hardening", "Startup race fix, stale address detection, systemd/launchd service deployment."},
	{"phase5-network-resilience.md", 15, "Phase 5 - Network Resilience", "Native mDNS via dns_sd.h, PeerManager lifecycle, stale connection cleanup, IPv6 path probing, mDNS LAN-first connect, relay-discard logic."},
	{"dev-tooling.md", 16, "Dev Tooling", "Go doc sync pipeline replacing fragile bash/sed, relay setup subcommand replacing bash config generation."},
}

// Quick-start metadata (extracted from README.md, not from docs/).
var quickStartMeta = docEntry{
	Source:      "README.md",
	Output:      "quick-start.md",
	Weight:      1,
	Title:       "Quick Start",
	Description: "Get two devices connected with Shurli in 60 seconds. Build from source, init, invite, join, and proxy any TCP service through an encrypted P2P tunnel.",
}

// Relay setup metadata (from relay-server/README.md).
var relaySetupMeta = docEntry{
	Source:      "relay-server/README.md",
	Output:      "relay-setup.md",
	Weight:      5,
	Title:       "Relay Setup",
	Description: "Complete guide to deploying your own Shurli relay server on a VPS. Ubuntu setup, systemd service, firewall rules, and health checks.",
}

// Ordered by weight. README.md becomes _index.md.
var faqEntries = []journalEntry{
	{"README.md", 3, "FAQ", "Frequently asked questions about Shurli, organized by topic."},
	{"design-philosophy.md", 1, "Design Philosophy", "Why Shurli uses no accounts, no central servers, and no vendor dependencies."},
	{"comparisons.md", 2, "Comparisons", "How different approaches to remote access compare: centralized VPNs, P2P mesh tools, relay architectures."},
	{"relay-and-nat.md", 3, "Relay & NAT Traversal", "How connections work: Circuit Relay v2, hole-punching, symmetric NAT, self-hosted relays."},
	{"security-and-features.md", 4, "Security & Features", "How pairing, verification, reachability grading, encrypted invites, and private DHT networks work."},
	{"technical-deep-dives.md", 5, "Technical Deep Dives", "libp2p improvements, emerging technologies, and the Go vs Rust trade-off."},
}

// linkOnlyEntries are docs excluded from auto-sync but still need link rewriting.
// Other docs reference these files (e.g., ROADMAP.md), so cross-doc links like
// (ROADMAP.md) must still resolve to (../roadmap/).
var linkOnlyEntries = []docEntry{
	{"ROADMAP.md", "roadmap", 0, "", ""},
	{"FAQ.md", "faq", 0, "", ""},
	// FAQ sub-pages (for cross-doc links from main docs, e.g. ARCHITECTURE.md)
	{"faq/comparisons.md", "faq/comparisons", 0, "", ""},
	{"faq/design-philosophy.md", "faq/design-philosophy", 0, "", ""},
	{"faq/relay-and-nat.md", "faq/relay-and-nat", 0, "", ""},
	{"faq/security-and-features.md", "faq/security-and-features", 0, "", ""},
	{"faq/technical-deep-dives.md", "faq/technical-deep-dives", 0, "", ""},
}

// Directories whose relative links (../dir/) get rewritten to GitHub URLs.
var githubSourceDirs = []string{
	"cmd/", "pkg/", "internal/", "relay-server/", "deploy/", "test/", ".github/",
}

// Directories for quick-start root-relative links (dir/ without ../).
var quickStartLinkDirs = []string{
	"relay-server/", "docs/", "configs/", "deploy/", "cmd/", "pkg/", "internal/", "test/",
}
