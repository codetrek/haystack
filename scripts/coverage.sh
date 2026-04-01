#!/usr/bin/env bash
# Run Go coverage tool
# Usage: ./scripts/coverage.sh

cd "$(dirname "$0")/../src"
go run ../scripts/lib/coverage/ "$@"
