#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
HOOKS_DIR="$PROJECT_ROOT/git-hooks"

# Set git to use our hooks directory
echo "Setting git hooks path to $HOOKS_DIR..."
git config core.hooksPath "$HOOKS_DIR"

# Ensure hooks are executable
chmod +x "$HOOKS_DIR/pre-commit"
chmod +x "$HOOKS_DIR/commit-msg"
chmod +x "$HOOKS_DIR/pre-push"

echo "Git hooks configured successfully!"
echo "Hooks path: $HOOKS_DIR"
echo ""
echo "Active hooks:"
echo "  - pre-commit: Block main branch commits, run gofmt"
echo "  - commit-msg: Block co-author credits"
echo "  - pre-push: Run coverage check"
