# Storage Implementation

This directory contains the core storage implementation for the Local Code Search Indexer. It manages how documents and their indexes are stored, retrieved, and maintained.

## Storage Architecture

The storage system is built on top of PebbleDB, a high-performance key-value store. The implementation uses a custom key-value schema to efficiently store and retrieve document data and indexes.

### Key Components

1. **Storage Engine (`storage.go`)**
    * Manages the PebbleDB instance for document storage.
    * Handles database initialization and shutdown.
    * Provides core storage operations.

2. **Codec Implementation (`codec.go`)**
    * Defines encoding/decoding for document-related keys and values using byte constants from `core/storage/types.go`.
    * Manages serialization of document metadata and content pointers.
    * Uses key type constants like `KeyTypeDocMeta`, `KeyTypeDocWords`, `KeyTypeDocPath`, and `KeyTypeDocWorkspace`.

3. **Document Management (`document.go`)**
    * Defines the `Document` structure.
    * Provides functions for creating, retrieving, and deleting documents.
    * Handles storage of document metadata and associated words.

4. **Document Search Utilities (`search.go`)**
    * Offers functions to get document paths (`GetDocumentPath`).
    * Allows scanning of files within a workspace context (`ScanFiles`).

5. **Batch Write Operations (`batch_write.go`)**
    * Defines constants (e.g., `MaxBatchSize`) and utility functions for creating PebbleDB batches.

6. **Internal Document Handling (`document_internal.go`)**
    * Contains helper functions like `saveDocument` for internal document persistence logic.

## Key-Value Schema

The storage system uses byte-prefixed keys for different data types. These prefixes are constants defined in `server/core/storage/types.go`.

* `KeyTypeDocWorkspace` (byte `10`): Document-related workspace metadata.
* `KeyTypeDocMeta` (byte `12`): Document metadata.
* `KeyTypeDocWords` (byte `11`): Document words (a representation of words).
* `KeyTypeDocPath` (byte `13`): Document path information.

### Key Formats

Keys are constructed by prefixing a byte constant (representing the key type) to the specific identifier(s).

1. **Document Workspace Keys**
   The key is formed using `KeyTypeDocWorkspace` followed by the encoded workspace ID.
   Example structure: `byte(KeyTypeDocWorkspace){workspaceid_encoded}`

2. **Document Metadata Keys**
   The key is formed using `KeyTypeDocMeta` followed by the encoded workspace ID and document ID.
   Example structure: `byte(KeyTypeDocMeta){workspaceid_encoded}|{docid_encoded}`

3. **Document Words Keys**
   The key is formed using `KeyTypeDocWords` followed by the encoded workspace ID and document ID.
   Example structure: `byte(KeyTypeDocWords){workspaceid_encoded}|{docid_encoded}`

4. **Document Path Keys**
   The key is formed using `KeyTypeDocPath` followed by the encoded workspace ID and document ID.
   Example structure: `byte(KeyTypeDocPath){workspaceid_encoded}|{docid_encoded}`

## Data Structures

1. **Document Storage**
   * Metadata (JSON format)
   * Content (compressed text)
   * Word positions
   * Path information

2. **Index Storage**
   * Inverted index for keywords
   * Document frequency counts
   * Position lists
   * Path-based indexes

## Performance Considerations

1. **Write Optimization**
   * Batched writes
   * Periodic flushing
   * Write-ahead logging

2. **Read Optimization**
   * Key prefix scanning
   * Caching frequently accessed data
   * Compression for large values

3. **Space Optimization**
   * Efficient key encoding
   * Value compression
   * Garbage collection

## Implementation Details

1. **Storage Initialization**
   * Creates necessary directories
   * Initializes PebbleDB
   * Sets up version tracking
   * Starts background tasks

2. **Data Access**
   * Thread-safe operations
   * Transaction support
   * Error handling
   * Recovery mechanisms

3. **Maintenance**
   * Automatic compaction
   * Version upgrades
   * Data migration
   * Backup support

## Usage Guidelines

1. **Storage Operations**
   * Use provided interfaces
   * Handle errors appropriately
   * Clean up resources
   * Monitor performance

2. **Data Management**
   * Regular backups
   * Version control
   * Space monitoring
   * Performance tuning

3. **Development**
   * Follow key schema
   * Maintain backward compatibility
   * Test thoroughly
   * Document changes
