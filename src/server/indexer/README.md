# Indexer Implementation

This directory contains the core indexing implementation for the Local Code Search Indexer. The indexer is responsible for scanning, parsing, and indexing files in workspaces.

## Architecture Overview

The indexer is implemented as a pipeline of three main components:

1. **Scanner** (`scanner.go`)
2. **Parser** (`parser.go`)
3. **Writer** (`writer.go`)

These components work together in a producer-consumer pattern to efficiently process files and build search indexes. The overall orchestration, including managing workspaces and specific file updates, is handled by `indexer.go`.

## Component Details

### 1. Scanner (`scanner.go`)

The scanner is responsible for:

- Traversing directories of a given workspace.
- Applying file filters (including .gitignore and custom patterns defined in workspace configuration).
- Identifying files to be processed based on these filters.
- Queueing identified files (as paths) for the parser.
- Reporting progress during a scan by logging the number of files found.

Key features:

- Processes one workspace at a time from an internal queue.
- Efficient directory traversal leveraging `fsutils.ListFiles`.
- Supports `.gitignore` rules (if enabled for the workspace) and custom include/exclude glob patterns.
- Tracks and logs the count of files added to the parsing queue for each workspace.

### 2. Parser (`parser.go`)

The parser handles:

- Reading content of individual files received from the scanner.
- Detecting changes by comparing modification times and content hashes against previously indexed versions (if any).
- Extracting and normalizing words from file content and also from their relative file paths.
- Preparing document objects (containing metadata and extracted words) for the writer.

Processing steps:

