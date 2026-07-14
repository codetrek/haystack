# Local Code Search Indexer

A robust tool for creating and querying search indexes on local code repositories, making it easier to find content across large codebases.

## Overview

This project provides a fast and efficient way to index your local code repositories and perform complex searches. It includes both a server component for maintaining indexes and a client for querying them.

## Features

- Fast indexing of local code repositories
- Full-text search across codebases
- Support for various document types and programming languages
- Configurable server and client options
- Git integration for repository analysis

## Architecture

The Search Indexer is structured with the following components:

- **Client**: Provides search query interface
- **Server**:
  - **Core**: Handles document processing and management
  - **Indexer**: Creates and maintains search indexes
  - **Searcher**: Processes search queries
  - **Server**: HTTP API, MCP
- **Runtime**: Manages execution environment
- **Utils**: Common utilities and helper functions

## Getting Started

### Prerequisites

- Go 1.24+
- Git

### Installation

Clone the repository:

```bash
git clone https://github.com/codetrek/haystack.git
cd haystack
```

The repository is a Go **workspace** (`go.work`) spanning two modules —
`packages/core` (the search/index library) and `packages/server` (the server + CLI
app). Dependencies are vendored under `vendor/`, so there is no separate
dependency-install step.

### Configuration

Copy the example configuration and modify as needed:

```bash
cp packages/server/config.example.yaml config.local.yaml
```

Edit `config.local.yaml` to configure your indexing preferences.

### Running the Server

```bash
go run ./packages/server/cmd/haystack server run
```

### Using the Client

```bash
# Example query command
go run ./packages/server/cmd/haystack search "your search query"
```

## Development

### Testing

Run the test suite with:

```bash
make test            # or: go test ./packages/core/... ./packages/server/...
```

### Building

```bash
make build           # or: go build -o build/haystack ./packages/server/cmd/haystack/
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request
