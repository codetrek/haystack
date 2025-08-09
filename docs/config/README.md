# Configuration Documentation

This directory contains documentation for Haystack's configuration system.

## Files

- **[configuration.md](configuration.md)** - Complete configuration reference with detailed explanations of all settings, validation rules, and examples
- **[quick-reference.md](quick-reference.md)** - Quick reference guide with common configuration patterns and use cases

## Overview

Haystack uses YAML configuration files to control various aspects of the search indexer including:

- Server settings (ports, workers, file size limits)
- Search behavior and result limits
- File filtering and inclusion/exclusion patterns
- Symbol parsing and AI features
- Client defaults and networking options

## Getting Started

1. Start with the [quick-reference.md](quick-reference.md) for common configuration patterns
2. Refer to [configuration.md](configuration.md) for detailed explanations of all available options
3. Use `config.local.yaml` for machine-specific overrides
4. Configuration files are loaded from the same directory as the Haystack executable

## Key Features

- **Auto-validation**: Invalid settings are automatically corrected with defaults
- **Performance tuning**: Configurable workers, cache sizes, and result limits
- **Flexible filtering**: Include/exclude patterns with Git integration
- **Multiple networking**: Support for both TCP and Unix domain sockets
- **AI integration**: Configurable symbol parsing and embedding features
