# Server Control APIs

These APIs provide server management and health monitoring capabilities.

## Health Check

**Endpoint:** `GET /health`

### Description
Returns the current health status of the Haystack server.

### Request
- **Method:** GET
- **Content-Type:** Not required
- **Body:** None

### Response
```json
{
  "code": 0,
  "message": "healthy",
  "data": {
    "data_path": "/path/to/data",
    "pid": 12345,
    "version": "1.0.0"
  }
}
```

### Response Fields
- `code`: Status code (0 = success)
- `message`: Status message
- `data.data_path`: Path to server data directory
- `data.pid`: Process ID of the server
- `data.version`: Server version

---

## Server Status

**Endpoint:** `POST /api/v1/server/status`

### Description
Returns detailed server status information including shutdown and restart states.

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
    "shutting_down": false,
    "restarting": false,
    "pid": 12345,
    "version": "1.0.0",
    "data_path": "/path/to/data"
  }
}
```

### Response Fields
- `data.shutting_down`: Whether server is in shutdown process
- `data.restarting`: Whether server is restarting
- `data.pid`: Process ID
- `data.version`: Server version
- `data.data_path`: Data directory path

---

## Server Restart

**Endpoint:** `POST /api/v1/server/restart`

### Description
Initiates a server restart. The server will gracefully shut down and restart.

### Request
- **Method:** POST
- **Content-Type:** `application/json`
- **Body:** Empty JSON object `{}`

### Response
```json
{
  "code": 0,
  "message": "restarting"
}
```

### Notes
- The restart is asynchronous
- Client connections will be terminated during restart
- Server will maintain workspace data across restarts

---

## Server Stop

**Endpoint:** `POST /api/v1/server/stop`

### Description
Initiates a graceful server shutdown.

### Request
- **Method:** POST
- **Content-Type:** `application/json`
- **Body:** Empty JSON object `{}`

### Response
```json
{
  "code": 0,
  "message": "stopping"
}
```

### Notes
- Shutdown is graceful with 5-second timeout
- All active connections will be closed
- Workspace data is preserved
