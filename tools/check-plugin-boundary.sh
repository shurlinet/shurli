#!/bin/bash
# tools/check-plugin-boundary.sh
# Generic plugin architecture boundary checker.
# Used by: .git/hooks/pre-commit AND .github/workflows/ci.yml
#
# Discovers ALL plugins dynamically from plugins/*/plugin.go.
# Checks that pkg/sdk/ does not contain plugin-specific code.
# Uses a suppression file for known pre-existing violations (Option A).
#
# Exit 0 = clean, Exit 1 = violations found.
# Compatible with bash 3+ (macOS default).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SUPPRESSION_FILE="$REPO_ROOT/tools/known-boundary-violations.txt"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

VIOLATIONS=0
SUPPRESSED=0

# Load suppression list into a flat string (one entry per line)
SUPPRESSION_LIST=""
if [[ -f "$SUPPRESSION_FILE" ]]; then
    while IFS= read -r line; do
        [[ "$line" =~ ^#.*$ || -z "$line" ]] && continue
        SUPPRESSION_LIST="${SUPPRESSION_LIST}${line}
"
    done < "$SUPPRESSION_FILE"
fi

is_suppressed() {
    local file_line="$1"
    local file_only="${file_line%%:*}"

    # Check file-level suppression (most common)
    if echo "$SUPPRESSION_LIST" | grep -qxF -- "$file_only"; then
        return 0
    fi
    # Check exact file:line match
    if echo "$SUPPRESSION_LIST" | grep -qxF -- "$file_line"; then
        return 0
    fi
    return 1
}

report_violation() {
    local severity="$1"
    local file_line="$2"
    local message="$3"

    if is_suppressed "$file_line"; then
        SUPPRESSED=$((SUPPRESSED + 1))
        return
    fi

    echo -e "${RED}VIOLATION${NC} [$severity] $file_line"
    echo "  $message"
    VIOLATIONS=$((VIOLATIONS + 1))
}

echo "=== Plugin Architecture Boundary Check ==="
echo ""

# --- CHECK 1: Discover plugins and their protocol strings ---
echo "Discovering plugins..."
PLUGIN_DIRS=""
PLUGIN_PROTOCOLS=""

for plugin_go in "$REPO_ROOT"/plugins/*/plugin.go; do
    [[ -f "$plugin_go" ]] || continue
    plugin_dir=$(dirname "$plugin_go")
    plugin_name=$(basename "$plugin_dir")
    PLUGIN_DIRS="${PLUGIN_DIRS}${plugin_name} "

    # Extract protocol names from plugin.Protocol{Name: "..."} declarations
    # and full /shurli/ protocol strings
    for go_file in "$plugin_dir"/*.go; do
        [[ -f "$go_file" ]] || continue
        [[ "$go_file" == *_test.go ]] && continue

        # Full protocol paths: "/shurli/file-transfer/2.0.0"
        while IFS= read -r proto; do
            proto_clean=$(echo "$proto" | tr -d '"' | tr -d '[:space:]')
            if [[ -n "$proto_clean" ]]; then
                if ! echo "$PLUGIN_PROTOCOLS" | grep -qF "${plugin_name}:${proto_clean}"; then
                    PLUGIN_PROTOCOLS="${PLUGIN_PROTOCOLS}${plugin_name}:${proto_clean}
"
                fi
            fi
        done < <(grep -ohE '"/shurli/[^"]*"' "$go_file" 2>/dev/null | tr -d '"' || true)

        # Short protocol names from plugin.Protocol{Name: "file-transfer", ...}
        # These get expanded to /shurli/file-transfer/VERSION by the framework
        # Only extract names that look like protocol IDs (contain a hyphen)
        while IFS= read -r proto; do
            if [[ -n "$proto" && "$proto" == *-* ]]; then
                if ! echo "$PLUGIN_PROTOCOLS" | grep -qF "${plugin_name}:${proto}"; then
                    PLUGIN_PROTOCOLS="${PLUGIN_PROTOCOLS}${plugin_name}:${proto}
"
                fi
            fi
        done < <(grep -oE 'Name:[[:space:]]*"[^"]*"' "$go_file" 2>/dev/null | grep -oE '"[^"]*"' | tr -d '"' || true)
    done
done

plugin_count=$(echo "$PLUGIN_DIRS" | wc -w | tr -d ' ')
proto_count=$(echo "$PLUGIN_PROTOCOLS" | grep -c . || echo 0)
echo "  Found $plugin_count plugin(s): $PLUGIN_DIRS"
echo "  Found $proto_count protocol(s)"
echo ""

# --- CHECK 2: Protocol strings must NOT appear in pkg/sdk/ ---
echo "Checking pkg/sdk/ for plugin protocol leaks..."
while IFS= read -r proto_entry; do
    [[ -z "$proto_entry" ]] && continue
    plugin_name="${proto_entry%%:*}"
    proto_string="${proto_entry#*:}"

    while IFS= read -r match; do
        [[ -z "$match" ]] && continue
        # Skip if the match only appears in a comment portion of the line
        line_content="${match#*:*:}"
        # Remove the comment portion and check if the protocol string still exists
        code_part="${line_content%%//*}"
        if [[ "$code_part" != *"$proto_string"* ]]; then
            continue
        fi
        rel_path="${match#$REPO_ROOT/}"
        report_violation "CRITICAL" "$rel_path" \
            "Protocol '$proto_string' belongs to plugin '$plugin_name' but is defined/referenced in pkg/sdk/."
    done < <(grep -rn "$proto_string" "$REPO_ROOT/pkg/sdk/"*.go 2>/dev/null | grep -v _test.go || true)
done <<< "$PLUGIN_PROTOCOLS"
echo ""

# --- Load engine types config (for CHECK 3 and CHECK 4) ---
ENGINE_TYPES_FILE="$REPO_ROOT/tools/plugin-engine-types.txt"
GREP_PATTERNS=""
RECEIVER_PATTERNS=""
if [[ -f "$ENGINE_TYPES_FILE" ]]; then
    while IFS= read -r cfgline; do
        [[ "$cfgline" =~ ^#.*$ || -z "$cfgline" ]] && continue
        if [[ "$cfgline" == grep:* ]]; then
            pattern="${cfgline#grep:}"
            if [[ -z "$GREP_PATTERNS" ]]; then
                GREP_PATTERNS="$pattern"
            else
                GREP_PATTERNS="${GREP_PATTERNS}\|${pattern}"
            fi
        elif [[ "$cfgline" == receiver:* ]]; then
            RECEIVER_PATTERNS="${RECEIVER_PATTERNS}${cfgline#receiver:}
"
        fi
    done < "$ENGINE_TYPES_FILE"
else
    echo "  WARNING: $ENGINE_TYPES_FILE not found. Skipping type/method checks."
fi

# --- CHECK 3: Plugin-specific types in pkg/sdk/ ---
echo "Checking pkg/sdk/ for plugin-specific types..."
if [[ -n "$GREP_PATTERNS" ]]; then
    while IFS= read -r match; do
        [[ -z "$match" ]] && continue
        rel_path="${match#$REPO_ROOT/}"
        type_name=$(echo "$match" | sed -n 's/.*type \([A-Z][a-zA-Z]*\).*/\1/p')
        report_violation "CRITICAL" "$rel_path" \
            "Type '$type_name' in pkg/sdk/ appears to be plugin-specific."
    done < <(grep -rn "$GREP_PATTERNS" \
        "$REPO_ROOT/pkg/sdk/"*.go 2>/dev/null | grep -v _test.go || true)
fi
echo ""

# --- CHECK 4: Plugin engine methods in pkg/sdk/ ---
echo "Checking pkg/sdk/ for plugin engine methods..."
while IFS= read -r pattern; do
    [[ -z "$pattern" ]] && continue
    while IFS= read -r match; do
        [[ -z "$match" ]] && continue
        rel_path="${match#$REPO_ROOT/}"
        method_name=$(echo "$match" | sed -n 's/.*) \([a-zA-Z]*\)(.*/\1/p')
        recv_type=$(echo "$pattern" | sed -n 's/.*\*\([A-Za-z]*\)).*/\1/p')
        report_violation "CRITICAL" "$rel_path" \
            "$recv_type method '$method_name' is plugin engine code living in SDK."
    done < <(grep -rn "$pattern" "$REPO_ROOT/pkg/sdk/"*.go 2>/dev/null | grep -v _test.go || true)
done <<< "$RECEIVER_PATTERNS"
echo ""

# --- CHECK 5: cmd/shurli/ must not import plugins/ directly ---
# Allowed registration files: main.go, serve_common.go, cmd_daemon.go
echo "Checking cmd/shurli/ for direct plugin imports..."
for go_file in "$REPO_ROOT"/cmd/shurli/*.go; do
    [[ -f "$go_file" ]] || continue
    [[ "$go_file" == *_test.go ]] && continue
    basename_file=$(basename "$go_file")
    # Registration files are allowed to import plugins
    case "$basename_file" in
        main.go|serve_common.go|cmd_daemon.go) continue ;;
    esac
    rel_file="cmd/shurli/$basename_file"
    while IFS= read -r match; do
        [[ -z "$match" ]] && continue
        line_num="${match%%:*}"
        report_violation "HIGH" "$rel_file:$line_num" \
            "cmd/shurli/$basename_file imports from plugins/ directly. Must delegate through plugin registry."
    done < <(grep -n 'shurlinet/shurli/plugins' "$go_file" 2>/dev/null || true)
done
echo ""

# --- CHECK 6: pkg/sdk/ must not import from plugins/ ---
echo "Checking pkg/sdk/ for reverse plugin imports..."
for go_file in "$REPO_ROOT"/pkg/sdk/*.go; do
    [[ -f "$go_file" ]] || continue
    [[ "$go_file" == *_test.go ]] && continue
    rel_file="pkg/sdk/$(basename "$go_file")"
    while IFS= read -r match; do
        [[ -z "$match" ]] && continue
        line_num="${match%%:*}"
        report_violation "CRITICAL" "$rel_file:$line_num" \
            "pkg/sdk/ imports from plugins/. SDK must NEVER depend on plugins."
    done < <(grep -n 'shurlinet/shurli/plugins' "$go_file" 2>/dev/null || true)
done
echo ""

# --- SUMMARY ---
echo "=== Summary ==="
if [[ $VIOLATIONS -gt 0 ]]; then
    echo -e "${RED}FAILED${NC}: $VIOLATIONS new violation(s) found."
    if [[ $SUPPRESSED -gt 0 ]]; then
        echo -e "${YELLOW}SUPPRESSED${NC}: $SUPPRESSED known pre-existing violation(s) (see tools/known-boundary-violations.txt)."
    fi
    echo ""
    echo "See: memory/plugin-architecture-audit-violations-2026-04-03.md"
    echo "Rule: Plugin extraction means the code MOVES (PROJECT-RULES.md)"
    exit 1
else
    if [[ $SUPPRESSED -gt 0 ]]; then
        echo -e "${GREEN}PASSED${NC} (with $SUPPRESSED suppressed known violation(s) pending migration)."
    else
        echo -e "${GREEN}PASSED${NC}: No plugin boundary violations found."
    fi
    exit 0
fi
