#!/bin/bash
set -e

echo "Checking Go code formatting..."

# Run gofmt on all files and list those that are not formatted
# Exclude vendor directory
unformatted=$(find . -name "*.go" -not -path "./vendor/*" | xargs gofmt -l)

if [ -n "$unformatted" ]; then
    echo "Error: The following files are not formatted correctly:"
    echo "$unformatted"
    echo "Please run 'gofmt -w .' to format your code."
    exit 1
fi

echo "Go code formatting check passed."
