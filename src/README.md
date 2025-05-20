# Source Code Directory

This directory contains the core source code of the Local Code Search Indexer project. It is organized into several key components:

## Directory Structure

- **client/**: Contains the client-side code for querying the search index.
- **conf/**: Manages application configuration files and loading.
- **server/**: Houses the server-side implementation, including:
  - **core/**: Core functionalities like document processing, storage (utilizing PebbleDB), parsing, inverted indexing, and workspace management.
  - **indexer/**: Code for creating and maintaining search indexes from code repositories.
  - **searcher/**: Implementation of search query processing and result retrieval.
  - **server/**: Contains main server logic, request handling (including MCP - Model Context Protocol), and coordination of server-side operations.
- **shared/**: Contains code shared across different parts of the project:
  - **running/**: Manages the application's runtime environment, server lifecycle, and shutdown procedures.
  - **types/**: Defines common data structures and type definitions used throughout the application.
- **utils/**: Common utilities and helper functions, including file system operations (`fs/`), Git integration (`git/`), and queue management (`queue/`).
- **vendor/**: Contains third-party library code (dependencies) managed by Go modules.

## Key Components

1. **Client**
   - Provides the user interface for search queries.
   - Handles communication with the server.
   - Manages search results presentation.

2. **Conf**
   - Manages application configuration (e.g., from `config.example.yaml`, `config.local.yaml`).
   - Handles loading and accessing configuration settings.

3. **Server**
   - **Core**: Central component for document handling. This includes parsing files, managing document storage (leveraging PebbleDB), creating and querying inverted indexes, and representing workspace data.
   - **Indexer**: Responsible for scanning repositories, processing files, and writing data to the search index.
   - **Searcher**: Processes user search queries, interacts with the index, and retrieves relevant search results.
   - **Server (sub-component)**: Implements the primary server logic, including handling client requests (e.g., via Model Context Protocol), managing server state, and orchestrating operations related to documents, search, and workspace management.

4. **Shared**
   - **Running**: Manages the overall application lifecycle, including server startup, graceful shutdown, and the runtime execution context.
   - **Types**: Defines common Go data structures and types (e.g., for documents, search parameters, results) that are used consistently across various components of the Haystack application.

5. **Utils**
   - Provides a collection of common helper functions and utilities.
   - Includes modules for MD5 hashing, path manipulation, simple filtering logic, file system interactions (`fs/`), Git repository operations (`git/`), and data queue implementations (`queue/`).

6. **Vendor**
   - Directory containing vendored third-party Go module dependencies. This ensures that the project uses specific versions of external libraries for consistent and reproducible builds.

## Development Notes

- All Go source files should follow standard Go project layout
- Each component should have its own tests in a `_test.go` file
- Dependencies are managed using Go modules
- Configuration is handled through YAML files