1. For each file path received, retrieve file metadata (size, modification time).
2. Skip processing if the file exceeds the configured `Server.MaxFileSize`.
3. If the file was previously indexed, compare its current modification time with the stored one. If unchanged, skip further processing for this file.
4. Read file content. If the content is not likely text-based (e.g., binary file), skip.
5. Generate a content hash from the file's content. If previously indexed and the hash is unchanged, skip.
6. Extract words from the content:
    - Use a primary regular expression (`[a-zA-Z0-9_][a-zA-Z0-9_-]+`) to find potential words.
    - Further split these words based on camelCase and snake_case conventions (e.g., "MyFile" becomes "My", "File").
    - Normalize all extracted words to lowercase.
    - Filter words based on length constraints (typically 3-80 characters).
    - Ensure only unique words (for this file's content) are kept.
7. Extract and process words from the relative file path using a similar normalization and filtering process.
8. Create a document structure containing the file's relative path, metadata (ID, size, modification time, hash), and the unique words from its content and path.
9. Queue this document structure for the `Writer`.

### 3. Writer (`writer.go`)

The writer manages:

- Receiving processed document structures from the parser.
- Batching these documents for efficient writing to the persistent storage.
- Invoking storage operations (create or update) via the `documents` package, which handles the actual interaction with Pebble DB.

Features:

- Operates asynchronously, decoupling parsing from direct storage writes.
- Batches documents by collecting a small number of documents or after a short timeout before processing.
- Delegates actual storage logic, index updates, transaction management, and detailed error handling during writes to the `documents` package.
- Distinguishes between new documents and updates to existing documents when calling `documents` package functions.

## Indexing Process

1. **Initialization** (Orchestrated by `indexer.Run` in `indexer.go`)

   - The `indexer.Run` function initializes and starts the Scanner, Parser, and Writer components, each in its own goroutine(s).
   - Scanner begins its main loop, ready to process workspaces added to its queue.
   - Parser workers (number defined by `Server.IndexWorkers` in configuration) are spawned, each ready to receive file processing tasks from the Scanner (via an internal channel in the Parser).
   - Writer begins its main loop, ready to receive processed documents from the Parser (via an internal channel). Storage (Pebble DB) initialization is typically handled by the `documents` package when it's first accessed.

2. **File Processing Pipeline**

   ```go
   Scanner -> Parser -> Writer -> (documents package) -> Storage (Pebble DB)
   ```

   - The `Scanner` is given a workspace to process. It traverses the workspace, applies filters, and sends paths of files to be indexed to the `Parser`.
   - A `Parser` worker receives a file path, performs change detection, reads content, extracts words, and creates a document structure.
   - The `Parser` then sends this document structure to the `Writer`.
   - The `Writer` batches these documents and uses functions from the `documents` package (e.g., `SaveNewDocuments`, `UpdateDocuments`) to persist them into Pebble DB.

3. **Change Detection** (Primarily within `parser.go`, with support from `indexer.go`)

   - **File Modification Time**: In `parser.go`, the current modification time of a file is compared against the stored modification time for an existing document. If they match, and other checks pass, the file might be skipped.
   - **Content Hash**: If modification times differ or it's a new file, `parser.go` computes a hash of the file's content. This hash is compared against any stored hash for an existing document. If they match, the file is skipped.
   - **File Existence/Type Changes**: `indexer.go` includes logic (e.g., `RefreshFileIfNeeded`, `AddOrSyncFile`) to handle cases where a tracked file path no longer exists, has become a directory, or a new file appears. This can lead to document removal or new document indexing.

4. **Word Processing** (Within `parser.go`'s `parseString` and `camelSnakeSplit` functions)

   - **Regex-based extraction**: Words are initially identified from content using a regular expression (e.g., `[a-zA-Z0-9_][a-zA-Z0-9_-]+`).
   - **Splitting and Normalization**: Identified character sequences are further processed:
     - Split based on camelCase and snake_case conventions (e.g., "CamelCaseWord" -> "Camel", "Case", "Word").
     - Normalized to lowercase.
   - **Filtering**: Words are filtered based on length (e.g., minimum 3, maximum 80 characters).
   - **Duplicate Removal**: Only unique words (per document, case-insensitively) are stored in the index for that document.

## Performance Optimizations

1. **Concurrency**

   - **Multiple Parser Workers**: The number of goroutines for parsing files is configurable (`Server.IndexWorkers`), allowing parallel processing of files.
   - **Batch Writing**: The `Writer` collects multiple documents before writing them to storage, reducing I/O overhead.
   - **Asynchronous Pipeline**: Scanner, Parser, and Writer operate as stages in a pipeline using Go channels, allowing them to work concurrently on different sets of data.

2. **Resource Management**

   - **File Size Limits**: Files exceeding a configurable size (`Server.MaxFileSize`) are skipped to prevent excessive memory usage and processing time.
   - **Worker Pool Sizing**: The number of parser workers can be tuned.
   - **Memory Considerations**: Primarily managed by processing files individually in parsers and by the file size limit. Content is read into memory for parsing.

3. **Efficiency**

   - **Change Detection**: By checking modification times and content hashes, the system avoids re-processing and re-indexing files that haven't changed.
   - **Incremental Updates**: Only new or modified files are fully processed and written to storage.
   - **Optimized Word Extraction**: Regex and string manipulations are used for word extraction, with efforts to normalize and store only relevant terms.

## Configuration

Key configuration options:

- `Server.IndexWorkers`: Number of parser workers
- `Server.MaxFileSize`: Maximum file size to index
- Filter patterns for includes/excludes

## Error Handling

The system aims to be robust by logging errors encountered at various stages. Generally, errors with specific files do not halt the entire indexing process for a workspace.

1. **File System Errors** (Primarily in `scanner.go` during traversal and `parser.go` during file access)

   - **Permission Issues**: Logged if a directory or file cannot be accessed.
   - **Missing Files**: If a file path queued for parsing is not found (e.g., deleted between scan and parse), an error is logged.
   - **Read Errors**: Errors during file content reading in the parser are logged.
   Typically, processing for that specific file is skipped, and the indexer moves on.

2. **Processing Errors**

   - **Parse Failures** (in `parser.go`):
     - If a file is skipped due to size limits or being identified as non-text, this is logged.
     - Errors during content hashing or word extraction are logged.
   - **Write Failures** (in `writer.go` via the `documents` package): Errors during storage operations (e.g., Pebble DB issues) are handled within the `documents` package, which logs them. The success of a batch write operation depends on this underlying layer.

3. **Resilience**

   - **Error Logging**: Comprehensive logging throughout the `indexer`, `parser`, `scanner`, and `writer` components helps in diagnosing issues.
   - **Skipping Problematic Items**: If a specific file encounters an unrecoverable error during its processing pipeline (scan, parse, or prepare for write), it is usually logged and skipped, allowing the indexing of other files to continue.
   - **Workspace State**: The `indexer.go` and `workspace` package manage the state of workspaces (e.g., last sync time). `indexer.go` also contains logic (e.g., `RefreshFileIfNeeded`) to remove documents from the index if their corresponding files are found to be deleted or inaccessible during refresh operations.
   - **Retry Mechanisms**: Explicit retry mechanisms are not a prominent feature within the scanner/parser/writer pipeline itself; resilience is primarily achieved by skipping problematic items and relying on future scans or sync operations to reconcile.

## Usage

1. **Starting the Indexer**

   ```go
   indexer.Run(wg)
   ```

2. **Adding Workspaces**

   ```go
   indexer.SyncIfNeeded(workspacePath)
   ```

3. **File Updates**

   ```go
   indexer.AddOrSyncFile(workspace, relPath)
   ```

## Development Guidelines

1. **Adding Features**

   - Follow pipeline architecture
   - Maintain component isolation
   - Add appropriate tests

2. **Performance Tuning**

   - Monitor worker utilization
   - Adjust batch sizes
   - Optimize filters

3. **Testing**

   - Unit tests for each component
   - Integration tests
   - Performance benchmarks
