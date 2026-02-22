#!/usr/bin/env bash
# sync-docs.sh  - Transforms docs/*.md into website/content/docs/ with Hugo front matter.
#
# This script is idempotent: running it twice produces identical output.
# It runs before every Hugo build (locally and in CI).
#
# Usage:
#   cd website && bash sync-docs.sh
#   cd website && bash sync-docs.sh && hugo server

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DOCS_DIR="$(dirname "$SCRIPT_DIR")/docs"
OUT_DIR="$SCRIPT_DIR/content/docs"

# Ensure output directory exists
mkdir -p "$OUT_DIR"

# Sync doc images (docs/images/ -> website/static/images/docs/)
if [[ -d "$DOCS_DIR/images" ]]; then
  mkdir -p "$SCRIPT_DIR/static/images/docs"
  cp -r "$DOCS_DIR/images/"* "$SCRIPT_DIR/static/images/docs/"
  echo "  SYNC docs/images/ -> website/static/images/docs/"
fi

# GitHub repo base URL for linking to source files
GITHUB_BASE="https://github.com/satindergrewal/peer-up/blob/main"

# Map: source filename -> output filename:weight:title
# Order follows the user journey (Jobs/Musk/Satinder principles):
#   Use it -> Explore it -> Understand it -> Trust it -> Self-host -> Automate it -> Deep dive -> Vision -> Contribute -> History
declare -A DOC_MAP
DOC_MAP=(
  # weight 1 = Quick Start (synced separately via sync_quickstart)
  ["NETWORK-TOOLS.md"]="network-tools.md:2:Network Tools"
  ["FAQ.md"]="faq.md:3:FAQ"
  # weight 4 = Trust & Security (standalone page, not synced from docs/)
  # weight 5 = Relay Setup (synced separately via sync_relay_setup)
  ["MONITORING.md"]="monitoring.md:6:Monitoring"
  ["DAEMON-API.md"]="daemon-api.md:7:Daemon API"
  ["ARCHITECTURE.md"]="architecture.md:8:Architecture"
  ["ROADMAP.md"]="roadmap.md:9:Roadmap"
  ["TESTING.md"]="testing.md:10:Testing"
  # Engineering Journal is synced separately (per-batch directory)
)

# SEO descriptions for each synced page
declare -A DESC_MAP
DESC_MAP=(
  ["NETWORK-TOOLS.md"]="P2P network diagnostic commands: ping, traceroute, and resolve. Works standalone or through the daemon API."
  ["FAQ.md"]="How peer-up compares to Tailscale and ZeroTier, how NAT traversal works, the security model, and troubleshooting common issues."
  ["MONITORING.md"]="Set up Prometheus and Grafana to visualize peer-up metrics. Pre-built dashboard, PromQL examples, audit logging, and alerting rules."
  ["DAEMON-API.md"]="REST API reference for the peer-up daemon. Unix socket endpoints for managing peers, services, proxies, ping, traceroute, and more."
  ["ARCHITECTURE.md"]="Technical architecture of peer-up: libp2p foundation, circuit relay v2, DHT peer discovery, daemon design, connection gating, and naming system."
  ["ROADMAP.md"]="Multi-phase development roadmap for peer-up. From NAT traversal tool to decentralized P2P network infrastructure."
  ["TESTING.md"]="Test strategy for peer-up: unit tests, Docker integration tests, coverage targets, and CI pipeline configuration."
)

