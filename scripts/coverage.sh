#!/usr/bin/env bash
# Run Go coverage tool
# Usage: ./scripts/coverage.sh [detail]

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Build the coverage tool from its own module
COVERAGE_BIN=$(mktemp)
trap 'rm -f "$COVERAGE_BIN"' EXIT

cd "$SCRIPT_DIR/lib/coverage"
go build -o "$COVERAGE_BIN" .

# Run the coverage tool from the project root
cd "$PROJECT_ROOT"
"$COVERAGE_BIN" "$@"
