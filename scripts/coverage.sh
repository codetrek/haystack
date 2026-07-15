#!/usr/bin/env bash
# Run Go coverage tool
# Usage: ./scripts/coverage.sh [detail]

set -e

cd "$(dirname "$0")/../packages/server"
go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.2 "$@"
