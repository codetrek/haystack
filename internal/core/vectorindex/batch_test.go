package vectorindex

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
)

func newTestIndex() *HNSWIndex {
	return NewHNSWIndex(NewMemNodeStore(), WithRand(rand.New(rand.NewSource(42))))
}

func TestBatchCoalescePutThenPut(t *testing.T) {
	idx := newTestIndex()
	b := idx.NewBatch()
	b.Put("d", []float32{1, 0, 0})
	b.Put("d", []float32{0, 1, 0}) // overwrites
	assert.Equal(t, 1, b.Len(), "same docId must coalesce to one op")

	requireNoError(t, b.Commit())

	// Only the second vector should be present.
	res, err := idx.Search([]float32{0, 1, 0}, 1)
	requireNoError(t, err)
	requireLen(t, res, 1)
	assert.InDelta(t, 0.0, res[0].Distance, 1e-6)
}

func TestBatchCoalescePutThenDelete(t *testing.T) {
	idx := newTestIndex()
	b := idx.NewBatch()
	b.Put("d", []float32{1, 0, 0})
	b.Delete("d") // net: delete, nothing inserted
	assert.Equal(t, 1, b.Len())

	requireNoError(t, b.Commit())

	res, err := idx.Search([]float32{1, 0, 0}, 1)
	requireNoError(t, err)
	assert.Empty(t, res, "Put then Delete in one batch inserts nothing")
}

func TestBatchCoalesceDeleteThenPutIsUpsert(t *testing.T) {
	idx := newTestIndex()
	b := idx.NewBatch()
	b.Delete("d")
	b.Put("d", []float32{0, 0, 1})
	assert.Equal(t, 1, b.Len())

	requireNoError(t, b.Commit())

	res, err := idx.Search([]float32{0, 0, 1}, 1)
	requireNoError(t, err)
	requireLen(t, res, 1)
	assert.InDelta(t, 0.0, res[0].Distance, 1e-6)
}

func TestBatchDeleteAbsentIsNoop(t *testing.T) {
	idx := newTestIndex()
	b := idx.NewBatch()
	b.Delete("never-existed")
	assert.Equal(t, 1, b.Len())
	requireNoError(t, b.Commit()) // must not error

	res, err := idx.Search([]float32{1, 0, 0}, 1)
	requireNoError(t, err)
	assert.Empty(t, res)
}

func TestBatchEmptyCommitNoop(t *testing.T) {
	idx := newTestIndex()
	b := idx.NewBatch()
	assert.Equal(t, 0, b.Len())
	requireNoError(t, b.Commit())
}

func TestBatchDiscardDropsBuffer(t *testing.T) {
	idx := newTestIndex()
	b := idx.NewBatch()
	b.Put("d", []float32{1, 0, 0})
	b.Discard()
	assert.Equal(t, 0, b.Len())

	requireNoError(t, b.Commit()) // discarded → nothing applied
	res, err := idx.Search([]float32{1, 0, 0}, 1)
	requireNoError(t, err)
	assert.Empty(t, res)
}

func TestBatchUpsertReplacesCommittedDoc(t *testing.T) {
	idx := newTestIndex()

	// Commit an initial value.
	requireNoError(t, idx.Insert("d", []float32{1, 0, 0}))

	// A second batch Puts the same docId with a new vector.
	b := idx.NewBatch()
	b.Put("d", []float32{0, 0, 1})
	requireNoError(t, b.Commit())

	// docId maps to exactly one node, and it is the new vector.
	res, err := idx.Search([]float32{0, 0, 1}, 2)
	requireNoError(t, err)
	requireLen(t, res, 1) // only one node exists for "d"
	assert.InDelta(t, 0.0, res[0].Distance, 1e-6)

	// The old vector is gone.
	res2, err := idx.Search([]float32{1, 0, 0}, 1)
	requireNoError(t, err)
	requireLen(t, res2, 1)
	assert.Greater(t, res2[0].Distance, float32(0.5), "old [1,0,0] node must no longer exist")
}
