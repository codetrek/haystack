package documents

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/codetrek/haystack/core/invertedindex"
	"github.com/codetrek/haystack/core/kv/pebblekv"
	"github.com/codetrek/haystack/core/queue"
)

// queueTableOpsIndexer is a minimal Indexer whose CreateTable/DeleteTable block
// on the SAME mpsc worker (via q.RunFunc), exactly like invertedstore.Store's
// table ops. It reproduces the production wiring where documents.Store and the
// inverted index share one queue, so that Store.Delete (which itself runs on the
// queue) must NOT call DeleteTable from inside a queue task — that would nest
// RunFunc-in-RunFunc and deadlock the single worker.
//
// Search/GetDocs/Update/NewBatch are unused by these tests and are no-ops.
type queueTableOpsIndexer struct {
	q      queue.Queue
	nextID int
}

func (x *queueTableOpsIndexer) Search(int, string, int, func(string) bool) invertedindex.SearchResult {
	return invertedindex.SearchResult{}
}
func (x *queueTableOpsIndexer) GetDocs(int, string) invertedindex.SearchResult {
	return invertedindex.SearchResult{}
}
func (x *queueTableOpsIndexer) Update(int, int64, []string) {}
func (x *queueTableOpsIndexer) NewBatch() invertedindex.Batch {
	return noopBatch{}
}
func (x *queueTableOpsIndexer) CloseAndWait() {}

func (x *queueTableOpsIndexer) CreateTable(string) (int, error) {
	var id int
	err := x.q.RunFunc(func() error {
		x.nextID++
		id = x.nextID
		return nil
	})
	return id, err
}

func (x *queueTableOpsIndexer) DeleteTable(int) error {
	return x.q.RunFunc(func() error { return nil })
}

type noopBatch struct{}

func (noopBatch) Update(int, int64, []string) invertedindex.Batch { return noopBatch{} }
func (noopBatch) Commit()                                         {}

var _ invertedindex.Indexer = (*queueTableOpsIndexer)(nil)

// TestDelete_NoDeadlockWithQueueBlockingIndexer guards the documents↔Indexer
// seam contract from design §4/§6: a synchronous index table op (RunFunc on the
// shared worker) must not be invoked from inside Store.Delete's own queue task.
// Before the fix that hoisted indexDeleteTable out of the RunFunc body, this
// test would hang (the worker waits on itself). The t.Fatal-on-timeout watchdog
// turns that hang into a failure instead of a stuck test run.
func TestDelete_NoDeadlockWithQueueBlockingIndexer(t *testing.T) {
	tempDir := t.TempDir()
	db, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	q := queue.NewMpsc("TestDeleteNoDeadlock")
	q.Start()
	defer q.Stop()

	idx := &queueTableOpsIndexer{q: q}
	st, err := New(db, q, idx, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer st.CloseAndWait()

	if err := st.Create(7, "ws"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- st.Delete(7) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Delete returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("documents.Store.Delete deadlocked: a queue-blocking Indexer.DeleteTable was called from inside Store.Delete's own queue task")
	}
}
