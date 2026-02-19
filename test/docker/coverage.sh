#!/bin/bash
# coverage.sh — Run unit + Docker integration tests and merge coverage data.
#
# Usage:
#   ./test/docker/coverage.sh          # Full run: unit + Docker + merge
#   ./test/docker/coverage.sh --unit   # Unit tests only (fast)
#
# Requires: Docker running, go 1.20+
#
# Output:
#   coverage/combined.out  — merged coverage profile (text format)
#   Final line: total coverage percentage

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
COV="$ROOT/coverage"

rm -rf "$COV"
mkdir -p "$COV/unit" "$COV/integration" "$COV/merged"

echo "=== Unit tests (with covdata) ==="
go test -cover "$ROOT/..." -args -test.gocoverdir="$COV/unit"

if [[ "${1:-}" == "--unit" ]]; then
    echo
    echo "=== Unit coverage only ==="
    go tool covdata textfmt -i="$COV/unit" -o="$COV/combined.out"
    go tool cover -func="$COV/combined.out" | tail -1
    exit 0
fi

echo
echo "=== Docker integration tests (with covdata) ==="
PEERUP_COVDIR="$COV/integration" go test -tags integration -count=1 -timeout 300s "$ROOT/test/docker/"

echo
echo "=== Merging coverage ==="
go tool covdata merge -i="$COV/unit,$COV/integration" -o="$COV/merged"
go tool covdata textfmt -i="$COV/merged" -o="$COV/combined.out"

echo
echo "=== Per-package coverage ==="
go tool covdata percent -i="$COV/merged"

echo
echo "=== Total ==="
go tool cover -func="$COV/combined.out" | tail -1
