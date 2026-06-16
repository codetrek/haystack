package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestGraphFile_PersistReopen_SameResults(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	dim := 12
	n := 200
	rows := make([]struct {
		doc int64
		v   []float32
		pl  Payload
	}, 0, n)
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		rows = append(rows, struct {
			doc int64
			v   []float32
			pl  Payload
		}{int64(i), v, nil})
	}
	head := buildHeadSeg(Cosine, rows)
	segDir := filepath.Join(t.TempDir(), "seg-1-0")
	requireNoError(t, writeSealedSegment(segDir, head))
	ss, err := openSealedSegment(segDir, Cosine)
	requireNoError(t, err)
	defer ss.close()

	// Build in memory.
	gs := newSegGraphStore(ss)
	idx := newHNSWIndex(gs, withGraphRand(rand.New(rand.NewSource(12))))
	b := idx.newBatch()
	ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		gs.bindSlot(docID, slot)
		b.put(docID, stored)
	})
	requireNoError(t, b.commit())

	q := make([]float32, dim)
	for d := range q {
		q[d] = rng.Float32()
	}
	wantRes, err := idx.search(q, 10)
	requireNoError(t, err)

	// Persist + reopen.
	requireNoError(t, writeGraphFile(segDir, gs))
	rgs, err := openGraphFile(segDir, ss)
	requireNoError(t, err)
	ridx := newHNSWIndex(rgs)
	gotRes, err := ridx.search(q, 10)
	requireNoError(t, err)

	if len(gotRes) != len(wantRes) {
		t.Fatalf("reopened result count = %d, want %d", len(gotRes), len(wantRes))
	}
	for i := range gotRes {
		if gotRes[i].DocID != wantRes[i].DocID {
			t.Fatalf("result[%d] docId = %d, want %d (graph persistence diverged)", i, gotRes[i].DocID, wantRes[i].DocID)
		}
	}
}

// TestGraphFile_TruncatedRejected applies adversarial-review appendix #9(3):
// openGraphFile must NOT panic indexing past the end of a short/corrupt file —
// it must return a clean error. We seal a tiny segment, build+persist a real
// graph.dat, then truncate it mid-node-table and assert openGraphFile errors
// rather than panicking (recovery can then re-build or surface the error).
func TestGraphFile_TruncatedRejected(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	dim := 8
	rows := make([]struct {
		doc int64
		v   []float32
		pl  Payload
	}, 0, 30)
	for i := 0; i < 30; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		rows = append(rows, struct {
			doc int64
			v   []float32
			pl  Payload
		}{int64(i), v, nil})
	}
	head := buildHeadSeg(Cosine, rows)
	segDir := filepath.Join(t.TempDir(), "seg-1-0")
	requireNoError(t, writeSealedSegment(segDir, head))
	ss, err := openSealedSegment(segDir, Cosine)
	requireNoError(t, err)
	defer ss.close()

	gs := newSegGraphStore(ss)
	idx := newHNSWIndex(gs, withGraphRand(rand.New(rand.NewSource(14))))
	b := idx.newBatch()
	ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		gs.bindSlot(docID, slot)
		b.put(docID, stored)
	})
	requireNoError(t, b.commit())
	requireNoError(t, writeGraphFile(segDir, gs))

	// Chop the node table off after the header + a few bytes: the parser must
	// detect the short buffer and error, not panic on data[off:] out of range.
	path := filepath.Join(segDir, "graph.dat")
	data, err := os.ReadFile(path)
	requireNoError(t, err)
	if len(data) <= segPageSize {
		t.Fatalf("graph.dat unexpectedly small (%d bytes)", len(data))
	}
	requireNoError(t, os.WriteFile(path, data[:segPageSize+5], 0644))

	if _, err := openGraphFile(segDir, ss); err == nil {
		t.Fatal("openGraphFile must reject a truncated node table, got nil error")
	}
}
