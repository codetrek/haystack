# Storage Implementation

This directory contains the core storage implementation for the Local Code Search Indexer. It manages how documents and their indexes are stored, retrieved, and maintained.

## Storage Architecture

The storage system is built on top of PebbleDB, a high-performance key-value store. The implementation uses a custom key-value schema to efficiently store and retrieve document data and indexes.

### Key Components

1. **Storage Engine (`storage.go`)**
   - Manages the PebbleDB instance
   - Handles database initialization and shutdown
   - Implements write batching and flushing
   - Provides version management

2. **Codec Implementation (`codec.go`)**
   - Defines the key-value encoding scheme
   - Implements serialization/deserialization
   - Manages key prefixes and formats

3. **Document Storage (`document.go`)**
   - Handles document metadata storage
   - Manages document content storage
   - Implements document versioning

4. **Search Index (`search.go`)**
   - Stores inverted indexes
   - Manages keyword-document mappings
   - Implements search result caching

5. **Workspace Management (`workspace.go`)**
   - Handles workspace metadata
   - Manages workspace-document relationships
   - Implements workspace isolation

## Key-Value Schema

The storage system uses the following key prefixes:

- `ws:` - Workspace metadata
- `dm:` - Document metadata
- `dw:` - Document words/content
- `dp:` - Document path words
- `kw:` - Keyword indexes
- `pw:` - Path word indexes

### Key Formats

1. **Workspace Keys**
   ```
   ws:{workspaceid}
   ```

2. **Document Metadata Keys**
   ```
   dm:{workspaceid}|{docid}
   ```

3. **Document Words Keys**
   ```
   dw:{workspaceid}|{docid}
   ```

4. **Keyword Index Keys**
   ```
   kw:{workspaceid}|{keyword}|{doccount}|{docshash}
   ```

## Data Structures

1. **Document Storage**
   - Metadata (JSON format)
   - Content (compressed text)
   - Word positions
   - Path information

2. **Index Storage**
   - Inverted index for keywords
   - Document frequency counts
   - Position lists
   - Path-based indexes

## Performance Considerations

1. **Write Optimization**
   - Batched writes
   - Periodic flushing
   - Write-ahead logging

2. **Read Optimization**
   - Key prefix scanning
   - Caching frequently accessed data
   - Compression for large values

3. **Space Optimization**
   - Efficient key encoding
   - Value compression
   - Garbage collection

## Implementation Details

1. **Storage Initialization**
   - Creates necessary directories
   - Initializes PebbleDB
   - Sets up version tracking
   - Starts background tasks

2. **Data Access**
   - Thread-safe operations
   - Transaction support
   - Error handling
   - Recovery mechanisms

3. **Maintenance**
   - Automatic compaction
   - Version upgrades
   - Data migration
   - Backup support

## Usage Guidelines

1. **Storage Operations**
   - Use provided interfaces
   - Handle errors appropriately
   - Clean up resources
   - Monitor performance

2. **Data Management**
   - Regular backups
   - Version control
   - Space monitoring
   - Performance tuning

3. **Development**
   - Follow key schema
   - Maintain backward compatibility
   - Test thoroughly
   - Document changes
