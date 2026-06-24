package documents

import (
	"encoding/binary"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/codetrek/haystack/core/idtable"
	"github.com/codetrek/haystack/core/invertedindex"
	"github.com/codetrek/haystack/core/kv/pebblekv"
	"github.com/codetrek/haystack/core/queue"
)

// queueAsyncUpdateIndexer reproduces the production seam where the inverted index
// shares ONE mpsc worker with documents.Store and its per-doc Update is
// ASYNCHRONOUS — i.e. it enqueues an apply onto that shared queue (q.AddFunc),
// exactly like invertedstore.Store.Update and invertedindex.IndexerAdapter.Update.
//
// The hazard it guards: documents.Store.Save/Update/DeleteDocument run their kv
// writes inside s.q.RunFunc (occupying the single worker). If the index
// notification (Update / Batch.Commit, each an AddFunc = channel send) were made
// from INSIDE that worker task, then once the channel buffer (default 100) fills,
// the worker would block sending to a queue only it can drain → permanent
// deadlock. The fix hoists the index notification OUTSIDE the worker task; this
// indexer makes the regression observable by saving > buffer docs.
type queueAsyncUpdateIndexer struct {
	q queue.Queue

	mu      sync.Mutex
	applied map[int64]int // docid -> count of applied Updates
	nextID  int
}

func newQueueAsyncUpdateIndexer(q queue.Queue) *queueAsyncUpdateIndexer {
	return &queueAsyncUpdateIndexer{q: q, applied: map[int64]int{}}
}

func (x *queueAsyncUpdateIndexer) Search(int, string, int, func(string) bool) invertedindex.SearchResult {
	return invertedindex.SearchResult{}
}
func (x *queueAsyncUpdateIndexer) GetDocs(int, string) invertedindex.SearchResult {
	return invertedindex.SearchResult{}
}

// Update enqueues the apply asynchronously on the SHARED queue (AddFunc), like the
// production stores. The apply just records the docid.
func (x *queueAsyncUpdateIndexer) Update(tableId int, docid int64, keywords []string) {
	x.q.AddFunc(func() error {
		x.mu.Lock()
		x.applied[docid]++
		x.mu.Unlock()
		return nil
	})
}

func (x *queueAsyncUpdateIndexer) NewBatch() invertedindex.Batch {
	return &queueAsyncBatch{x: x}
}

func (x *queueAsyncUpdateIndexer) CreateTable(string) (int, error) {
	var id int
	err := x.q.RunFunc(func() error {
		x.nextID++
		id = x.nextID
		return nil
	})
	return id, err
}

func (x *queueAsyncUpdateIndexer) DeleteTable(int) error {
	return x.q.RunFunc(func() error { return nil })
}

func (x *queueAsyncUpdateIndexer) CloseAndWait() {}

func (x *queueAsyncUpdateIndexer) appliedCount(docid int64) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.applied[docid]
}

// queueAsyncBatch enqueues ONE AddFunc PER op on Commit (not collapsed to a single
// task). This is deliberately the worst case for the buffer: it lets a Commit of N
// ops overrun the channel buffer all by itself, so the guard test catches BOTH a
// per-doc Update loop AND a Batch.Commit being made from inside the worker task —
// either overruns the buffer once N > the buffer depth. (invertedstore's real
// Batch.Commit collapses to one AddFunc, but the seam contract — never enqueue
// from inside the worker — must hold regardless of how a given Indexer chunks its
// async applies, so the test exercises the strict case.)
type queueAsyncBatch struct {
	x   *queueAsyncUpdateIndexer
	ops []int64
}

func (b *queueAsyncBatch) Update(tableId int, docid int64, keywords []string) invertedindex.Batch {
	b.ops = append(b.ops, docid)
	return b
}

