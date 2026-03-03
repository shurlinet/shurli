# Dev Tooling Decisions

Build system, documentation pipeline, and developer workflow tooling.

---

### ADR-DT01: Go Replaces Bash for Doc Sync Pipeline

**Context**: `website/sync-docs.sh` transformed `docs/*.md` into Hugo-ready website content with front matter injection, cross-reference link rewriting, and image path mapping. Over 3 iterations, the sed-based link rewriting broke in different ways: parenthesis stripping from markdown links (bash `\(\)` grouping conflict), anchored links like `(FAQ.md#section)` not matched by the plain `(FAQ.md)` pattern, and `.md` extensions not stripped for Hugo directory-style URLs. Each fix introduced new fragility because sed regex is context-free and markdown links are not.

**Alternatives considered**:
- **Fix the sed chains again** - The third round of fixes proved the pattern: every sed fix risks breaking an adjacent match. The bash script had grown to 377 lines with 20+ sed commands, many interacting.
- **Python/Node script** - Would work but adds a runtime dependency to a pure-Go project. Every contributor would need Python/Node installed.
- **Add to shurli binary as a subcommand** - Keeps it in Go but bloats the shipped binary with dev-only code. Users never run doc sync.

**Decision**: Standalone Go program in `tools/sync-docs/` (4 files, ~1033 lines). Every text transform is a pure `func(string) string` with zero I/O. Table-driven tests cover every transform independently. Run via `go run ./tools/sync-docs` or `make sync-docs`. Not compiled into the shurli binary. CI runs it during website builds.

Architecture:
- `config.go` - ordered slices of doc entries and journal entries (replaces bash associative arrays)
- `transforms.go` - 11 pure transform functions using `strings.ReplaceAll` and one compiled regex
- `transforms_test.go` - 12 test functions + integration test that creates a project in `t.TempDir()`
- `main.go` - flag parsing, file I/O orchestration, `--dry-run` support

**Consequences**: Link rewriting bugs become compiler errors or test failures instead of runtime surprises. Adding a new doc file means adding one struct literal to `config.go`. The `website` Makefile target auto-syncs before starting Hugo. Zero new dependencies.

**Reference**: [`tools/sync-docs/`](https://github.com/shurlinet/shurli/blob/main/tools/sync-docs/)

---

### ADR-DT02: Relay Setup as Go Subcommand (Replace Bash Section 6.5)

**Context**: `tools/relay-setup.sh` section 6.5 generated relay node configuration (192 lines of bash). It duplicated config YAML that already existed in Go's `config_template.go`, creating a maintenance burden where changes had to be made in two places.

**Alternatives considered**:
- **Keep bash, import template** - Bash cannot import Go templates. Would require generating a shared file format.
- **Separate Go binary in tools/** - Like sync-docs, but relay setup is user-facing (operators run it), not dev-only. Belongs in the main binary.

**Decision**: `shurli relay setup` subcommand with TimeMachine-style backup/restore. Reuses existing `config_template.go` for config generation. Setup.sh section 6.5 reduced from 192 lines to 3 lines (`shurli relay setup`). 18 new tests cover backup rotation, restore, config generation, and edge cases.

**Consequences**: Single source of truth for relay config. Operators get `--backup` and `--restore` flags for free. The bash script still handles system-level tasks (apt, systemd, firewall) where bash is the right tool.

**Reference**: [`cmd/shurli/cmd_relay.go`](https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_relay.go), [`tools/relay-setup.sh`](https://github.com/shurlinet/shurli/blob/main/tools/relay-setup.sh)

---

### ADR-DT03: Directory Consolidation (relay-server/ Eliminated)

**Context**: The `relay-server/` directory was a grab-bag: systemd service file, setup script, README, and a gitignored binary. It created confusion about where relay artifacts lived and duplicated the pattern already established by `deploy/` (service files) and `tools/` (scripts). Contributors had to look in three places to find relay-related files.

**Alternatives considered**:
- **Keep relay-server/, add symlinks** - Preserves backward compatibility but adds indirection. Symlinks confuse `go build` and some CI tools.
- **Rename to deploy/relay/** - Better grouping but creates a nested directory for three files.
- **Distribute files to existing directories** - Service file joins `deploy/`, setup script joins `tools/`, relay guide joins `docs/`. Each file goes where its type already lives. Delete `relay-server/` entirely.

**Decision**: Clean cut, no backward compatibility:

| Old location | New location |
|---|---|
| `relay-server/relay-server.service` | `deploy/shurli-relay.service` |
| `relay-server/setup.sh` | `tools/relay-setup.sh` |
| `relay-server/README.md` | `docs/RELAY-SETUP.md` |
| `relay-server/relay-server` (binary) | `/usr/local/bin/shurli` (system install) |

Simultaneously, the relay install moved from relative-path (`$RELAY_DIR/shurli`) to FHS-compliant system paths (`/usr/local/bin/shurli` + `/etc/shurli/relay/`). The Makefile gained relay targets (`install-relay`, `uninstall-relay`) alongside existing daemon targets.

The setup script delegates build and install to `make install-relay` instead of running `go build` inline. The service file uses fixed system paths (no sed placeholder substitution).

53+ cross-codebase references updated. Runtime identifiers (`relay-server/<version>` UserAgent, `relay-server.yaml` config filename) left unchanged - these are network protocol identifiers, not file paths.

**Consequences**: One directory deleted. Files live where their type lives. Contributors find service files in `deploy/`, scripts in `tools/`, docs in `docs/`. FHS-compliant installation makes the relay deployable like any standard Linux service. `make install-relay` handles the full lifecycle.

**Reference**: `deploy/shurli-relay.service`, `tools/relay-setup.sh`, `docs/RELAY-SETUP.md`, `Makefile`
