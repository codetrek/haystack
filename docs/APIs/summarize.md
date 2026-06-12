# Haystack API Documentation Summary

Haystack provides a comprehensive REST API for local code search indexing and querying. The API is organized into four main categories:

## API Categories

### 1. Server Control APIs
- **Health Check**: Monitor server status and health
- **Server Management**: Check status, stop, and restart the server

### 2. Workspace Management APIs
- **Create Workspace**: Initialize new code search workspace
- **Update Workspace**: Modify workspace configuration
- **Delete Workspace**: Remove workspace and its index
- **List Workspaces**: Get all available workspaces
- **Get Workspace**: Retrieve specific workspace details
- **Move Workspace**: Update the on-disk path of an existing workspace
- **Sync Operations**: Synchronize workspace content

### 3. Document Management APIs
- **Update Document**: Add or update individual files in workspace
- **Delete Document**: Remove files from workspace index

### 4. Search APIs
- **Content Search**: Full-text search across code content
- **File Search**: Find files by name/path with fuzzy matching
- **Symbol Search**: Search for code symbols (functions, classes, etc.)

## Base URL Structure
All APIs follow the pattern: `/api/v1/{category}/{action}`

## Response Format
All APIs return JSON responses with a consistent structure:
```json
{
  "code": 0,
  "message": "Ok",
  "data": {}
}
```

## Content Types
- **Request**: `application/json`
- **Response**: `application/json`
- **Streaming**: `text/event-stream` (for content search)

## Authentication
The APIs do not require authentication in the current implementation.

## Error Handling
- HTTP 400: Bad Request (invalid JSON, missing required fields)
- HTTP 500: Internal Server Error
- Response codes in JSON: 0 = success, 1+ = error codes

## Special Features
- **Streaming Support**: Content search supports streaming responses
- **Workspace Indexing**: Automatic background indexing
- **Git Integration**: Respects .gitignore files
- **Filter Support**: Include/exclude patterns for targeted searches
- **MCP Integration**: Model Context Protocol support for AI tools

For detailed documentation of each API endpoint, refer to the individual API documentation files.
