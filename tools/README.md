# Shurli Dev Tools

## Plugin Architecture Boundary Enforcement

Shurli enforces strict separation between the core SDK (`pkg/sdk/`) and plugins (`plugins/`). Plugin code must not leak into the SDK, and the SDK must not depend on plugins.

Three layers of enforcement catch violations at different stages:

### 1. Pre-commit Hook (`tools/git-hooks/pre-commit`)

Runs automatically before every `git commit`. Blocks commits that introduce new plugin boundary violations.

**Setup** (one-time, per clone -- local config only, does not affect other repos):
```bash
git config --local core.hooksPath tools/git-hooks
```

**What it checks:**
- Shell-based grep checks (protocol leaks, engine types, receiver methods)
- Go static analyzer (import graph violations, AST-level engine detection)

**If it blocks your commit:**
- Read the violation message -- it tells you exactly what's wrong
- Plugin-specific code belongs in `plugins/<name>/`, not in `pkg/sdk/`
- If the violation is a false positive, add an entry to `tools/known-boundary-violations.txt` (requires review)

### 2. CI Checks (`.github/workflows/ci.yml`)

Same checks run in CI on every push to `main`/`dev` and every PR. Catches anything that slips past local hooks.

### 3. Claude Code Hook (`.claude/hooks/postchange-reminder.sh`)

Fires after every file edit during Claude Code sessions. Provides context-appropriate reminders about plugin boundaries, test commands, and architecture rules.

---

## Config Files

### `tools/plugin-engine-types.txt`

Lists all plugin-specific types, protocol constants, and grep patterns. Both the shell script and Go analyzer read from this file -- nothing is hardcoded.

**When to update:**
- When extracting a new plugin from `pkg/sdk/`: add its engine types here
- When completing a migration: remove the entries

**Format:**
```
type:MyEngineType          # Receiver methods on this type must not be in pkg/sdk/
const:MyProtocolConst      # This constant must not be defined in pkg/sdk/
grep:^type MyType          # Grep pattern for type declarations (shell script)
receiver:^func (x \*MyType)  # Grep pattern for receiver methods (shell script)
```

### `tools/known-boundary-violations.txt`

Suppression file for pre-existing violations. Lists files in `pkg/sdk/` that contain plugin-specific code awaiting migration.

**This file MUST be deleted** when all violations are migrated to `plugins/`.

Suppression is **per-file**: adding `pkg/sdk/transfer.go` suppresses violations in that file only. A new file `pkg/sdk/new_engine.go` will NOT be suppressed.

### `tools/check-plugin-boundary.sh`

Shell script that runs 6 grep-based checks. Used by pre-commit hook and CI.

### `tools/boundarycheck/`

Go static analyzer using `golang.org/x/tools/go/analysis`. Checks import graphs and AST-level patterns. Run via `go vet -vettool=<binary>`.

**Run manually:**
```bash
go build -o /tmp/bc ./tools/boundarycheck/cmd/boundarycheck
go vet -vettool=/tmp/bc ./plugins/... ./pkg/sdk/... ./pkg/plugin/... ./cmd/shurli/...
```

### `tools/importcheck/`

Go static analyzer that flags plugins importing forbidden `internal/` packages. Plugins may only import:
- `pkg/sdk/`
- `pkg/plugin/`
- `internal/config`, `internal/daemon`, `internal/termcolor` (Layer 1 only)

---

## Architecture Rules

| Rule | Enforced by |
|------|-------------|
| `pkg/sdk/` must not import `plugins/` | Go analyzer |
| `plugins/` must not import forbidden `internal/` | Go analyzer + importcheck |
| `cmd/shurli/` must not import `plugins/` except registration files | Go analyzer |
| No plugin engine receiver methods in `pkg/sdk/` | Go analyzer + shell script |
| No plugin protocol constants in `pkg/sdk/` | Go analyzer + shell script |
| No plugin protocol strings in `pkg/sdk/` | Shell script |

**Registration files** (allowed to import `plugins/`): `main.go`, `serve_common.go`, `cmd_daemon.go`.

---

## Other Tools

### `tools/sync-docs/`
Syncs `docs/` to `website/` with Hugo front matter and link rewriting. Dev-only, not in the binary.

### `tools/install.sh`
Production install script. Served at `get.shurli.io`.

### `tools/relay-setup.sh`
Relay server deployment script.
