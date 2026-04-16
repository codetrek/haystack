#!/bin/bash
# Run coverage tool in CI mode
# This script is called by GitHub Actions workflow

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

COVERAGE_BIN=$(mktemp)
trap 'rm -f "$COVERAGE_BIN"' EXIT

cd "$PROJECT_ROOT/scripts/lib/coverage"
go build -o "$COVERAGE_BIN" .

cd "$PROJECT_ROOT"
EXCLUDE_FUNCS="PutNode,DeleteNode,SetNodeMapping,DeleteNodeMapping" "$COVERAGE_BIN"
