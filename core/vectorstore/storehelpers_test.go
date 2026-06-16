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
