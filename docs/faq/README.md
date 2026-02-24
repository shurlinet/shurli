# peer-up FAQ

Frequently asked questions about peer-up, organized by topic. Each section covers a different aspect of the project - from design philosophy to technical internals.

> **Note on comparisons**: All technical comparisons in this document are based on publicly available documentation, specifications, and published benchmarks as of the date listed at the bottom. Software evolves - details may be outdated by the time you read this. If you spot an inaccuracy, corrections are welcome via [GitHub issues](https://github.com/satindergrewal/peer-up/issues) or pull requests.

## Sections

| Section | What it covers |
|---------|---------------|
| [Design Philosophy](design-philosophy.md) | Why peer-up uses no accounts, no central servers, and no vendor dependencies. The reasoning behind self-sovereign identity and local-first design. |
| [Comparisons](comparisons.md) | How different approaches to remote access compare: centralized VPNs, P2P mesh tools, relay architectures, and blockchain P2P stacks. Where peer-up's design sits in the landscape. |
| [Relay & NAT Traversal](relay-and-nat.md) | How connections actually work: Circuit Relay v2, hole-punching, symmetric NAT, public vs self-hosted relays, and running your home node as a relay. |
| [Security & Features](security-and-features.md) | How pairing, verification, reachability grading, encrypted invites, and private DHT networks work. |
| [Technical Deep Dives](technical-deep-dives.md) | Under-the-hood details: libp2p improvements peer-up has adopted, emerging technologies to watch, and the Go vs Rust trade-off. |

**Last Updated**: 2026-02-24
