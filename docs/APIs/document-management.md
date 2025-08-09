# Document Management APIs

These APIs provide functionality for managing individual documents within a workspace, allowing for real-time updates to the search index.

## Update Document

**Endpoint:** `POST /api/v1/document/update`

### Description

Adds or updates a single document in the workspace search index. This API is typically used for real-time indexing when files are modified in an editor or development environment.

### Request

- **Method:** POST
- **Content-Type:** `application/json`

**Request Body:**
```json
{
  "workspace": "/absolute/path/to/workspace",
  "path": "relative/path/to/file.go"
}
```

### Request Fields

- `workspace` (required): Absolute path to the workspace directory
- `path` (required): Relative path to the file within the workspace

### Response

**Success:**
```json
{
  "code": 0,
  "message": "Ok"
}
```

**File Ignored:**
```json
{
  "code": 0,
  "message": "File Ignored"
}
```

**Error:**
```json
{
  "code": 1,
  "message": "Error description"
}
```

### Response Codes

- `0`: Success (file updated or ignored based on filters)
- `1`: Error (workspace not found, file processing failed)

### Notes

- Files are automatically filtered based on workspace configuration
- If a file doesn't match include patterns or matches exclude patterns, it will be ignored
- The operation is synchronous and will complete before returning
- Updates existing index entries or creates new ones as needed

---

## Delete Document

**Endpoint:** `POST /api/v1/document/delete`

### Description

Removes a document from the workspace search index. The file itself is not deleted from the filesystem, only its index entries are removed.

### Request

- **Method:** POST
- **Content-Type:** `application/json`

**Request Body:**
```json
{
  "workspace": "/absolute/path/to/workspace",
  "path": "relative/path/to/file.go"
}
```

### Request Fields

- `workspace` (required): Absolute path to the workspace directory
- `path` (required): Relative path to the file within the workspace

### Response

```json
{
  "code": 0,
  "message": "Ok"
}
```

### Response Codes

- `0`: Success (document removed from index)

### Error Handling

- If the workspace is not found, returns HTTP 400 Bad Request
- If the document was not in the index, the operation still succeeds (idempotent)

### Notes

- This operation only affects the search index, not the actual file
- The operation is synchronous and completes immediately
- Removing a document makes it unsearchable until re-indexed
- Commonly used when files are deleted or moved in development environments
