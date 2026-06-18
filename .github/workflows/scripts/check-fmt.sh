#!/bin/bash
set -e

echo "Checking Go code formatting..."

# Run gofmt on all files and list those that are not formatted
# Exclude vendor and third_party (vendored upstream code we don't reformat)
unformatted=$(find . -name "*.go" -not -path "./vendor/*" -not -path "*/third_party/*" | xargs gofmt -l)

if [ -n "$unformatted" ]; then
    echo "Error: The following files are not formatted correctly:"
    echo "$unformatted"
    echo "Please run 'gofmt -w \$(find . -name \"*.go\" -not -path \"./vendor/*\" -not -path \"*/third_party/*\")' to format your code."
    exit 1
fi

echo "Go code formatting check passed."
