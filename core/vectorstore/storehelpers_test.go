package vectorstore

import (
	"sort"
	"testing"

	"github.com/codetrek/haystack/core/kv"
	"github.com/codetrek/haystack/core/kv/pebblekv"
)

// newTestKV opens a temp pebble store (cacheSize is int64 bytes; 16 MiB) closed
// on test cleanup.
func newTestKV(t *testing.T) kv.Store {
	t.Helper()
	store, err := pebblekv.Open(t.TempDir(), 16<<20)
	requireNoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// openTestStore opens a Store in a fresh temp dir over a fresh KV.
func openTestStore(t *testing.T, m Metric) *Store {
	t.Helper()
	s, err := Open(Options{Dir: t.TempDir(), KV: newTestKV(t), Metric: m})
	requireNoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// batchPutVecs Puts n random vectors (id = prefix+i for i in [0,n)) in ONE batch
// commit — one fsync instead of n — then records the docId-keyed oracle into vecs
// AFTER commit (s.idToDoc is only populated by Commit, not before). Drop-in for the
// per-Put `put := func(id,v){ s.Put(...); vecs[s.idToDoc[id]]=v }` setup pattern,
// for tests where the population is incidental setup (default maxSegSize, no per-Put
// assertion) so Batch+Seal reproduces the identical end state.
func batchPutVecs(t *testing.T, s *Store, prefix string, n int, randVec func() []float32, vecs map[int64][]float32) {
	t.Helper()
	b := s.NewBatch()
	ids := make([]string, n)
	vs := make([][]float32, n)
	for i := 0; i < n; i++ {
		ids[i] = prefix + itoa(i)
		vs[i] = randVec()
		b.Put(ids[i], vs[i], nil)
	}
	requireNoError(t, b.Commit())
	for i := 0; i < n; i++ {
		vecs[s.idToDoc[ids[i]]] = append([]float32(nil), vs[i]...)
	}
}

// bruteForceKNN is the ground-truth oracle: it computes the metric distance for
// every candidate and returns the k nearest docIds ascending by distance (ties
// by docId).
func bruteForceKNN(m Metric, q []float32, vecs map[int64][]float32, k int) []int64 {
	pq, _ := m.prepare(q)
	type hit struct {
		doc int64
		d   float32
	}
	var hits []hit
	for doc, raw := range vecs {
		stored, _ := m.prepare(raw)
		hits = append(hits, hit{doc, m.distance(stored, pq)})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].d != hits[j].d {
			return hits[i].d < hits[j].d
		}
		return hits[i].doc < hits[j].doc
	})
	if k > len(hits) {
		k = len(hits)
	}
	out := make([]int64, k)
	for i := 0; i < k; i++ {
		out[i] = hits[i].doc
	}
	return out
}

// faultKV wraps a kv.Store to inject failures into the two operations the
// vectorstore relies on through idtable: a Get error (to fail idtable.New's
// startup read) and an IsClosed() == true (to fail Allocator.GetId, which the
// store reaches via docIDForAlloc on the Put and replay paths). All other Store
// methods are promoted from the embedded real store.
type faultKV struct {
	kv.Store
	getErr   error
	isClosed bool
}

func (f *faultKV) Get(key []byte) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.Store.Get(key)
}

func (f *faultKV) IsClosed() bool {
	if f.isClosed {
		return true
	}
	return f.Store.IsClosed()
}
