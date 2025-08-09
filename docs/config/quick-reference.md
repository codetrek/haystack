# Haystack Configuration Quick Reference

This is a quick reference guide for the most commonly used Haystack configuration options.

## Basic Configuration

### Minimal Configuration

```yaml
global:
  port: 13134
  data_path: "~/.haystack" # Or "C:\Users\<user>\.haystack" for windows
```

### Common Settings

```yaml
global:
  port: 13134                    # Server port (0 to disable TCP)
  data_path: "~/.haystack"       # Data storage location, Or "C:\Users\<user>\.haystack" for windows

client:
  default_workspace: "/path/to/workspace"  # Default workspace
  default_limit:
    max_results: 500             # Client result limit

server:
  max_file_size: 5242880         # 5MB file size limit
  index_workers: 4               # Indexing parallelism
  cache_size: 16777216           # 16MB cache

symbols:
  enable_feature: true           # Enable symbol parsing
  enable_prompt_search: false    # Disable AI search by default, WIP
```

## Performance Tuning

### For Large Codebases

```yaml
server:
  max_file_size: 10485760        # 10MB - allow larger files
  index_workers: 8               # More workers for faster indexing
  cache_size: 67108864           # 64MB - larger cache

  search:
    limit:
      max_results: 200000        # Allow more results
      max_files_results: 2000    # Search more files
```

### For Resource-Constrained Systems

```yaml
server:
  max_file_size: 1048576         # 1MB - smaller files only
  index_workers: 2               # Fewer workers
  cache_size: 8388608            # 8MB - smaller cache

  search:
    limit:
      max_results: 10000         # Fewer results
      max_files_results: 100     # Search fewer files
```

## File Filtering

### Include Specific Directories

```yaml
server:
  filters:
    include:
      - "src/**/*"
      - "lib/**/*"
      - "include/**/*"
```

### Exclude Build Artifacts

```yaml
server:
  filters:
    exclude:
      use_git_ignore: true       # Respect .gitignore
      customized:
        - "node_modules/"
        - "dist/"
        - "build/"
        - "*.log"
        - "*.tmp"
```

## Symbol Configuration

### Enable AI Features

```yaml
symbols:
  enable_feature: true
  enable_prompt_search: true
  embedding_port: 13144
  default_enable_embedding_symbols: true
  default_enable_embedding_prompt: true
```

### Disable Symbol Features

```yaml
symbols:
  enable_feature: false
  enable_prompt_search: false
```

## Networking

### TCP Only

```yaml
global:
  port: 13134
  # socket_path not specified
```

### Unix Socket Only

```yaml
global:
  port: 0                        # Disable TCP
  socket_path: "/tmp/haystack.sock"
```

### Both TCP and Unix Socket

```yaml
global:
  port: 13134
  socket_path: "/tmp/haystack.sock"
```

## Binary Paths (Advanced)

### Custom Tool Paths

```yaml
bin_path:
  node: "/usr/local/bin/node"
  ctags: "/usr/local/bin/ctags"
  embedding_server_js: "./embedding/dist/index.js"
```

## Configuration File Locations

Haystack searches for configuration files in this order:

1. `config.local.yaml` (highest priority - for local overrides)
2. `config.yaml` (standard configuration)
3. `config.example.yaml` (development builds only)

## Default Ports

- **Main Server**: 13134
- **Embedding Server**: 13144

## Default Data Location

- **Linux/macOS**: `~/.haystack`
- **Windows**: `%USERPROFILE%\.haystack`

## Quick Tips

- Use `config.local.yaml` for machine-specific settings
- Set `port: 0` to disable TCP server and use Unix sockets only
- Increase `index_workers` on multi-core systems for faster indexing
- Use `.gitignore` integration: `use_git_ignore: true`
- Start with default settings and adjust based on performance needs
