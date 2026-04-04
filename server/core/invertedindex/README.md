# Inverted Index Design

This document outlines the design of the inverted index component within Haystack.

## Overview

The inverted index is a core data structure used for efficient keyword-based search. It maps terms (keywords) to the documents that contain them. This allows for fast retrieval of documents relevant to a search query.

## Key Components

* **`invertedindex.go`**: Defines the main `InvertedIndex` struct and its primary methods for adding documents and searching.
* **`codec.go`**: Handles the encoding and decoding of index data for storage.
* **`storage.go`**: Manages the persistence of the inverted index, likely interacting with a key-value store (e.g., PebbleDB).
* **`batch_write.go` / `pending_writes.go`**: Implements mechanisms for batching writes to the index to improve performance.
* **`keywords_merger.go`**: Logic for merging keyword lists or posting lists.

## Data Structure

The inverted index typically consists of:

1. **A Term Dictionary**: A collection of all unique terms (keywords) found in the indexed documents.
2. **Posting Lists**: For each term in the dictionary, a list of document identifiers (and potentially positions or frequencies of the term within those documents) that contain the term.

```text
TermA -> [Doc1, Doc3, Doc5]
TermB -> [Doc2, Doc3, Doc4]
...
```

## Indexing Process

1. **Index Update**: For each term extracted from a document:
    * If the term is new, it's added to the term dictionary.
    * The document's identifier is added to the posting list associated with the term.

## Search Process

1. **Query Parsing**: The search query is parsed into terms.
2. **Term Lookup**: Each query term is looked up in the term dictionary to retrieve its corresponding posting list.
3. **Posting List Operations**:
    * For multi-term queries, operations like intersection (for AND queries) or union (for OR queries) are performed on the retrieved posting lists.
    * The result is a list of document identifiers that match the query.
4. **Document Retrieval**: The matching document identifiers are used to fetch the actual documents.

## Storage Considerations

* The `codec.go` and `storage.go` files suggest that the index is persisted to disk.
* Efficient serialization and deserialization of index data are crucial for performance.
* Compression techniques might be used to reduce storage space.

## Concurrency and Batching

* `batch_write.go` and `pending_writes.go` indicate that updates to the index are likely batched to optimize write performance and manage concurrency. This helps in reducing I/O operations and contention on the index.

## Future Considerations / Potential Enhancements

* Term frequency and inverse document frequency (TF-IDF) for ranking search results.
* Support for more complex query operators.
* Advanced index merging strategies.
