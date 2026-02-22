---
title: "Dev Tooling"
weight: 13
description: "Go doc sync pipeline replacing fragile bash/sed, relay setup subcommand replacing bash config generation."
---
<!-- Auto-synced from docs/engineering-journal/dev-tooling.md by sync-docs - do not edit directly -->


Build system, documentation pipeline, and developer workflow tooling.

---

### ADR-DT01: Go Replaces Bash for Doc Sync Pipeline

**Context**: `website/sync-docs.sh` transformed `docs/*.md` into Hugo-ready website content with front matter injection, cross-reference link rewriting, and image path mapping. Over 3 iterations, the sed-based link rewriting broke in different ways: parenthesis stripping from markdown links (bash `\(\)` grouping conflict), anchored links like `(FAQ.md#section)` not matched by the plain `(FAQ.md)` pattern, and `.md` extensions not stripped for Hugo directory-style URLs. Each fix introduced new fragility because sed regex is context-free and markdown links are not.

**Alternatives considered**:
- **Fix the sed chains again** - The third round of fixes proved the pattern: every sed fix risks breaking an adjacent match. The bash script had grown to 377 lines with 20+ sed commands, many interacting.
- **Python/Node script** - Would work but adds a runtime dependency to a pure-Go project. Every contributor would need Python/Node installed.
- **Add to peerup binary as a subcommand** - Keeps it in Go but bloats the shipped binary with dev-only code. Users never run doc sync.

**Decision**: Standalone Go program in `tools/sync-docs/` (4 files, ~1033 lines). Every text transform is a pure `func(string) string` with zero I/O. Table-driven tests cover every transform independently. Run via `go run ./tools/sync-docs` or `make sync-docs`. Not compiled into the peerup binary. CI runs it during website builds.

Architecture:
- `config.go` - ordered slices of doc entries and journal entries (replaces bash associative arrays)
- `transforms.go` - 11 pure transform functions using `strings.ReplaceAll` and one compiled regex
- `transforms_test.go` - 12 test functions + integration test that creates a project in `t.TempDir()`
- `main.go` - flag parsing, file I/O orchestration, `--dry-run` support

**Consequences**: Link rewriting bugs become compiler errors or test failures instead of runtime surprises. Adding a new doc file means adding one struct literal to `config.go`. The `website` Makefile target auto-syncs before starting Hugo. Zero new dependencies.

**Reference**: [`tools/sync-docs/`](https://github.com/satindergrewal/peer-up/blob/main/tools/sync-docs/)

---

### ADR-DT02: Relay Setup as Go Subcommand (Replace Bash Section 6.5)

**Context**: `relay-server/setup.sh` section 6.5 generated relay node configuration (192 lines of bash). It duplicated config YAML that already existed in Go's `config_template.go`, creating a maintenance burden where changes had to be made in two places.

**Alternatives considered**:
- **Keep bash, import template** - Bash cannot import Go templates. Would require generating a shared file format.
- **Separate Go binary in tools/** - Like sync-docs, but relay setup is user-facing (operators run it), not dev-only. Belongs in the main binary.

**Decision**: `peerup relay setup` subcommand with TimeMachine-style backup/restore. Reuses existing `config_template.go` for config generation. Setup.sh section 6.5 reduced from 192 lines to 3 lines (`peerup relay setup`). 18 new tests cover backup rotation, restore, config generation, and edge cases.

**Consequences**: Single source of truth for relay config. Operators get `--backup` and `--restore` flags for free. The bash script still handles system-level tasks (apt, systemd, firewall) where bash is the right tool.

**Reference**: [`https://github.com/satindergrewal/peer-up/blob/main/cmd/peerup/cmd_relay.go`](https://github.com/satindergrewal/peer-up/blob/main/cmd/peerup/cmd_relay.go), [`relay-server/setup.sh`](https://github.com/satindergrewal/peer-up/blob/main/relay-server/setup.sh)
