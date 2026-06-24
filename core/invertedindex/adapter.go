package invertedindex

// adapter.go adapts the pebble-backed *Index to the storage-agnostic Indexer
// seam (indexer.go). It exists so a consumer written against Indexer can run on
// either implementation during the migration window; the go-forward production
// implementation is invertedstore.Store, which satisfies Indexer natively.
//
// One contract gap between *Index and Indexer is bridged here: async, on-worker
// writes. Indexer.Update is thread-safe and may be called from any goroutine;
// *Index.Update MUST run on the mpsc worker (it mutates the unlocked
// pendingWrites/pendingDeletes maps). The adapter enqueues the call onto the
// queue (AddFunc), so callers never need to be on the worker.
//
// Since #105 *Index owns its OWN forward map and its Update is the same 3-arg
// (tableId, docid, currentKeywords) shape as the seam — it diffs the current set
// against its stored forward map and retracts dropped keywords on its own. So the
// adapter forwards the keyword set verbatim (no lossy oldKeywords=nil shim): an
// empty/nil set is a correct delete, and a re-Update correctly retracts the
// doc's previously-indexed-but-now-dropped keywords. This makes the adapter a
// fully-correct alternate implementation, not just a migration shim.
type IndexerAdapter struct {
	*Index
}

// NewIndexerAdapter wraps idx so it satisfies Indexer. idx must already be
// started (invertedindex.New). A nil idx yields a nil adapter pointer; callers
// that may hold a nil index should guard before wrapping.
func NewIndexerAdapter(idx *Index) *IndexerAdapter {
	return &IndexerAdapter{Index: idx}
}

// Update enqueues an asynchronous re-(post) of the doc's current keywords onto
// the worker. An empty/nil keywords set is a delete: *Index.Update diffs the new
// set against its forward map, so it tombstones the doc's old postings on its
// own. Honors the Indexer contract that Update is thread-safe and never requires
// the worker.
func (a *IndexerAdapter) Update(tableId int, docid int64, keywords []string) {
	// Defensive copy: keywords may be reused/mutated by the caller after Update
	// returns, but the work runs LATER on the worker.
	var kw []string
	if len(keywords) > 0 {
		kw = append([]string(nil), keywords...)
	}
	a.q.AddFunc(func() error {
		a.Index.Update(tableId, docid, kw)
		return nil
	})
}

// CreateTable runs the inherited *Index.CreateTable on the worker (RunFunc) so it
// serializes behind any queued async Update tasks on the single shared worker.
// *Index.CreateTable touches only the db (GetIncrementalId/Put), not the
// non-thread-safe pendingWrites/pendingDeletes maps, but routing it through the
// queue keeps the seam uniform with invertedstore.Store (whose table ops also run
// on the worker) and matches DeleteTable's serialization. RunFunc (not AddFunc)
// preserves the synchronous Indexer.CreateTable contract: the caller blocks until
// the table id is allocated and returned.
func (a *IndexerAdapter) CreateTable(description string) (int, error) {
	var (
		id  int
		err error
	)
	rerr := a.q.RunFunc(func() error {
		id, err = a.Index.CreateTable(description)
		return nil
	})
	if rerr != nil {
		return 0, rerr
	}
	return id, err
}

// DeleteTable runs the inherited *Index.DeleteTable on the worker (RunFunc) so it
// serializes behind any queued async Update tasks on the single shared worker.
//
// This override is REQUIRED for correctness, not just uniformity: *Index.Update
// (which IndexerAdapter.Update enqueues via AddFunc) mutates the unlocked
// pendingWrites/pendingDeletes maps on the worker, and *Index.DeleteTable ->
// clearPendingWrites READS those same maps. Without this override DeleteTable
// would run synchronously on the CALLER goroutine (documents.Store.Delete hoists
// indexDeleteTable out of its own queue task to avoid the RunFunc-in-RunFunc
// deadlock), concurrently with a still-pending async Update draining on the worker
// — a Go map read/write data race that can panic ("concurrent map read and map
// write"). Routing DeleteTable through the SAME worker serializes it AFTER every
// previously-queued Update, so the maps have a single accessor. RunFunc (not
// AddFunc) preserves the synchronous Indexer.DeleteTable contract.
func (a *IndexerAdapter) DeleteTable(tableId int) error {
	return a.q.RunFunc(func() error { return a.Index.DeleteTable(tableId) })
}

// NewBatch returns an Indexer Batch that accumulates ops in memory and, on
// Commit, applies them as ONE synchronous worker task (RunFunc) looping Update —
// so the whole batch lands in a single worker turn (no per-op AddFunc churn).
func (a *IndexerAdapter) NewBatch() Batch {
	return &adapterBatch{a: a}
}

// adapterBatch is the invertedindex side of the Indexer.Batch seam. It buffers
// (tableId, docid, keywords) ops and applies them in one RunFunc on Commit.
type adapterBatch struct {
	a   *IndexerAdapter
	ops []adapterOp
}

type adapterOp struct {
	tableId  int
	docid    int64
	keywords []string
}

// Update appends a defensive copy of the op and returns the batch for chaining.
func (b *adapterBatch) Update(tableId int, docid int64, keywords []string) Batch {
	var kw []string
	if len(keywords) > 0 {
		kw = append([]string(nil), keywords...)
	}
	b.ops = append(b.ops, adapterOp{tableId: tableId, docid: docid, keywords: kw})
	return b
}

// Commit applies the buffered ops in order on the worker (one RunFunc) and spends
// the batch. An empty batch is a no-op.
func (b *adapterBatch) Commit() {
	if len(b.ops) == 0 {
		return
	}
	ops := b.ops
	b.ops = nil
	_ = b.a.q.RunFunc(func() error {
		for _, op := range ops {
			b.a.Index.Update(op.tableId, op.docid, op.keywords)
		}
		return nil
	})
}

// Compile-time assertions that the adapter and its batch satisfy the seam.
var (
	_ Indexer = (*IndexerAdapter)(nil)
	_ Batch   = (*adapterBatch)(nil)
)
