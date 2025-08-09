# Workspace Management APIs

These APIs provide comprehensive workspace management functionality for creating, updating, deleting, and synchronizing code search workspaces.

## Create Workspace

**Endpoint:** `POST /api/v1/workspace/create`

### Description

Creates a new workspace for code indexing and search. A workspace represents a directory that will be indexed for code search functionality.

### Request

- **Method:** POST
- **Content-Type:** `application/json`

**Request Body:**
```json
{
  "workspace": "/absolute/path/to/project",
  "use_global_filters": true,
  "filters": {
    "exclude": {
      "use_git_ignore": true,
      "customized": ["*.tmp", "*.log"]
    },
    "include": ["*.go", "*.js", "*.py"]
  }
}
```

### Request Fields

- `workspace` (required): Absolute path to the workspace directory
- `use_global_filters` (optional): Whether to use global filter settings
- `filters` (optional): Custom filter configuration
  - `exclude.use_git_ignore`: Respect .gitignore files
  - `exclude.customized`: Additional patterns to exclude
  - `include`: File patterns to include

### Response

```json
{
  "code": 0,
  "message": "Ok",
  "data": {
    "id": 1,
    "path": "/absolute/path/to/project",
    "total_files": 0,
    "use_global_filters": true,
    "filters": {...},
    "created_time": "2024-01-01T00:00:00Z",
    "last_accessed_time": "2024-01-01T00:00:00Z",
    "last_full_sync_time": "2024-01-01T00:00:00Z",
    "indexing": true
  }
}
```

### Response Codes

- `0`: Success
- `1`: Error (workspace creation failed)

---

## Update Workspace

**Endpoint:** `POST /api/v1/workspace/update`

### Description

Updates the configuration of an existing workspace, including filter settings.

### Request

- **Method:** POST
- **Content-Type:** `application/json`

**Request Body:**
```json
{
  "workspace": "/absolute/path/to/project",
  "use_global_filters": false,
  "filters": {
    "exclude": {
      "use_git_ignore": true,
      "customized": ["*.tmp"]
    },
    "include": ["*.go"]
  }
}
```

### Response

```json
{
  "code": 0,
  "message": "Ok",
  "data": {
    "id": 1
  }
}
```

---

## Delete Workspace

**Endpoint:** `POST /api/v1/workspace/delete`

### Description

Deletes a workspace and removes all its indexed data.

### Request

- **Method:** POST
- **Content-Type:** `application/json`

**Request Body:**
```json
{
  "workspace": "/absolute/path/to/project"
}
```

### Response

```json
{
  "code": 0,
  "message": "Deleted",
  "data": {
    "id": 1,
    "path": "/absolute/path/to/project",
    "total_files": 150,
    "created_time": "2024-01-01T00:00:00Z",
    "last_accessed_time": "2024-01-01T00:00:00Z",
    "last_full_sync_time": "2024-01-01T00:00:00Z",
    "indexing": false
  }
}
```

### Response Codes

- `0`: Success
- `1`: Error (deletion failed)
- `2`: Invalid path (not absolute)
- `3`: Workspace not found

---

## List Workspaces

**Endpoint:** `POST /api/v1/workspace/list`

### Description

Returns a list of all available workspaces.

### Request

- **Method:** POST
- **Content-Type:** `application/json`
- **Body:** Empty JSON object `{}`

### Response

```json
{
  "code": 0,
  "message": "Ok",
  "data": {
    "workspaces": [
      {
        "id": 1,
        "path": "/path/to/project1",
        "total_files": 100,
        "created_time": "2024-01-01T00:00:00Z",
        "last_accessed_time": "2024-01-01T00:00:00Z",
        "last_full_sync_time": "2024-01-01T00:00:00Z",
        "indexing": false
      }
    ]
  }
}
```

---

## Get Workspace

**Endpoint:** `POST /api/v1/workspace/get`

### Description

Retrieves detailed information about a specific workspace.

### Request

- **Method:** POST
- **Content-Type:** `application/json`

**Request Body:**
```json
{
  "workspace": "/absolute/path/to/project"
}
```

### Response

```json
{
  "code": 0,
  "message": "Ok",
  "data": {
    "id": 1,
    "path": "/absolute/path/to/project",
    "total_files": 150,
    "use_global_filters": true,
    "filters": {...},
    "created_time": "2024-01-01T00:00:00Z",
    "last_accessed_time": "2024-01-01T00:00:00Z",
    "last_full_sync_time": "2024-01-01T00:00:00Z",
    "indexing": false
  }
}
```

### Response Codes

- `0`: Success
- `1`: Workspace not found

---

## Sync Workspace

**Endpoint:** `POST /api/v1/workspace/sync`

### Description

Triggers a synchronization of the workspace content with the search index.

### Request

- **Method:** POST
- **Content-Type:** `application/json`

**Request Body:**
```json
{
  "workspace": "/absolute/path/to/project"
}
```

### Response

```json
{
  "code": 0,
  "message": "Sync in progress..."
}
```

### Response Codes

- `0`: Success (sync initiated)
- `1`: Error (workspace not found or sync failed)

---

## Sync All Workspaces

**Endpoint:** `POST /api/v1/workspace/sync-all`

### Description

Triggers synchronization for all registered workspaces.

### Request

- **Method:** POST
- **Content-Type:** `application/json`
- **Body:** Empty JSON object `{}`

### Response

```json
{
  "code": 0,
  "message": "Sync all in progress..."
}
```

### Notes

- Synchronization is performed asynchronously
- The indexing status can be checked via the Get Workspace API
- Sync operations respect workspace filter configurations
