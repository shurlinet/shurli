---
title: "Phase 9 - Plugin Security Threat Analysis"
weight: 27
description: "43-vector threat analysis across 3 rounds. Trusted computing base, WASM host function API, supply chain defense, credential isolation, AI-era constraints."
---
<!-- Auto-synced from docs/engineering-journal/plugin-security-threat-analysis.md by sync-docs - do not edit directly -->


| | |
|---|---|
| **Date** | 2026-03-17 |
| **Status** | Complete (analysis). Mitigations applied to Layer 1. Layer 2/3 mitigations tracked for implementation. |
| **ADRs** | ADR-U01 to ADR-U08 |

43 attack vectors identified across three rounds of analysis. Every vector has a concrete mitigation strategy. Nothing deferred without a tracking note. This journal covers the architecture decisions that emerged from the analysis, not the individual vectors (those are documented in the full threat model).

---

### ADR-U01: Layer 1 Compiled-In Plugins Are the Trusted Computing Base

| | |
|---|---|
| **Date** | 2026-03-17 |
| **Status** | Accepted |

### Context

Layer 1 plugins run in the same Go process as the daemon. They have full access to the libp2p host, private keys, peer connections, and all secrets. This is not a bug to fix; it's a deliberate architectural boundary.

The threat analysis identified this as the single highest-impact vector: a malicious compiled-in plugin can impersonate the node, intercept all streams, poison DHT, and exfiltrate everything.

### Decision

Layer 1 compiled-in plugins are part of the Trusted Computing Base. Official code only. No third-party compiled-in plugins. The binary IS the trust boundary. Third-party plugins must use WASM (Layer 2) with sandboxing.

The `PluginContext` interface already enforces this: it provides scoped access (ConnectToPeer, OpenStream, RegisterHandler) rather than raw host access. But discipline, not enforcement, is the boundary for Layer 1.

### Consequences

- Layer 1 is simple, fast, and fully trusted
- All third-party extensibility deferred to Layer 2 WASM
- `shurli build --with` (Layer 1.5) explicitly documented as "you're compiling this into your kernel" trust decision

---

### ADR-U02: WASM Host Function API Is the Security Boundary

| | |
|---|---|
| **Date** | 2026-03-17 |
| **Status** | Accepted (design, not yet implemented) |

### Context

wazero's WASM sandbox is solid, but every host function exposed is a hole through the wall. If you expose `host_send_stream(peer_id, protocol, data)`, a malicious plugin can craft raw protocol messages to forge core Shurli protocols.

Real-world: V8 had CVE-2023-6699 (WASM type confusion), Wasmtime had CVE-2026-27572 (DoS from guest), Pyodide had CVE-2025-68668 (escape, CVSS 9.9).

### Decision

Design host functions like OS syscalls. Three rules:

1. **Protocol namespace enforcement**: WASM plugins can only register handlers under `/shurli/plugin/<plugin-name>/`. Never under `/shurli/` root.
2. **Narrow, typed, validated**: Every host function validates inputs against a schema. Never raw stream/socket access.
3. **Logged and rate-limited**: Every host function call counted per plugin per time window.

Spend 80% of Layer 2 security effort on host function design.

### Consequences

- Plugin capabilities are strictly bounded by what host functions expose
- Narrower API = less attack surface but also less functionality per plugin
- Performance overhead from per-call validation and logging (acceptable for WASM plugins)

---

### ADR-U03: Supply Chain Defense via Content-Addressed Storage

| | |
|---|---|
| **Date** | 2026-03-17 |
| **Status** | Accepted (design) |

### Context

Real incidents: PyPI `semantic-types` typosquat (Jan 2025), npm ShaiHulud worm (500+ packages via stolen tokens), VS Code `prettier-vscode-plus` impersonation (Nov 2025, 4 hours before takedown), Go `boltdb-go/bolt` backdoor on module proxy for 3 years.

### Decision

Four-layer defense:

1. **Ed25519 signatures** on .wasm binaries. Pin author public keys, not names.
2. **Content-addressed storage**: plugins identified by hash. `shurli plugin install sha256:abc123` is the safe path.
3. **No auto-update**. Updates require explicit user approval with permission diff.
4. **TOFU key pinning** (like SSH known_hosts): first install pins the author's key. Key change = refuse update + warn.

### Consequences

- Typosquatting mitigated: names are human convenience, hashes are the identity
- Update flow is manual and explicit (sovereignty preserved)
- Key rotation requires signing key-rotation message with OLD key endorsing NEW key

---

### ADR-U04: Decomposed Permission Model

| | |
|---|---|
| **Date** | 2026-03-17 |
| **Status** | Accepted (design) |

### Context

Broad permissions ("network", "filesystem") create confused deputy problems. A "weather widget" with "network" permission can enumerate all peers, map topology, and exfiltrate data via protocol messages.

### Decision

Fine-grained, scoped permissions:

