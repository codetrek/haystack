# Haystack Configuration

This document describes the configuration system for Haystack, a local code search indexer tool.

## Configuration File

Haystack uses YAML configuration files that are loaded in the following order of precedence:

1. `config.local.yaml` (highest priority)
2. `config.yaml`
3. `config.example.yaml` (only in development builds)

The configuration file is searched for in the same directory as the Haystack executable.

## Configuration Structure

The configuration is organized into several main sections:

### Global Configuration

```yaml
global:
  data_path: string     # Path to store Haystack data (default: ~/.haystack)
  port: int            # TCP server port (default: 13134, 0 to disable)
  socket_path: string  # Unix domain socket path (optional)
```

**Global Settings:**

- `data_path`: Directory where Haystack stores its data files. If not specified, defaults to `~/.haystack`
- `port`: TCP port for the Haystack server. Set to 0 to disable TCP server. Valid range: 1-65535
- `socket_path`: Path for Unix domain socket communication.

### Client Configuration

```yaml
client:
  default_workspace: string    # Default workspace to use
  default_limit:              # Default search result limits for client
    max_results: int          # Maximum total results (default: 500)
    max_results_per_file: int # Maximum results per file (default: 50)
    max_files_results: int    # Maximum number of files in results (default: 100)
```

**Client Settings:**

- `default_workspace`: The default workspace path to use when none is specified
- `default_limit`: Default search result limits applied to client requests

### Server Configuration

```yaml
server:
  max_file_size: int64        # Maximum file size to index in bytes (default: 5MB)
  index_workers: int          # Number of indexing workers (default: 6)
  symbol_parser_workers: int  # Number of symbol parsing workers (default: 2)
  cache_size: int64          # Cache size in bytes (default: 16MB)
  logging_stdout: bool       # Enable stdout logging (default: false)

  filters:                   # File filtering configuration
    include:                 # Patterns to include
      - "**/*"              # Default: include all files
    exclude:                 # Patterns to exclude
      use_git_ignore: bool   # Use .gitignore rules (default: false)
      customized:           # Custom exclusion patterns
        - "node_modules/"
        - "dist/"
        - "build/"
        - "vendor/"
        - "out/"
        - "obj/"
        - "log/"
        - "logs/"
        - "debug/"
        - "release/"
        - ".*"              # Hidden files/directories
        - "*.log"
        - "*.log.*"
        - "!.github/"       # Exception: include .github/

  search:                    # Search behavior configuration
    max_wildcard_length: int      # Maximum wildcard pattern length (default: 24, max: 64)
    max_keyword_distance: int     # Maximum distance between keywords (default: 32, max: 128)
    limit:                       # Server-side search limits
      max_results: int           # Maximum total results (default: 100000)
      max_results_per_file: int  # Maximum results per file (default: 500)
      max_files_results: int     # Maximum number of files (default: 1000)
```

**Server Settings:**

- `max_file_size`: Maximum size of files to index (in bytes). Files larger than this are skipped
- `index_workers`: Number of concurrent workers for indexing operations. Auto-adjusts to CPU count if invalid
- `symbol_parser_workers`: Number of workers for parsing code symbols. Auto-adjusts to CPU count if invalid
- `cache_size`: Size of the internal cache in bytes
- `logging_stdout`: Whether to output logs to stdout
- `filters`: File inclusion/exclusion patterns for indexing
- `search`: Search behavior and result limits

### Symbols Configuration

```yaml
symbols:
  enable_feature: bool                     # Enable symbol parsing (default: true)
```

**Symbol Settings:**

- `enable_feature`: Master switch for symbol parsing functionality

### Binary Path Configuration

```yaml
bin_path:
  ctags: string               # Path to CTags executable
```

**Binary Path Settings:**

- `ctags`: Path to the CTags executable (for symbol parsing)

### Test Configuration

```yaml
for_test:
  path: string  # Path used for testing purposes
```

