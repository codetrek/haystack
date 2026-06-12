# Haystack

Haystack is a local code-search indexer written in Go. A single binary runs
either as a long-lived **server** (daemon) that scans and indexes local
repositories ("workspaces") and answers queries, or as a thin **client** (CLI)
that forwards commands to a running server. Queries are served over an HTTP API
and an MCP (Model Context Protocol) endpoint, so both humans at a terminal and
AI tools can search across large codebases.

## Overview

- **Server mode (daemon).** Indexes one or more workspaces into on-disk stores
  and exposes search over HTTP and MCP. Indexing covers full-text content,
  filenames, and (optionally) code symbols via ctags.
- **Client mode (CLI).** A presentation layer that builds requests, posts them
  to the server over loopback TCP or a Unix socket, and renders the results.
- **MCP integration.** AI assistants and editors can call Haystack's search
  tools through the MCP endpoint exposed by the running server.

The same process picks its mode at startup in `cmd/haystack/main.go`: if it is
launched in daemon mode it calls `server.Run()`; otherwise it dispatches the CLI
subcommand through `client.Run()`.

The repository is a Go workspace (`go.work`) with two modules:

- The **application module** (`github.com/codetrek/haystack`, root `go.mod`) —
  the server, client, indexer/searcher, and haystack-specific core packages
  under `internal/`.
- The **`searchcore` module** (`github.com/codetrek/haystack/searchcore`,
  `searchcore/go.mod`) — a reusable, embeddable full-text search core with no
  dependency on haystack internals.

## Build and run

### Prerequisites

- Go 1.23+ (the module sets toolchain `go1.24.2`).
- Git.
- Optional: [universal-ctags](https://github.com/universal-ctags/ctags) for
  symbol indexing (configure its path under `bin_path.ctags`).

### Clone

```bash
git clone https://github.com/codetrek/haystack.git
cd haystack
```

All commands below run from the repository root. The Go workspace
(`go.work`) wires in both the application module and `searchcore`, so a standard
`go build ./...` covers the application module; build `searchcore` separately
from its own directory (see [Building both modules](#building-both-modules)).

### Build

Build the `haystack` binary into `build/`:

```bash
make build
```

Or build directly with `go`:

```bash
go build -o build/haystack ./cmd/haystack/
```

A cross-platform release build that produces zipped binaries for multiple
OS/arch targets is available via the build script:

```bash
go run build.go
```

### Configure

Configuration is optional — Haystack runs with sensible defaults. To customize,
copy the example file and edit it:

```bash
cp config.example.yaml config.local.yaml
```

Configuration files are loaded in order from `$CWD/config.local.yaml`,
`$CWD/config.yaml`, then `$HOME/.haystack/config.yaml`. By default the index is
stored under `$HOME/.haystack/index` and the server listens on TCP port `13134`
(a Unix socket can be configured instead). See the
[configuration reference](docs/config/README.md) for all options.

### Run the server

Start the daemon in the current process:

```bash
go run ./cmd/haystack/ server run
```

Or, using the built binary, start/stop/check it as a background daemon:

```bash
build/haystack server start     # launch a background daemon
build/haystack server status    # check whether it is running
build/haystack server stop      # stop it
build/haystack server restart   # restart it
```

### Run a search

Add a workspace and query it through the CLI (the client talks to the running
server):

```bash
# Index a repository as a workspace
build/haystack workspace create /path/to/repo
build/haystack workspace sync /path/to/repo

# Content search
build/haystack search "error AND handle" -workspace /path/to/repo

# Filename search
build/haystack files config -workspace /path/to/repo

# Symbol search (requires ctags + symbols enabled in config)
build/haystack symbols MyFunc -workspace /path/to/repo
```

Run `build/haystack <command> -h` for command-specific flags, or
`build/haystack help` for the full command list (`search`, `files`, `symbols`,
`workspace`, `server`, `version`, `help`).

### MCP usage

When the server is running with a TCP address, it also exposes an MCP endpoint
so AI tools and language models can search the indexed code:

- Streamable HTTP: `http://<server_address>/mcp`
- Legacy SSE: `http://<server_address>/mcp/sse` (with message endpoint
  `http://<server_address>/mcp/message`)

Point an MCP-capable client at that endpoint to use the `HaystackSearch` and
related tools. See the [MCP integration guide](docs/APIs/mcp-integration.md) for
tool names, parameters, and examples.

### Building both modules

The CI sanity check builds each module independently:

```bash
go build ./...                 # application module (repo root)
(cd searchcore && go build ./...)   # searchcore module
```

### Test

```bash
make test        # go test ./... -count=1
make coverage    # tests with coverage report
```

## Architecture

Haystack is split into a daemon (server) and a CLI (client) over a shared HTTP
API, with a layered search core underneath. The server opens two on-disk
Pebble-backed stores (documents/catalog and the inverted index), drives an
indexer that scans workspaces, and answers content/filename/symbol queries
through a searcher and the HTTP/MCP layer. The `searchcore` module provides the
reusable full-text engine (inverted index, document storage, collection
management, tokenizer) beneath the haystack-specific core packages.

For the full component breakdown, module boundaries, storage and index
subsystems, the workspace model, and the main data flows, see
**[docs/architecture.md](docs/architecture.md)**.

## Documentation

Start at the **[documentation index](docs/README.md)** for the full evergreen
doc set. Highlights:

- [Architecture overview](docs/architecture.md)
- [API reference](docs/APIs/complete-api-reference.md)
- [Configuration reference](docs/config/README.md)
- [`searchcore` module docs](searchcore/README.md)

## Contributing

Contributions are welcome. Please open a pull request:

1. Fork the repository.
2. Create a feature branch (`git checkout -b feature/amazing-feature`).
3. Commit your changes.
4. Push to your branch and open a Pull Request.
