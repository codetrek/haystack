package invertedindex

// indexer.go defines the storage-agnostic seam that decouples consumers
// (documents.Store, engine, the root searcher/symbols) from the concrete
// inverted-index implementation. Both invertedindex (via IndexerAdapter) and
// invertedstore.Store satisfy Indexer, so a consumer can be migrated from the
// pebble-backed invertedindex to the segment-based invertedstore by swapping
// the constructed value with no consumer-side code change (design §4 "Drop-in
// seam").
//
// Why the interface lives HERE (not in a new leaf package): both consumers and
// engine already import invertedindex and use invertedindex.SearchResult
// directly; invertedstore may import invertedindex without a cycle (invertedindex
// does not import invertedstore). Keeping the seam here is the minimal change —
// invertedstore.SearchResult becomes a type alias of invertedindex.SearchResult
// (search.go in invertedstore), so both implementations return the IDENTICAL
// named type and engine/searcher keep compiling against invertedindex.SearchResult.

// Batch is the storage-agnostic bulk-ingest handle returned by Indexer.NewBatch.
// It amortizes many per-document Updates into one applied unit. The concrete
// types (invertedstore.Batch, invertedindex's adapter batch) implement it; the
// interface names no concrete pointer so both can satisfy one Indexer.
//
// Update appends a (tableId, docid, keywords) op and returns the batch for
// chaining; keywords is the doc's CURRENT full keyword set, empty ⇒ delete.
// Commit applies the accumulated ops (asynchronously for invertedstore; via a
// single RunTask for the invertedindex adapter). A committed batch is spent.
type Batch interface {
	Update(tableId int, docid int64, keywords []string) Batch
	Commit()
}

// Indexer is the inverted-index seam consumed by documents.Store, engine, and
// (in the root module) the searcher and symbols stores. It is exactly the
// invertedstore.Store public surface (design §4): reads are thread-safe and
// snapshot-direct; writes are thread-safe and asynchronous (no "must be on the
// worker" contract); table ops are synchronous.
//
// NOTE the Update signature: it takes ONLY the doc's current keyword set, NO
// oldKeywords. The store owns the forward map and diffs against it (§8), so the
// caller cannot drift from a stale old-keywords arg — and documents.Store can
// drop its doc-words machinery. invertedindex's *Index already owns its own
// forward map since #105, so its Update is the same 3-arg shape; the
// IndexerAdapter only adds the async-enqueue/serialization the seam requires.
type Indexer interface {
	// Search returns the union of docids whose keywords have query as a prefix
	// (lower-cased) in the table; filterKeyword (if non-nil) gates each keyword;
	// limit caps distinct docids (<= 0 = unlimited).
	Search(tableId int, query string, limit int, filterKeyword func(string) bool) SearchResult

	// GetDocs returns the docids stored under the EXACT keyword key in the table.
	GetDocs(tableId int, key string) SearchResult

	// Update sets a doc's CURRENT full keyword set (empty ⇒ delete). Thread-safe
	// and asynchronous.
	//
	// DEADLOCK CAUTION: the production indexers (invertedstore.Store,
	// IndexerAdapter) implement Update/NewBatch().Commit by ENQUEUEing the apply
	// onto a shared mpsc worker (a blocking channel send). A consumer that owns its
	// kv writes via that SAME worker (documents.Store, symbols) MUST NOT call Update
	// or commit a Batch from INSIDE its own worker task: the send would block on a
	// queue only that worker can drain, deadlocking once the channel buffer fills on
	// a large batch. Hoist the notification OUTSIDE the worker task. See
	// documents.Store.indexDocuments / symbols.replayIndexUpdates and their
	// save_no_deadlock_test.go guards.
	Update(tableId int, docid int64, keywords []string)

	// NewBatch starts a bulk-ingest batch bound to this indexer.
	NewBatch() Batch

	// CreateTable allocates a new keyword-namespace table and returns its id.
	CreateTable(description string) (int, error)

	// DeleteTable drops a table and (eventually) reclaims its bytes.
	DeleteTable(tableId int) error

	// CloseAndWait flushes pending work and releases resources.
	CloseAndWait()
}
