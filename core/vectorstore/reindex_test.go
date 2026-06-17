package vectorstore

import (
	"math"
	"math/rand"
	"testing"
)

func TestReindexNodeStore_ReconstructsRawAndRepreparesUnderIndexMetric(t *testing.T) {
	// Records are stored under the PRIMARY metric (cosine: unit + norm). A reindex
	// store for an index whose metric is Euclidean must hand the graph builder the
	// RAW vector re-prepared under Euclidean (identity), reconstructed from the
	// cosine unit·norm at ~1e-7 (§3.4).
	dir := t.TempDir()
	seg := buildTinySealedForGraphTest(t, dir) // 3 rows; defined in graphfile_test.go

	// Wrap the segment's graph store as a Euclidean reindex over a cosine-stored seg.
	// (The seg here was written under DotProduct in the helper; use DotProduct as the
	// primary and Euclidean as the index metric — both raw-identity, so the
	// reconstruction is exact and the test is deterministic.)
	rs := newReindexNodeStore(seg, DotProduct, Euclidean)

	if rs.Metric() != Euclidean {
		t.Fatalf("reindex Metric() = %v, want Euclidean (the INDEX metric)", rs.Metric())
	}
	// Bind a node for slot 0 (docId 1) and read its vector back through the wrapper.
	// bindSlot MUST precede PutNode: PutNode consumes the pending (docId→slot) binding
	// the segGraphStore set up (deviation from the plan, which ordered them reversed —
	// PutNode would have errored without the prior bind).
	rs.bindSlot(1, 0)
	requireNoError(t, rs.PutNode(mustNextID(t, rs), 0, nil, 1))
	got, err := rs.GetVectorRef(0)
	requireNoError(t, err)
	want := []float32{1, 0, 0} // slot 0's raw vector
	if len(got) != 3 {
		t.Fatalf("reconstructed dim = %d, want 3", len(got))
	}
	for i := range want {
		if math.Abs(float64(got[i]-want[i])) > 1e-6 {
			t.Fatalf("reconstruct[%d] = %v, want ~%v (>1e-6 off)", i, got[i], want[i])
		}
	}
}

func mustNextID(t *testing.T, rs *reindexNodeStore) uint64 {
	t.Helper()
	id, err := rs.NextNodeId()
	requireNoError(t, err)
	return id
}

// TestReindexNodeStore_BuiltGraphRanksUnderIndexMetric red-proofs the load-bearing
// build-space fix (appendix #2-7): the HNSW insert path takes the NEW node's own
// vector from the b.put arg (hnsw.go:219 metric.prepare(vector)) while NEIGHBOR
// vectors come from GetVectorRef. buildSegmentGraphReindex MUST feed b.put the
// SAME index-metric form GetVectorRef returns, or the inserted node and its
// candidate neighbors live in different vector spaces and the built TOPOLOGY
// degrades.
//
// The defect is subtle to observe because search-time distances are reconstructed
// correctly (newBuiltIndexFor wraps the reopened graph), so HNSW's robustness masks
// a moderately-wrong graph at generous ef. To expose it we (1) use Cosine PRIMARY
// (unit + norm, a NON-identity restore — DotProduct primary would hide the bug)
// with Euclidean INDEX over many widely-varied-magnitude vectors so cosine and
// euclidean order them differently, and (2) build+search at a DELIBERATELY SMALL ef
// where graph quality, not brute breadth, decides recall. With the correct build the
// averaged euclidean recall is ~0.90; with b.put fed the primary stored form it
// collapses to ~0.74. The 0.85 bar (averaged over 40 fixed-seed queries, fully
// deterministic) sits cleanly between the two.
func TestReindexNodeStore_BuiltGraphRanksUnderIndexMetric(t *testing.T) {
	dir := t.TempDir()
	rng := rand.New(rand.NewSource(99))
	dim := 12
	// Cosine-stored segment: half the corpus is large-magnitude, half small, so the
	// unit-collapse a cosine store applies makes euclidean order != cosine order.
	raws := make(map[int64][]float32)
	seg := newSegment(Cosine)
	for i := 0; i < 400; i++ {
		v := make([]float32, dim)
		scale := float32(1)
		if i%2 == 0 {
			scale = 100
		}
		for d := range v {
			v[d] = (rng.Float32()*2 - 1) * scale
		}
		doc := int64(i + 1)
		raws[doc] = v
		stored, norm := Cosine.prepare(v)
		seg.append(doc, stored, norm, nil)
	}
	requireNoError(t, writeSealedSegment(dir, seg, nil))
	ss, err := openSealedSegment(dir, Cosine)
	requireNoError(t, err)
	t.Cleanup(ss.close)

	// Build + search at a small ef so the build's graph QUALITY (not brute breadth)
	// decides recall — this is what makes a build-space split observable.
	cfg := graphConfig{M: 8, EfConstruction: 8, EfSearch: 8}.withDefaults()
	gs, err := buildSegmentGraphReindex(dir, "euclid", ss, Cosine, Euclidean, cfg)
	requireNoError(t, err)
	bi := newBuiltIndexFor(gs, ss, Cosine, Euclidean, cfg)

	// Average euclidean recall over a fixed-seed query set vs the euclidean oracle.
	qrng := rand.New(rand.NewSource(7))
	const trials = 40
	var sum float64
	for tr := 0; tr < trials; tr++ {
		q := make([]float32, dim)
		for d := range q {
			q[d] = (qrng.Float32()*2 - 1) * 50
		}
		want := bruteForceKNN(Euclidean, q, raws, 10)
		hits, serr := bi.idx.search(q, 10)
		requireNoError(t, serr)
		sum += recallAtK(hits, want)
	}
	avg := sum / float64(trials)
	if avg < 0.85 {
		t.Fatalf("euclid built-graph avg recall vs euclidean oracle = %.3f, want >= 0.85 "+
			"(a primary/index space mismatch in the build mis-ranks → ~0.74)", avg)
	}
}
