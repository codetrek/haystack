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
	requireNoError(t, writeSealedSegment(segDir, head, nil))
	ss, err := openSealedSegment(segDir, Cosine, 1, nil)
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
	requireNoError(t, writeGraphFile(segDir, "default", gs))
	rgs, err := openGraphFile(segDir, "default", ss)
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
	requireNoError(t, writeSealedSegment(segDir, head, nil))
	ss, err := openSealedSegment(segDir, Cosine, 1, nil)
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
	requireNoError(t, writeGraphFile(segDir, "default", gs))

	// Chop the node table off after the header + a few bytes: the parser must
	// detect the short buffer and error, not panic on data[off:] out of range.
	path := filepath.Join(segDir, "graph-default.dat")
	data, err := os.ReadFile(path)
	requireNoError(t, err)
	if len(data) <= segPageSize {
		t.Fatalf("graph-default.dat unexpectedly small (%d bytes)", len(data))
	}
	requireNoError(t, os.WriteFile(path, data[:segPageSize+5], 0644))

	if _, err := openGraphFile(segDir, "default", ss); err == nil {
		t.Fatal("openGraphFile must reject a truncated node table, got nil error")
	}
}

func TestGraphFile_PerIndexName_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	seg := buildTinySealedForGraphTest(t, dir) // helper defined below

	// Build two graphs with DIFFERENT index names into the SAME seg dir; they must
	// land in distinct files and round-trip independently (no collision).
	gA, err := buildSegmentGraph(dir, "alpha", seg, graphConfig{}.withDefaults())
	requireNoError(t, err)
	gB, err := buildSegmentGraph(dir, "beta", seg, graphConfig{}.withDefaults())
	requireNoError(t, err)

	if _, err := os.Stat(filepath.Join(dir, "graph-alpha.dat")); err != nil {
		t.Fatalf("graph-alpha.dat missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "graph-beta.dat")); err != nil {
		t.Fatalf("graph-beta.dat missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "graph.dat")); !os.IsNotExist(err) {
		t.Fatalf("legacy graph.dat must NOT be written; stat err = %v", err)
	}

	// Reopen each by name and confirm the node counts match the build.
	roA, err := openGraphFile(dir, "alpha", seg)
	requireNoError(t, err)
	roB, err := openGraphFile(dir, "beta", seg)
	requireNoError(t, err)
	if len(roA.nodeSlot) != len(gA.nodeSlot) || len(roB.nodeSlot) != len(gB.nodeSlot) {
		t.Fatalf("reopened node counts diverged: A %d/%d B %d/%d",
			len(roA.nodeSlot), len(gA.nodeSlot), len(roB.nodeSlot), len(gB.nodeSlot))
	}

	// graphFileName is the single source of truth for the layout.
	if graphFileName("alpha") != "graph-alpha.dat" {
		t.Fatalf("graphFileName(alpha) = %q", graphFileName("alpha"))
	}
}

// buildTinySealedForGraphTest writes a 3-row sealed segment and opens it, for
// graph-file round-trip tests that need a real *sealedSegment to resolve vectors.
func buildTinySealedForGraphTest(t *testing.T, dir string) *sealedSegment {
	t.Helper()
	seg := newSegment(DotProduct)
	seg.append(1, []float32{1, 0, 0}, 0, nil)
	seg.append(2, []float32{0, 1, 0}, 0, nil)
	seg.append(3, []float32{0, 0, 1}, 0, nil)
	requireNoError(t, writeSealedSegment(dir, seg, nil))
	ss, err := openSealedSegment(dir, DotProduct, 1, nil)
	requireNoError(t, err)
	t.Cleanup(ss.close)
	return ss
}
