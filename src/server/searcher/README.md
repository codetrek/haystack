# Search Implementation

This directory contains the core search implementation for the Local Code Search Indexer. The search system is designed to efficiently find content across indexed files using a combination of inverted indexes and content scanning.

## Architecture Overview

The search system consists of two main components:

1. **Search Engine** (`simple_content_search_engine.go`) - [Complete]

2. **Searcher** (`searcher.go`) - [Complete]

## Search Process

### 1. Document Collection

The search engine uses a two-phase approach:

1. **Index Lookup**

   - Uses inverted indexes for initial document filtering
   - Supports prefix-based lookups
   - Handles wildcard patterns

2. **Content Scanning**

   - Line-by-line content matching
   - Regular expression based matching
   - Position tracking for matches

### 2. Result Processing

Results are processed with the following considerations:

1. **Filtering**

   - Path-based filtering
   - Include/exclude patterns
   - File type filtering

2. **Limits**

   - Maximum results per file
   - Total result limit
   - Timeout handling

3. **Ranking**

   - Match position
   - Match frequency
   - File relevance

## Search Algorithm

### 1. Query Processing

The search algorithm follows these steps:

1. **Query Compilation**

   ```go
   // Example: "hello world" -> ["hello", "world"]
   func (q *SimpleContentSearchEngine) Compile(query string, caseSensitive bool) error {
       // 1. Split query into terms
       // 2. Generate regex patterns
       // 3. Build OR/AND clauses
   }
   ```

2. **Term Processing**

   - Extract word prefixes for index lookup
   - Generate regex patterns for content matching
   - Handle wildcards and case sensitivity

### 2. Document Collection Algorithm

The document collection algorithm involves multiple stages to efficiently gather and filter documents based on the query structure:

1. **Term-Level Document Retrieval (`SimpleContentSearchEngineTerm`)**

   - For each individual term in the query, this stage retrieves an initial set of documents.
   - It utilizes the inverted index and the term's prefix for fast lookups.

   ```go
   func (q *SimpleContentSearchEngineTerm) CollectDocuments(workspaceId int) invertedindex.SearchResult {
       // 1. Use prefix to lookup documents in the inverted index for the given workspaceId
       // 2. Return initial document set (invertedindex.SearchResult)
   }
   ```

   - Key aspects:
     - Uses inverted index for fast document filtering.
     - Supports prefix-based optimization.

2. **AND Clause Document Collection (`SimpleContentSearchEngineAndClause`)**

   - If a query involves multiple terms combined with an AND operator (implicitly or explicitly), this stage processes them.
   - It collects documents for each term within the AND clause (using the term-level retrieval above).
   - Then, it computes the intersection of these document sets, ensuring that only documents containing all terms in the AND clause are retained.

   ```go
   func (q *SimpleContentSearchEngineAndClause) CollectDocuments(workspaceId int) (*invertedindex.SearchResult, error) {
       // 1. Collect documents for each AndTerm using SimpleContentSearchEngineTerm.CollectDocuments
       // 2. Merge results by intersecting the document sets
       // 3. Return the intersected document set (*invertedindex.SearchResult)
   }
   ```

   - Key aspects:
     - Performs intersection of document sets for AND operations.

3. **OR Clause Document Collection (`SimpleContentSearchEngine`)**

   - If a query involves multiple OR clauses (where each clause can be a single term or an AND clause), this top-level stage handles it.
   - It collects documents for each OR clause (using the AND clause document collection above).
   - Then, it computes the union of these document sets, ensuring that documents matching any of the OR clauses are included.

   ```go
   func (q *SimpleContentSearchEngine) CollectDocuments() (*invertedindex.SearchResult, error) {
       // 1. Collect documents for each OrClause using SimpleContentSearchEngineAndClause.CollectDocuments
       // 2. Merge results by unioning the document sets
       // 3. Return the final combined document set (*invertedindex.SearchResult)
   }
   ```

   - Key aspects:
     - Performs union of document sets for OR operations.
     - Optimized for handling large document sets resulting from multiple clauses.

