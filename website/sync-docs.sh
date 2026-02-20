#!/usr/bin/env bash
# sync-docs.sh — Transforms docs/*.md into website/content/docs/ with Hugo front matter.
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
#   Use it → Explore it → Understand it → Trust it → Self-host → Automate it → Deep dive → Vision → Contribute → History
declare -A DOC_MAP
DOC_MAP=(
  # weight 1 = Quick Start (synced separately via sync_quickstart)
  ["NETWORK-TOOLS.md"]="network-tools.md:2:Network Tools"
  ["FAQ.md"]="faq.md:3:FAQ"
  # weight 4 = Trust & Security (standalone page, not synced from docs/)
  # weight 5 = Relay Setup (synced separately via sync_relay_setup)
  ["DAEMON-API.md"]="daemon-api.md:6:Daemon API"
  ["ARCHITECTURE.md"]="architecture.md:7:Architecture"
  ["ROADMAP.md"]="roadmap.md:8:Roadmap"
  ["TESTING.md"]="testing.md:9:Testing"
  ["ENGINEERING-JOURNAL.md"]="engineering-journal.md:10:Engineering Journal"
)

sync_doc() {
  local src_file="$1"
  local out_file="$2"
  local weight="$3"
  local title="$4"
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
    # Replace (FILENAME.md) with (../slug/)
    body="$(echo "$body" | sed "s|(\(${map_src}\))|../${map_slug}/|g")"
    # Also handle [text](FILENAME.md) pattern
    body="$(echo "$body" | sed "s|(${map_src})|(../${map_slug}/)|g")"
  done

  # Rewrite image paths for Hugo: images/foo.svg -> /images/docs/foo.svg
  body="$(echo "$body" | sed 's|](images/|](/images/docs/|g')"

  # Rewrite relay-server/README.md to website docs page with friendly link text
  body="$(echo "$body" | sed 's|\[relay-server/README.md\](../relay-server/README.md)|[Relay Setup guide](../relay-setup/)|g')"

  # Rewrite remaining relative source file references to GitHub URLs
  # e.g., (../cmd/peerup/...) -> (https://github.com/.../cmd/peerup/...)
  body="$(echo "$body" | sed "s|(\.\./\(cmd/\)|($GITHUB_BASE/\1|g")"
  body="$(echo "$body" | sed "s|(\.\./\(pkg/\)|($GITHUB_BASE/\1|g")"
  body="$(echo "$body" | sed "s|(\.\./\(internal/\)|($GITHUB_BASE/\1|g")"
  body="$(echo "$body" | sed "s|(\.\./\(relay-server/\)|($GITHUB_BASE/\1|g")"
  body="$(echo "$body" | sed "s|(\.\./\(deploy/\)|($GITHUB_BASE/\1|g")"
  body="$(echo "$body" | sed "s|(\.\./\(test/\)|($GITHUB_BASE/\1|g")"

  # Write output with Hugo front matter
  cat > "$dst_path" << FRONTMATTER
---
title: "${title}"
weight: ${weight}
---
<!-- Auto-synced from docs/${src_basename} by sync-docs.sh — do not edit directly -->

${body}
FRONTMATTER

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

  # Rewrite relay-server/README.md to website docs page with friendly link text
  content="$(echo "$content" | sed 's|\[relay-server/README.md\](relay-server/README.md)|[Relay Setup guide](../relay-setup/)|g')"

  # Rewrite remaining relative repo links to GitHub URLs
  # README links are root-relative: (relay-server/...), (docs/...), (configs/...), (deploy/...)
  for dir_prefix in relay-server/ docs/ configs/ deploy/ cmd/ pkg/ internal/ test/; do
    content="$(echo "$content" | sed "s|(${dir_prefix}|(${GITHUB_BASE}/${dir_prefix}|g")"
  done

  cat > "$dst_path" << FRONTMATTER
---
title: "Quick Start"
weight: 1
---
<!-- Auto-synced from README.md by sync-docs.sh — do not edit directly -->

${content}
FRONTMATTER

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

  cat > "$dst_path" << FRONTMATTER
---
title: "Relay Setup"
weight: 5
---
<!-- Auto-synced from relay-server/README.md by sync-docs.sh — do not edit directly -->

${body}
FRONTMATTER

  echo "  SYNC relay-server/README.md -> relay-setup.md"
}
sync_relay_setup

# Generate llms-full.txt — single-file concatenation of all docs for AI agents.
# Follows the llmstxt.org spec: one fetch gets everything.
# Order: README (overview) → docs in user-journey order.
generate_llms_full() {
  local llms_full="$SCRIPT_DIR/static/llms-full.txt"
  local readme="$(dirname "$SCRIPT_DIR")/README.md"

  # Doc files in user-journey order (matches sidebar weights)
  local -a doc_order=(
    "NETWORK-TOOLS.md"
    "FAQ.md"
    "DAEMON-API.md"
    "ARCHITECTURE.md"
    "ROADMAP.md"
    "TESTING.md"
    "ENGINEERING-JOURNAL.md"
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

    # Append relay setup guide
    if [[ -f "$relay_readme" ]]; then
      cat "$relay_readme"
      printf '\n\n---\n\n'
    fi
  } > "$llms_full"

  echo "  SYNC llms-full.txt ($(wc -c < "$llms_full" | tr -d ' ') bytes)"
}

generate_llms_full

echo "Done. $(ls "$OUT_DIR"/*.md 2>/dev/null | wc -l | tr -d ' ') files synced."