- `network:peers` - communicate with Shurli peers only (plugin's protocol namespace)
- `network:external:<domain>` - reach specific external domains
- `filesystem.read:<path>` - scoped to specific directories
- `config.read` vs `config.read.sensitive` (separate grants)

DNS resolution only available with `network:external` permission for approved domains. The existing `PluginPolicy` transport bitmask pattern is the right foundation to extend.

### Consequences

- Plugin approval prompts show exact scopes, not summaries
- More granular permissions = more approval prompts (UX trade-off)
- Backward compatible with Layer 1's policy system

---

### ADR-U05: Plugin Lifecycle State Machine

| | |
|---|---|
| **Date** | 2026-03-17 |
| **Status** | Accepted (partially implemented in supervisor) |

### Context

Hot reload creates timing windows. During disable, old plugin's goroutines may still be running with permissions that should be revoked. During enable, protocol handlers may be registered before permission checks are initialized.

### Decision

Atomic state machine: `LOADING -> READY -> ACTIVE -> DRAINING -> STOPPED`.

- Plugins handle streams ONLY in `ACTIVE` state
- `DRAINING`: stop new streams, wait for in-progress (30s hard timeout), then forcibly cancel
- Permission check on EVERY host function call, not just init
- Atomic swap on reload: new plugin fully loaded before old handler deregistered

### Consequences

- No streams accepted before `ACTIVE`, no operations after `DRAINING`
- The supervisor's crash-detect + restart + backoff pattern (already built) aligns with this state machine
- Reload creates a brief overlap where both versions are in memory (bounded by load time)

---

### ADR-U06: Credential Isolation (Zero-Knowledge Plugin Boundary)

| | |
|---|---|
| **Date** | 2026-03-17 |
| **Status** | Accepted (implemented in PluginContext) |

### Context

Daemon credentials (auth cookie, Ed25519 private key, vault passphrase, macaroon root keys, ZKP proving key) must never be accessible to plugins. Even compiled-in ones should use the `PluginContext` interface, not direct access to daemon internals.

### Decision

- `PluginContext` methods provide scoped operations (connect, open stream, register handler) but never expose raw keys or credentials
- WASM plugins get opaque peer handles, not raw peer IDs
- Error messages from host functions use structured error codes, not strings (prevents metadata leaks via verbose libp2p errors)
- Plugin-triggered logs use per-session pseudonym maps for peer identifiers

### Consequences

- Plugins cannot impersonate the daemon even if compromised
- Error handling is less informative for debugging (acceptable trade-off)
- Existing `CredentialSet` isolation in PluginContext validates this approach

---

### ADR-U07: AI-Era Threat Mitigations (Layer 3 Design Constraints)

| | |
|---|---|
| **Date** | 2026-03-17 |
| **Status** | Accepted (design constraints for future Layer 3) |

### Context

Round 3 of the analysis covered 10 AI-emergent threats backed by real incidents: CodeBreaker (USENIX 2024), ShaiHulud npm worm (2025), Anthropic Sleeper Agents (2024), Kimwolf botnet (700k I2P Sybils, Feb 2026), MCP 30 CVEs in 60 days (Jan-Feb 2026).

### Decision

Three hard constraints for Layer 3 (AI agent plugin development):

1. **Treat all AI output as untrusted input**. Skills.md is untrusted. Generated code goes through the full security pipeline.
2. **Break propagation chains**. Plugins cannot install other plugins. Hard-coded in code, not a permission. `shurli plugin install` is human-only.
3. **Hard-coded action boundaries for AI agents**. Trust-modifying actions (adding peers, changing capabilities, installing plugins) always require human approval. Enforced in code, not prompts.

### Consequences

- Layer 3 AI agents are strictly bounded, even if the AI model is compromised
- Human remains in the loop for all trust-modifying decisions
- AI agents can operate nodes autonomously for routine operations (monitoring, reconnection, grant refresh) but not for security-critical changes

---

### ADR-U08: Registry Is Informational Only

| | |
|---|---|
| **Date** | 2026-03-17 |
| **Status** | Accepted (design) |

### Context

Plugin registries are high-value targets. Pidgin's plugin repository was compromised for 41 days serving DarkGate malware (Aug 2024). Terraform modules were trivially hijackable.

### Decision

If a registry is built, it is INFORMATIONAL ONLY:

- Does not host binaries. Binaries fetched from author's URL, verified against author's signing key
- Registry index signed with project keys. Public key embedded in binary
- 48-hour delay on new entries before appearing in search results
- Multi-party signing: 2-of-3 maintainer signatures to publish registry update
- Transparency log: every registry update appended to public log (Certificate Transparency model)

### Consequences

- Registry compromise doesn't directly compromise users (no binary hosting)
- Discovery is centralized but trust is distributed (author keys, not registry keys)
- 48-hour delay slows legitimate plugin publishing (acceptable for security)
