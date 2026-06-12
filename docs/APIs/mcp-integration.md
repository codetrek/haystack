# MCP (Model Context Protocol) Integration

Haystack provides Model Context Protocol (MCP) integration for AI tools and language models to access code search functionality.

## Overview

The MCP server is automatically initialized when Haystack starts with a TCP address. It provides two tools for AI systems to search and analyze codebases.

## MCP Endpoint

**Legacy SSE:** `http://{server_address}/mcp/sse` (with the companion message endpoint `http://{server_address}/mcp/message`)
**Streamable HTTP:** `http://{server_address}/mcp`

## MCP Tools

### 1. HaystackSearch

**Tool Name:** `HaystackSearch`

**Description:** Search for code in current project, supports prefix matching and logical operators to help you find exactly what you're looking for in your codebase.

**Parameters:**

- `query` (required): The search query supporting:
  - Basic terms: single words like 'function'
  - Prefix matching: 'func*' matches 'function', 'functional', etc.
  - Exact phrases: '"Second third"' for exact matching
  - Logical operators: 'AND' (or space) for conjunction, '|' for OR
  - Examples: 'error AND handle', 'create | update', 'init*'

- `workspace` (required): Absolute path to the project directory (e.g., /home/user/projects/project1)

- `path` (optional): Relative path to search within workspace (e.g., src/core)

- `filter` (optional): Filter results by file path using glob patterns, comma-separated (e.g., 'src/**/*.go,*.cc')

- `exclude` (optional): Exclude files using glob patterns, comma-separated (e.g., 'test/**/*.go')

- `limit` (optional): Maximum number of results to return

### 2. HaystackFiles

**Tool Name:** `HaystackFiles`

**Description:** Search for files in current project, supports fuzzy matching on filenames and attempts to return a list of the most relevant files.

**Parameters:**

- `query` (required): Case-insensitive search query with fuzzy matching
  - Example: 'savedtabgroup' will match 'saved_tab_group', 'src/**/saved/tabgroup'

- `workspace` (required): Absolute path to the project directory

- `limit` (optional): Maximum number of results to return


## MCP Server Configuration

The MCP server is configured with the following capabilities:

- **Resource Capabilities:** Read and subscribe support enabled
- **Prompt Capabilities:** Enabled
- **Logging:** Enabled for debugging and monitoring
- **Keep-Alive:** 20-second intervals for SSE connections
- **Heartbeat:** 20-second intervals for HTTP connections

## Authentication

The MCP server does not require authentication in the current implementation.

## Error Handling

MCP tools return structured responses with error information when operations fail. Errors are propagated through the MCP protocol with appropriate error codes and messages.

## Integration Notes

- The MCP server is automatically started when Haystack is launched with a TCP address
- If no TCP address is provided, MCP server initialization is skipped
- The server supports both SSE and HTTP-based communication protocols
- Tools are dynamically registered based on server configuration
- All search operations respect workspace-specific filter configurations

## Usage Example

AI tools can use these MCP tools to:

1. Search for specific code patterns using `HaystackSearch`
2. Find relevant files quickly using `HaystackFiles`

This enables AI systems to have contextual understanding of codebases and provide more accurate assistance to developers.
