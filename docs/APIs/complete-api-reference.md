# Complete API Reference

This document provides a comprehensive reference for all available Haystack API endpoints, organized by category.

## Health and Utility Endpoints

### Health Check
- **Endpoint:** `GET /health`
- **Description:** Basic health check endpoint
- **Documentation:** [Server Control APIs](./server-control.md#health-check)

### Root Endpoint
- **Endpoint:** `GET /`
- **Description:** Returns 404 Not Found (default catch-all)

## Server Control APIs

All server control endpoints are documented in detail at: [Server Control APIs](./server-control.md)

### Server Status
- **Endpoint:** `POST /api/v1/server/status`
- **Description:** Get detailed server status information

### Server Restart
- **Endpoint:** `POST /api/v1/server/restart`
- **Description:** Restart the server gracefully

### Server Stop
- **Endpoint:** `POST /api/v1/server/stop`
- **Description:** Stop the server gracefully

## Workspace Management APIs

All workspace management endpoints are documented in detail at: [Workspace Management APIs](./workspace-management.md)

### Create Workspace
- **Endpoint:** `POST /api/v1/workspace/create`
- **Description:** Create a new workspace for code indexing

### Update Workspace
- **Endpoint:** `POST /api/v1/workspace/update`
- **Description:** Update workspace configuration and filters

### Delete Workspace
- **Endpoint:** `POST /api/v1/workspace/delete`
- **Description:** Delete a workspace and its index data

### List Workspaces
- **Endpoint:** `POST /api/v1/workspace/list`
- **Description:** Get a list of all available workspaces

### Get Workspace
- **Endpoint:** `POST /api/v1/workspace/get`
- **Description:** Get detailed information about a specific workspace

### Sync Workspace
- **Endpoint:** `POST /api/v1/workspace/sync`
- **Description:** Synchronize a specific workspace

### Sync All Workspaces
- **Endpoint:** `POST /api/v1/workspace/sync-all`
- **Description:** Synchronize all registered workspaces

## Document Management APIs

All document management endpoints are documented in detail at: [Document Management APIs](./document-management.md)

### Update Document
- **Endpoint:** `POST /api/v1/document/update`
- **Description:** Add or update a single document in the workspace index

### Delete Document
- **Endpoint:** `POST /api/v1/document/delete`
- **Description:** Remove a document from the workspace index

## Search APIs

All search endpoints are documented in detail at: [Search APIs](./search.md)

### Content Search
- **Endpoint:** `POST /api/v1/search/content`
- **Description:** Full-text search across code content
- **Special Features:**
  - Supports streaming responses with `Accept: text/event-stream`
  - Advanced query syntax with logical operators
  - Context lines before/after matches

### File Search
- **Endpoint:** `POST /api/v1/search/files`
- **Description:** Search for files by name/path with fuzzy matching

### Symbol Search
- **Endpoint:** `POST /api/v1/search/symbols`
- **Description:** Search for code symbols (functions, classes, variables)

### Prompt Search
- **Endpoint:** `POST /api/v1/search/prompts`
- **Description:** Search for AI prompts within the codebase

## MCP Integration

Model Context Protocol integration is documented at: [MCP Integration](./mcp-integration.md)

### MCP Endpoints
- **Base Endpoint:** `/mcp`
- **SSE Endpoint:** `/mcp/sse`
- **Message Endpoint:** `/mcp/message`

### MCP Tools
- **HaystackSearch:** Code content search tool
- **HaystackFiles:** File search tool
- **HaystackPromptSearch:** Prompt search tool

## API Conventions

### Request Format
- **Content-Type:** `application/json`
- **Method:** POST (for all API endpoints except health check)
- **Body:** JSON object with required parameters

### Response Format
All APIs return responses in this format:
```json
{
  "code": 0,
  "message": "Ok",
  "data": {}
}
```

### Response Codes
- `code: 0` - Success
- `code: 1+` - Various error conditions (see individual API documentation)

### HTTP Status Codes
- `200 OK` - Successful request
- `400 Bad Request` - Invalid JSON or missing required fields
- `404 Not Found` - Endpoint not found
- `500 Internal Server Error` - Server error

### Streaming Support
Content search supports streaming responses:
- **Request Header:** `Accept: text/event-stream`
- **Response Format:** Server-Sent Events (SSE)
- **Timeout:** 60 seconds for streaming, 10 seconds for regular requests

### Common Parameters

#### Workspace Path
- Must be an absolute path
- Used across all workspace and search operations
- Example: `/home/user/projects/myproject`

#### Search Filters
```json
{
  "filters": {
    "path": "src/",
    "include": "*.go,*.js",
    "exclude": "*_test.go"
  }
}
```

#### Search Limits
```json
{
  "limit": {
    "max_results": 100,
    "max_results_per_file": 10,
    "max_files_results": 50
  }
}
```

### Authentication
No authentication is required for any endpoints in the current implementation.

### Rate Limiting
No rate limiting is implemented in the current version.

## Error Handling

### Common Error Patterns
1. **Workspace Not Found**
   ```json
   {
     "code": 1,
     "message": "Workspace not found: /path/to/workspace"
   }
   ```

2. **Invalid Path**
   ```json
   {
     "code": 2,
     "message": "Workspace path must be absolute"
   }
   ```

3. **Missing Required Field**
   ```json
   {
     "code": 1,
     "message": "Workspace is required"
   }
   ```

### HTTP Error Responses
- Malformed JSON requests return HTTP 400 with error message
- Internal server errors return HTTP 500
- Unmatched routes return HTTP 404

## Server Configuration

The server supports both TCP and Unix socket connections:
- **TCP Address:** Configurable (e.g., `localhost:8080`)
- **Unix Socket:** Optional socket path for local connections
- **Graceful Shutdown:** 5-second timeout for clean shutdown
- **MCP Integration:** Automatically enabled when TCP address is provided

## Development Notes

### Concurrency
- All operations are designed to be thread-safe
- Workspace indexing happens asynchronously
- Multiple search requests can be processed concurrently

### Performance Considerations
- Use filters to improve search performance
- Streaming responses provide better UX for large result sets
- Symbol search is optimized with ctags indexing
- File search uses efficient fuzzy matching algorithms

### Logging
- All API requests are logged with timing information
- Error conditions are logged with appropriate context
- Request/response payloads are logged (excluding sensitive data)
