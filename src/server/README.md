# Server Directory

This directory contains the server-side implementation of the Local Code Search Indexer. The server is responsible for managing the search index, processing queries, and handling client requests.

## Directory Structure

- **`core/`**: Core server functionality and business logic.
  - Contains sub-modules for document handling (`documents`), inverted indexes (`invertedindex`), code parsing (`parser`), database interaction (`pebble`), storage abstractions (`storage`), and workspace management (`workspace`).
  - Manages server state and configurations.
  - Provides core services to other components.

- **`indexer/`**: Search index creation and maintenance.
  - Implements indexing strategies (e.g., `indexer.go`, `scanner.go`, `writer.go`).
  - Manages index storage and updates.
  - Handles index optimization.

- **`searcher/`**: Search query processing.
  - Processes search requests (e.g., `searcher.go`, `query_parser.go`).
  - Implements search algorithms.
  - Manages search results.

- **`server/`**: HTTP/gRPC server implementation and API endpoint handling.
  - Contains specific handlers for different functionalities like document operations (`document.go`), Model Context Protocol (`mcp.go`), search requests (`search.go`), server control (`server_cntl.go`), and workspace operations (`workspace.go`).
  - The main request handling logic resides in `server.go` within this directory.

- **`log.go`**: Top-level file for server logging implementation.
- **`server.go`**: Top-level file serving as the main server entry point, handling initialization and orchestration.

## Key Components

1. **Main Server (`src/server/server.go`)**
   - Main server entry point.
   - Handles server initialization, configuration, and component orchestration.

2. **Logging (`src/server/log.go`)**
   - Implements server-wide logging.
   - Manages log levels, formatting, and output.

3. **Core Module (`src/server/core/`)**
   - Encapsulates core business logic including document processing, storage management (utilizing PebbleDB via `pebble/`), parsing, and workspace data.

4. **Indexer Module (`src/server/indexer/`)**
   - Responsible for creating, updating, and optimizing search indexes.
   - Includes components for scanning repositories (`scanner.go`) and writing index data (`writer.go`).

5. **Searcher Module (`src/server/searcher/`)**
   - Handles incoming search queries, parsing them (`query_parser.go`), and executing search operations using defined algorithms.

6. **API and Request Handling (`src/server/server/`)**
   - Manages the specifics of API endpoints and request/response cycles.
   - Implements handlers for search, document management, MCP, and other server interactions.

## Development Guidelines

1. **Server Architecture**
   - Follow clean architecture principles
   - Keep components loosely coupled
   - Use interfaces for component communication

2. **Error Handling**
   - Implement proper error handling
   - Use appropriate error types
   - Provide meaningful error messages

3. **Performance**
   - Optimize for concurrent operations
   - Implement caching where appropriate
   - Monitor resource usage

4. **Testing**
   - Write unit tests for all components
   - Include integration tests
   - Test error scenarios

## Configuration

Server configuration is handled through:

- Environment variables
- Configuration files
- Command-line arguments

## API Documentation

The server exposes the following main APIs:

- Search API
- Index management API
- System status API
- Configuration API