func (b *queueAsyncBatch) Commit() {
	if len(b.ops) == 0 {
		return
	}
	ops := b.ops
	b.ops = nil
	for _, d := range ops {
		d := d
		b.x.q.AddFunc(func() error {
			b.x.mu.Lock()
			b.x.applied[d]++
			b.x.mu.Unlock()
			return nil
		})
	}
}

var _ invertedindex.Indexer = (*queueAsyncUpdateIndexer)(nil)

// docIDString encodes i as the canonical 8-byte idtable docid string the document
// store expects (matches idtable.EncodeId: GetId returns this 8-byte form).
func docIDString(i int) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(i))
	return string(b[:])
}

// TestSaveNewDocuments_NoDeadlockWithQueueAsyncIndexer guards the documents↔Indexer
// write seam: a batch larger than the mpsc channel buffer (default 100) must NOT
// deadlock when the indexer's Update/Commit enqueues onto the SHARED queue. Before
// the fix that hoisted the index notification out of SaveNewDocuments' s.q.RunFunc
// body, the worker would block sending to a queue only it could drain. The
// watchdog turns the hang into a failure.
func TestSaveNewDocuments_NoDeadlockWithQueueAsyncIndexer(t *testing.T) {
	tempDir := t.TempDir()
	db, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	q := queue.NewMpsc("TestSaveNoDeadlock")
	q.Start()
	defer q.Stop()

	idx := newQueueAsyncUpdateIndexer(q)
	st, err := New(db, q, idx, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer st.CloseAndWait()

	if err := st.Create(7, "ws"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 250 docs >> the 100-deep channel buffer — the regression fires only once the
	// buffer is overrun mid-task.
	const n = 250
	docs := make([]*Document, 0, n)
	for i := 0; i < n; i++ {
		docs = append(docs, &Document{ID: docIDString(i + 1), RelPath: "f", Words: []string{"w"}})
	}

	done := make(chan error, 1)
	go func() { done <- st.SaveNewDocuments(7, docs) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SaveNewDocuments returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("documents.Store.SaveNewDocuments deadlocked: the index notification was enqueued from inside the worker task and overran the channel buffer")
	}

	// Flush the queue so the async index applies have run, then confirm every doc
	// was indexed exactly once (the batch was committed and applied).
	q.RunFunc(func() error { return nil })
	for i := 0; i < n; i++ {
		docid := idtable.DecodeId(docIDString(i + 1))
		if got := idx.appliedCount(docid); got != 1 {
			t.Fatalf("docid %d applied %d times, want 1", docid, got)
		}
	}
}

// TestDeleteDocument_NoDeadlockWithQueueAsyncIndexer guards the single-doc delete
// path: DeleteDocument's index removal (Update with nil keywords) must also be
// hoisted out of its worker task. We seed one doc, then delete it; with the
// pre-fix code DeleteDocument's in-task Update would enqueue onto the shared queue
// from the worker — benign at n=1 but a latent contract violation. This asserts
// the delete notification reaches the index (applied count increments) without
// hanging.
func TestDeleteDocument_NoDeadlockWithQueueAsyncIndexer(t *testing.T) {
	tempDir := t.TempDir()
	db, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	q := queue.NewMpsc("TestDeleteDocNoDeadlock")
	q.Start()
	defer q.Stop()

	idx := newQueueAsyncUpdateIndexer(q)
	st, err := New(db, q, idx, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer st.CloseAndWait()

	if err := st.Create(7, "ws"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	docID := docIDString(42)
	if err := st.SaveNewDocuments(7, []*Document{{ID: docID, RelPath: "f", Words: []string{"w"}}}); err != nil {
		t.Fatalf("SaveNewDocuments: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- st.DeleteDocument(7, docID) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("DeleteDocument returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("documents.Store.DeleteDocument deadlocked")
	}

	q.RunFunc(func() error { return nil })
	// One Save apply + one Delete apply => 2 total applies for this docid.
	if got := idx.appliedCount(idtable.DecodeId(docID)); got != 2 {
		t.Fatalf("docid applied %d times, want 2 (save + delete)", got)
	}
}
