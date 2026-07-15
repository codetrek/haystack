# GitHub Copilot Instructions

## User Authority

The user is the ultimate authority on what changes should be made to their code. Your suggestions must align with their instructions and preferences.

## Project Knowledge: Haystack

### Project Overview
Haystack is a local code search indexer tool designed to create and query search indexes on local code repositories. It enables developers to find content across large codebases efficiently.

### Core Components
- **Client**: Interface for users to submit search queries and manage workspaces
  - Handles search operations
  - Manages server communication
  - Provides workspace management functions

- **Server**: Backend responsible for indexing and searching
  - **Core**: Handles document processing and storage
    - Parser: Processes and validates documents
    - Fulltext: Manages document persistence (uses Pebble DB)
  - **Indexer**: Creates and maintains search indexes
    - Scanner: Analyzes code repositories
    - Writer: Writes index data
  - **Searcher**: Processes search queries
    - Query Parser: Interprets search syntax
    - Search Engine: Executes search operations

- **Shared Components**:
  - Running: Manages runtime environment
  - Types: Defines common data structures

- **Utils**: Helper functions for file operations, git integration, etc.

### Architecture Patterns
- The codebase follows Go module structure
- Uses client-server architecture
- Implements workspace-based indexing approach
- Leverages Git integration for repository analysis

### Key Technologies
- Go (Golang)
- Pebble DB (storage)
- Git integration

This context should help you provide more relevant and aligned suggestions when working with the Haystack codebase.