### 3. Content Matching Algorithm

The content matching process:

1. **Line-by-Line Processing**

   ```go
   func (q *SimpleContentSearchEngine) IsLineMatch(line string) [][]int {
       // 1. Apply regex patterns
       // 2. Track match positions
       // 3. Return match ranges
   }
   ```

2. **Match Optimization**

   - Early termination for long lines
   - Batch processing for multiple patterns
   - Position tracking for highlighting

### 4. Result Ranking

Results are ranked based on:

1. **Relevance Factors** - (WIP)

   - Match position in file
   - Match frequency
   - File importance

2. **Filtering Criteria**

   - Path-based filtering
   - File type preferences
   - Custom include/exclude rules

### 5. Performance Optimizations

1. **Index Usage**

   - Prefix-based document filtering
   - Efficient term lookup
   - Caching of common queries

2. **Memory Management**

   - Streaming results
   - Batch processing
   - Resource limits

3. **Search Optimization**

   - Early termination
   - Parallel processing
   - Result limits

### 6. Algorithm Complexity

1. **Time Complexity**

   - Index lookup: O(log n)
   - Document merging: O(n)
   - Content matching: O(m * k)
     - m: number of documents
     - k: average document size

2. **Space Complexity**

   - Index storage: O(n)
   - Result storage: O(k)
     - k: result limit

### 7. Example Search Flow

```go
// 1. Query compilation
engine.Compile("hello world", false, false)

// 2. Document collection
docs := engine.CollectDocuments()

// 3. Content matching
for doc := range docs {
    matches := engine.IsLineMatch(doc.Content)
    // Process matches
}

// 4. Result ranking and filtering
results := rankAndFilter(matches)
```

## Search Engine Implementation

### SimpleContentSearchEngine

The main search engine implementation:

```go
type SimpleContentSearchEngine struct {
    MaxWildcardLength  int
    MaxKeywordDistance int
    Workspace          *workspace.Workspace
    OrClauses          []*SimpleContentSearchEngineAndClause
}

type SimpleContentSearchEngineAndClause struct {
    Regex    *regexp.Regexp
    AndTerms []*SimpleContentSearchEngineTerm
}

type SimpleContentSearchEngineTerm struct {
    Pattern string
    Prefix  string
}
```

Key features:

- Boolean query support (AND/OR)
- Regular expression matching
- Prefix-based optimization
- Case sensitivity control

## Performance Optimizations

1. **Index Usage**

   - Prefix-based document filtering
   - Efficient term lookup
   - Caching of common queries

2. **Content Scanning**

   - Line-by-line processing
   - Early termination
   - Batch processing

3. **Memory Management**

   - Streaming results
   - Result limits
   - Resource cleanup

## Search Features

1. **Query Types**

   - Simple word search
   - Phrase search
   - Boolean operations
   - Wildcard patterns

2. **Filtering**

   - Path-based filtering
   - File type filtering
   - Custom include/exclude patterns

3. **Results**

   - Line-level matches
   - Match highlighting
   - Context display
   - Result limits

## Configuration

Key configuration options:

- `Server.Search.Limit`: Result limits
- `Server.Search.MaxWildcardLength`: Wildcard pattern limits
- `Server.Search.MaxKeywordDistance`: Phrase matching distance

## Usage Example

```go
// Create search engine
engine := NewSimpleContentSearchEngine(workspace)

// Compile query
err := engine.Compile("hello world", false, false)

// Search content
results, truncated := SearchContent(workspace, &types.SearchContentRequest{
    Query: "hello world",
    Filters: &types.SearchFilters{
        Path: "/src",
    },
})
```

## Development Guidelines

1. **Adding Features**

   - Maintain search engine compatibility
   - Add appropriate tests
   - Consider performance impact

2. **Performance Tuning**

   - Monitor index usage
   - Optimize regex patterns
   - Adjust batch sizes

3. **Testing**

   - Integration tests for search
   - Performance benchmarks
   - Edge case handling
