#!/bin/bash
# Run coverage tool in CI mode
# This script is called by GitHub Actions workflow

set -e

# The App CI job gates the SERVER module (core has its own job).
cd "$(dirname "$0")/../../../packages/server"
go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.2
