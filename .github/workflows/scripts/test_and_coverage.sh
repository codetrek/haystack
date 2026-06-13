#!/bin/bash
# Run coverage tool in CI mode
# This script is called by GitHub Actions workflow

set -e

go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.0