sync_doc() {
  local src_file="$1"
  local out_file="$2"
  local weight="$3"
  local title="$4"
  local description="${DESC_MAP[$src_file]:-}"
  local src_basename
  src_basename="$(basename "$src_file")"

  local src_path="$DOCS_DIR/$src_file"
  local dst_path="$OUT_DIR/$out_file"

  if [[ ! -f "$src_path" ]]; then
    echo "  SKIP $src_file (not found)"
    return
  fi

  # Read the source file, strip the first # heading (Hugo renders title from front matter)
  local content
  content="$(cat "$src_path")"

  # Remove the first line if it starts with "# " (the title heading)
  local body
  body="$(echo "$content" | sed '1{/^# /d;}')"

  # Rewrite internal doc links: [TEXT](FILENAME.md) -> [TEXT](../lowercase-name/)
  # Only matches links to files in the DOC_MAP
  for map_src in "${!DOC_MAP[@]}"; do
    IFS=':' read -r map_out _ _ <<< "${DOC_MAP[$map_src]}"
    local map_slug="${map_out%.md}"
    # Replace (FILENAME.md) and (FILENAME.md#anchor) with (../slug/) and (../slug/#anchor)
    body="$(echo "$body" | sed "s|(${map_src}#\([^)]*\))|(../${map_slug}/#\1)|g")"
    body="$(echo "$body" | sed "s|(${map_src})|(../${map_slug}/)|g")"
  done

  # Rewrite image paths for Hugo: images/foo.svg -> /images/docs/foo.svg
  body="$(echo "$body" | sed 's|](images/|](/images/docs/|g')"

  # Rewrite relay-server/README.md to website docs page with friendly link text
  body="$(echo "$body" | sed 's|\[relay-server/README.md\](../relay-server/README.md)|[Relay Setup guide](../relay-setup/)|g')"

  # Rewrite ENGINEERING-JOURNAL.md to website engineering-journal section
  body="$(echo "$body" | sed 's|(ENGINEERING-JOURNAL.md)|(../engineering-journal/)|g')"

  # Rewrite remaining relative source file references to GitHub URLs
  # e.g., (../cmd/peerup/...) -> (https://github.com/.../cmd/peerup/...)
  body="$(echo "$body" | sed "s|(\.\./\(cmd/\)|($GITHUB_BASE/\1|g")"
  body="$(echo "$body" | sed "s|(\.\./\(pkg/\)|($GITHUB_BASE/\1|g")"
  body="$(echo "$body" | sed "s|(\.\./\(internal/\)|($GITHUB_BASE/\1|g")"
  body="$(echo "$body" | sed "s|(\.\./\(relay-server/\)|($GITHUB_BASE/\1|g")"
  body="$(echo "$body" | sed "s|(\.\./\(deploy/\)|($GITHUB_BASE/\1|g")"
  body="$(echo "$body" | sed "s|(\.\./\(test/\)|($GITHUB_BASE/\1|g")"
  body="$(echo "$body" | sed "s|(\.\./\(\.github/\)|($GITHUB_BASE/\1|g")"

  # Write output with Hugo front matter
  {
    echo "---"
    echo "title: \"${title}\""
    echo "weight: ${weight}"
    if [[ -n "$description" ]]; then
      echo "description: \"${description}\""
    fi
    echo "---"
    echo "<!-- Auto-synced from docs/${src_basename} by sync-docs.sh  - do not edit directly -->"
    echo ""
    echo "${body}"
  } > "$dst_path"

  echo "  SYNC $src_file -> $out_file"
}

# Quick-start page extracted from README.md (## Quick Start section)
sync_quickstart() {
  local readme="$(dirname "$SCRIPT_DIR")/README.md"
  local dst_path="$OUT_DIR/quick-start.md"

  if [[ ! -f "$readme" ]]; then
    echo "  SKIP quick-start (README.md not found)"
    return
  fi

  # Extract content between "## Quick Start" and the next "## " heading
  local in_section=0
  local content=""

  while IFS= read -r line; do
    if [[ "$line" == "## Quick Start"* ]]; then
      in_section=1
      continue
    fi
    if [[ $in_section -eq 1 && "$line" == "## "* ]]; then
      break
    fi
    if [[ $in_section -eq 1 ]]; then
      content+="$line"$'\n'
    fi
  done < "$readme"

  # Promote ### headings to ## (since the extracted section lost its parent ## heading)
  content="$(echo "$content" | sed 's/^### /## /')"

  # Rewrite relay-server/README.md to website docs page with friendly link text
  content="$(echo "$content" | sed 's|\[relay-server/README.md\](relay-server/README.md)|[Relay Setup guide](../relay-setup/)|g')"

  # Rewrite remaining relative repo links to GitHub URLs
  # README links are root-relative: (relay-server/...), (docs/...), (configs/...), (deploy/...)
  for dir_prefix in relay-server/ docs/ configs/ deploy/ cmd/ pkg/ internal/ test/; do
    content="$(echo "$content" | sed "s|(${dir_prefix}|(${GITHUB_BASE}/${dir_prefix}|g")"
  done

  {
    echo "---"
    echo 'title: "Quick Start"'
    echo "weight: 1"
    echo 'description: "Get two devices connected with peer-up in 60 seconds. Build from source, init, invite, join, and proxy any TCP service through an encrypted P2P tunnel."'
    echo "---"
    echo "<!-- Auto-synced from README.md by sync-docs.sh  - do not edit directly -->"
    echo ""
    echo "${content}"
  } > "$dst_path"

  echo "  SYNC README.md -> quick-start.md"
}

echo "Syncing docs/ -> website/content/docs/"

# Sync all mapped docs
for src_file in "${!DOC_MAP[@]}"; do
  IFS=':' read -r out_file weight title <<< "${DOC_MAP[$src_file]}"
  sync_doc "$src_file" "$out_file" "$weight" "$title"
done

# Sync quick-start from README
sync_quickstart

# Relay setup page from relay-server/README.md
sync_relay_setup() {
  local src_path="$(dirname "$SCRIPT_DIR")/relay-server/README.md"
  local dst_path="$OUT_DIR/relay-setup.md"

  if [[ ! -f "$src_path" ]]; then
    echo "  SKIP relay-setup (relay-server/README.md not found)"
    return
  fi

  local content
  content="$(cat "$src_path")"

  # Remove the first line if it starts with "# " (the title heading)
  local body
  body="$(echo "$content" | sed '1{/^# /d;}')"

  {
    echo "---"
    echo 'title: "Relay Setup"'
    echo "weight: 5"
    echo 'description: "Complete guide to deploying your own peer-up relay server on a VPS. Ubuntu setup, systemd service, firewall rules, and health checks."'
    echo "---"
    echo "<!-- Auto-synced from relay-server/README.md by sync-docs.sh  - do not edit directly -->"
    echo ""
    echo "${body}"
  } > "$dst_path"

  echo "  SYNC relay-server/README.md -> relay-setup.md"
}
sync_relay_setup

# Engineering Journal - synced as a directory (per-batch files)
sync_engineering_journal() {
  local journal_dir="$DOCS_DIR/engineering-journal"
  local out_journal_dir="$OUT_DIR/engineering-journal"

  if [[ ! -d "$journal_dir" ]]; then
    echo "  SKIP engineering-journal/ (not found)"
    return
  fi

  mkdir -p "$out_journal_dir"

  # Map: source filename -> weight:title:description
  declare -A JOURNAL_MAP
  JOURNAL_MAP=(
    ["README.md"]="12:Engineering Journal:Architecture Decision Records for peer-up. The why behind every significant design choice."
    ["core-architecture.md"]="2:Core Architecture:Foundational technology choices: Go, libp2p, private DHT, circuit relay v2, connection gating, single binary."
    ["batch-a-reliability.md"]="3:Batch A - Reliability:Timeouts, retries, DHT in the proxy path, and in-process integration tests."
    ["batch-b-code-quality.md"]="4:Batch B - Code Quality:Relay address deduplication, structured logging, sentinel errors, build version embedding."
    ["batch-c-self-healing.md"]="5:Batch C - Self-Healing:Config archive and rollback, commit-confirmed pattern, watchdog with pure-Go sd_notify."
    ["batch-d-libp2p-features.md"]="6:Batch D - libp2p Features:AutoNAT v2, QUIC transport ordering, Identify UserAgent, smart dialing."
    ["batch-e-new-capabilities.md"]="7:Batch E - New Capabilities:Relay health endpoint and headless invite/join for scripting."
    ["batch-f-daemon-mode.md"]="8:Batch F - Daemon Mode:Unix socket IPC, cookie authentication, RuntimeInfo interface, hot-reload authorized_keys."
    ["batch-g-test-coverage.md"]="9:Batch G - Test Coverage:Coverage-instrumented Docker tests, relay binary, injectable exit, post-phase audit protocol."
    ["batch-h-observability.md"]="10:Batch H - Observability:Prometheus metrics, nil-safe observability pattern, auth decision callback."
    ["pre-batch-i.md"]="11:Pre-Batch I:Makefile and build tooling, PAKE-secured invite/join, private DHT namespace isolation."
    ["batch-i-adaptive-path.md"]="12:Batch I - Adaptive Path Selection:Interface discovery, parallel dial racing, path quality tracking, network change monitoring, STUN hole-punching, every-peer-is-a-relay."
  )

  for src_file in "${!JOURNAL_MAP[@]}"; do
    IFS=':' read -r weight title description <<< "${JOURNAL_MAP[$src_file]}"
    local src_path="$journal_dir/$src_file"
    local dst_path="$out_journal_dir/$src_file"

    if [[ ! -f "$src_path" ]]; then
      echo "  SKIP engineering-journal/$src_file (not found)"
      continue
    fi

    # Read source, strip first # heading
    local body
    body="$(sed '1{/^# /d;}' "$src_path")"

    # Rewrite source code references to GitHub URLs
    body="$(echo "$body" | sed "s|\`cmd/peerup/|\`$GITHUB_BASE/cmd/peerup/|g")"
    body="$(echo "$body" | sed "s|\`pkg/p2pnet/|\`$GITHUB_BASE/pkg/p2pnet/|g")"
    body="$(echo "$body" | sed "s|\`internal/|\`$GITHUB_BASE/internal/|g")"

    # Determine if this is the _index.md (README.md) or a regular page
    local out_name="$src_file"
    if [[ "$src_file" == "README.md" ]]; then
      out_name="_index.md"
      dst_path="$out_journal_dir/_index.md"
    fi

    {
      echo "---"
      echo "title: \"${title}\""
      echo "weight: ${weight}"
      if [[ -n "$description" ]]; then
        echo "description: \"${description}\""
      fi
      echo "---"
      echo "<!-- Auto-synced from docs/engineering-journal/${src_file} by sync-docs.sh - do not edit directly -->"
      echo ""
      echo "${body}"
    } > "$dst_path"

    echo "  SYNC engineering-journal/$src_file -> engineering-journal/$out_name"
  done
}
sync_engineering_journal


# Generate llms-full.txt  - single-file concatenation of all docs for AI agents.
# Follows the llmstxt.org spec: one fetch gets everything.
# Order: README (overview) → docs in user-journey order.
generate_llms_full() {
  local llms_full="$SCRIPT_DIR/static/llms-full.txt"
  local readme="$(dirname "$SCRIPT_DIR")/README.md"

  # Doc files in user-journey order (matches sidebar weights)
  local -a doc_order=(
    "NETWORK-TOOLS.md"
    "FAQ.md"
    "MONITORING.md"
    "DAEMON-API.md"
    "ARCHITECTURE.md"
    "ROADMAP.md"
    "TESTING.md"
  )

  # Engineering Journal per-batch files in order
  local -a journal_order=(
    "engineering-journal/README.md"
    "engineering-journal/core-architecture.md"
    "engineering-journal/batch-a-reliability.md"
    "engineering-journal/batch-b-code-quality.md"
    "engineering-journal/batch-c-self-healing.md"
    "engineering-journal/batch-d-libp2p-features.md"
    "engineering-journal/batch-e-new-capabilities.md"
    "engineering-journal/batch-f-daemon-mode.md"
    "engineering-journal/batch-g-test-coverage.md"
    "engineering-journal/batch-h-observability.md"
    "engineering-journal/pre-batch-i.md"
    "engineering-journal/batch-i-adaptive-path.md"
  )

  local relay_readme="$(dirname "$SCRIPT_DIR")/relay-server/README.md"

  {
    # Start with README as the overview
    if [[ -f "$readme" ]]; then
      cat "$readme"
      printf '\n\n---\n\n'
    fi

    # Append each doc with a separator
    for doc_file in "${doc_order[@]}"; do
      local doc_path="$DOCS_DIR/$doc_file"
      if [[ -f "$doc_path" ]]; then
        cat "$doc_path"
        printf '\n\n---\n\n'
      fi
    done

    # Append engineering journal (all per-batch files)
    for journal_file in "${journal_order[@]}"; do
      local journal_path="$DOCS_DIR/$journal_file"
      if [[ -f "$journal_path" ]]; then
        cat "$journal_path"
        printf '\n\n---\n\n'
      fi
    done

    # Append relay setup guide
    if [[ -f "$relay_readme" ]]; then
      cat "$relay_readme"
      printf '\n\n---\n\n'
    fi
  } > "$llms_full"

  echo "  SYNC llms-full.txt ($(wc -c < "$llms_full" | tr -d ' ') bytes)"
}

generate_llms_full

echo "Done. $(find "$OUT_DIR" -name '*.md' 2>/dev/null | wc -l | tr -d ' ') files synced."
