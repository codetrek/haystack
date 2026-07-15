package invertedindex

import (
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

// countRowsIdx counts stored rows whose decoded (tableId, keyword) == (table, kw),
// scanning the row keyspace directly off the store. Mirrors countRows in
// key_robustness_test.go but works against a raw *Index (no testEnv wrapper).
func countRowsIdx(idx *Index, table int, kw string) int {
	n := 0
	_ = idx.db.Scan([]byte{idx.keyTypeRow}, func(k, _ []byte) bool {
		if tid, gotKw, _, _ := idx.decodeInvertedKey(string(k)); tid == table && gotKw == kw {
			n++
		}
		return true
	})
	return n
}

// T1 — the flush path must stamp each row's on-disk doccount with the DEDUPED
// unique docid count, not the pre-dedup buffer length. updateIndex appends one
// docid per keyword occurrence without dedup, so indexing {"hot","hot","hot"} for
// a single document buffers three docids; the flushed value dedups to one, but the
// buggy flush stamps doccount=3. Every row's doccount must equal the number of
// docids actually stored in its value.
func TestFlush_StampsDedupedDoccount(t *testing.T) {
	idx, cleanup := newBoundedIndex(t, Options{} /* unbounded, ticker disabled */)
	defer cleanup()

	const table = 1
	idx.updateIndex(table, makeDocID("d0"), []string{"hot", "hot", "hot"})
	forceFlush(idx)

	visited := 0
	_ = idx.db.Scan([]byte{idx.keyTypeRow}, func(k, value []byte) bool {
		tid, kw, doccount, _ := idx.decodeInvertedKey(string(k))
		if tid != table || kw != "hot" {
			return true
		}
		visited++
		unique := len(decodeInvertedValue(value))
		if doccount != unique {
			t.Errorf("row %q: stamped doccount=%d, but value holds %d unique docids", k, doccount, unique)
		}
		return true
	})
	if visited < 1 {
		t.Fatalf("expected at least 1 stored \"hot\" row, visited %d", visited)
	}
}

// T2 — a hot keyword whose per-flush rows each carry an inflated doccount
// (> maxInvertedIndexSize/2) gets quarantined by the merger and never compacted,
// so its row count never shrinks. With the deduped count the rows are well under
// the threshold and the merger folds them into fewer rows while preserving the
// retrievable docids.
func TestMerge_CompactsHotKeywordRows(t *testing.T) {
	idx, cleanup := newBoundedIndex(t, Options{} /* unbounded, ticker disabled */)
	defer cleanup()

	const table = 1
	const size = 10 // doccount>size/2==5 quarantines; buggy flush stamps 6 per row

	docs := []int64{makeDocID("d0"), makeDocID("d1"), makeDocID("d2")}
	for _, doc := range docs {
		idx.updateIndex(table, doc, []string{"hot", "hot", "hot", "hot", "hot", "hot"})
		forceFlush(idx)
	}

	before := countRowsIdx(idx, table, "hot")
	if before < 2 {
		t.Fatalf("expected >=2 separate \"hot\" rows before merge, got %d", before)
	}
	// DocIds is a set (map); capture its members as a sorted slice so the exact
	// retrievable docid SET — not just its size — can be compared across the merge.
	idsBefore := slices.Sorted(maps.Keys(idx.GetDocs(table, "hot").DocIds))
	docsBefore := len(idsBefore)

	m := merging{NextIter: string(idx.keyTypeRow)}
	for i := 0; i < 5; i++ {
		m = idx.mergeKeywordsIndex(m, size)
	}

	after := countRowsIdx(idx, table, "hot")
	if after >= before {
		t.Errorf("merger did not compact hot rows: before=%d after=%d (quarantined by inflated doccount)", before, after)
	}
	idsAfter := slices.Sorted(maps.Keys(idx.GetDocs(table, "hot").DocIds))
	if got := len(idsAfter); got != docsBefore {
		t.Errorf("docid set changed across merge: before=%d after=%d", docsBefore, got)
	}
	// The merge must be docid-preserving: not just the count, the exact SET of
	// retrievable docids must be identical before vs after.
	assert.Equal(t, idsBefore, idsAfter, "docid set changed across merge")
}