## Default Values

| Setting | Default Value | Description |
|---------|---------------|-------------|
| `global.port` | 13134 | Default TCP server port |
| `global.data_path` | `~/.haystack` | Default data directory |
| `server.max_file_size` | 5MB | Maximum file size to index |
| `server.index_workers` | 6 | Number of indexing workers |
| `server.symbol_parser_workers` | 2 | Number of symbol parsing workers |
| `server.cache_size` | 16MB | Internal cache size |
| `server.search.max_wildcard_length` | 24 | Maximum wildcard pattern length |
| `server.search.max_keyword_distance` | 32 | Maximum keyword distance |
| `server.search.limit.max_results` | 100000 | Server max total results |
| `server.search.limit.max_results_per_file` | 500 | Server max results per file |
| `server.search.limit.max_files_results` | 1000 | Server max files in results |
| `client.default_limit.max_results` | 500 | Client max total results |
| `client.default_limit.max_results_per_file` | 50 | Client max results per file |
| `client.default_limit.max_files_results` | 100 | Client max files in results |
| `symbols.enable_feature` | true | Symbol parsing enabled |

## Configuration Validation

The configuration system includes automatic validation and correction:

- **Worker Counts**: `index_workers` and `symbol_parser_workers` are reset to the number of CPU cores when set to zero/negative or above the CPU count
- **Ports**: A port outside the range 1-65535 is reset to the default (13134) when no `socket_path` is set, or disabled (0) when a `socket_path` is set
- **File Sizes**: Zero or negative `max_file_size` and `cache_size` values are reset to their defaults (5MB and 16MB)
- **Search Ranges**: `max_wildcard_length` is capped at 64 (reset to 24 if out of range); `max_keyword_distance` is capped at 128 (reset to 32 if out of range)
- **Server Search Limits**: `max_results`, `max_results_per_file`, and `max_files_results` are reset to their defaults when zero/negative or above the maxima (100000 / 500 / 1000)
- **Client Search Limits**: Client `max_results` and `max_results_per_file` cannot exceed the server's corresponding limits, and `max_files_results` cannot exceed 1000; out-of-range values are reset to the client defaults (500 / 50 / 100)
- **Paths**: A relative `socket_path` is resolved against the system temp directory

## Example Configuration

```yaml
global:
  data_path: "/home/<user>/.haystack"
  port: 13134

client:
  default_workspace: "/home/<user>/projects"
  default_limit:
    max_results: 200
    max_results_per_file: 25
    max_files_results: 50

server:
  max_file_size: 10485760  # 10MB
  index_workers: 4
  symbol_parser_workers: 4
  logging_stdout: false

  filters:
    include:
      - "**/*"
    exclude:
      use_git_ignore: true
      customized:
        - "node_modules/"
        - "dist/"
        - "build/"
        - "vendor/"
        - "out/"
        - "obj/"
        - "log/"
        - "logs/"
        - "debug/"
        - "release/"
        - ".*"              # Hidden files/directories
        - "*.log"
        - "*.log.*"
        - "!.github/"       # Exception: include .github/

  search:
    max_wildcard_length: 30
    max_keyword_distance: 40
    limit:
      max_results: 50000
      max_results_per_file: 250
      max_files_results: 500

bin_path:
  ctags: "/usr/local/bin/ctags"
```

## Configuration Loading Process

1. **Search Phase**: Look for configuration files in order of precedence
2. **Load Phase**: Parse YAML and unmarshal into configuration structure
3. **Validation Phase**: Apply defaults, validate ranges, and correct invalid values
4. **Initialization Phase**: Create data directory and prepare runtime environment

## Environment-Specific Behavior

- **Development Builds**: Include `config.example.yaml` in search path
- **Data Directory**: Automatically created if it doesn't exist
- **Socket Paths**: Converted to absolute paths in system temp directory if relative
- **Worker Limits**: Automatically adjusted based on available CPU cores
