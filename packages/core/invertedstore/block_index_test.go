package invertedstore

import (
	"bytes"
	"encoding/binary"
	"path/filepath"
	"testing"
)

// block_index_test.go — the dedicated block-index-integrity guard for C.2 (per-cursor decompress
// reuse). The differential hits-test does NOT cover this class: a corrupt idx[i].firstKey that is
// too LOW only makes scanPrefix start its sort.Search one block early, so every key is still found
// (the hits stay correct) while the persisted block index is silently wrong. This test reads each
// block's TRUE first record key off disk and asserts it byte-equals the persisted idx[i].firstKey —
// the only thing that catches a reused-block buffer clobbering a retained firstKey.

// blockFirstKey decodes the first record's key from data block i (the on-disk truth: the first
// uvarint(klen) key of the decompressed block), independent of seg.idx[i].firstKey.
func blockFirstKey(seg *segment, i int) []byte {
	blk := seg.blockBytes(i)
	kl, n := binary.Uvarint(blk)
	return append([]byte(nil), blk[n:n+int(kl)]...)
}

// assertBlockIndexIntact checks that for EVERY data block i, the persisted index first-key exactly
// equals block i's true first record key. A C.2 per-cursor decompress reuse without the blkFirst copy
// corrupts this (a reused source block overwrites a writer firstKey still pointing into it).
func assertBlockIndexIntact(t *testing.T, seg *segment) {
	t.Helper()
	if len(seg.idx) == 0 {
		t.Fatalf("segment %d has no data blocks; the test must force multiple blocks", seg.id)
	}
	for i := range seg.idx {
		want := blockFirstKey(seg, i)
		got := seg.idx[i].firstKey
		if !bytes.Equal(got, want) {
			t.Fatalf("seg %d block %d: persisted idx.firstKey %x != block's true first key %x",
				seg.id, i, got, want)
		}
	}
}

// TestMerge_BlockIndexFirstKeysIntact builds several L0 segments, each spanning MANY data blocks (so
// merge cursors cross block boundaries repeatedly), runs a tiered merge, and then asserts — on BOTH
// the live merged segment AND a fresh reopen from disk — that every block-index first-key equals the
// real first record key of its block. This is the genuine red→green discriminator for C.2: with a
// per-cursor block-buffer reuse but NO `w.blkFirst = append([]byte(nil), key...)` copy, a cursor's
// advance() that crosses a block boundary overwrites the buffer the retained writer firstKey still
// aliases, corrupting the persisted index — while the differential hits-test stays GREEN.
func TestMerge_BlockIndexFirstKeysIntact(t *testing.T) {
	// Tiny BlockTarget forces many data blocks per segment (both sources and the merged output), so the
	// merge crosses cursor block boundaries while a writer block is still open (the aliasing window).
	s, tbl := newMergeStoreOpts(t, Options{Fanout: 3, BlockTarget: 64, DictChunkBytes: 64})
	defer s.CloseAndWait()

	// Three L0 segments of distinct keywords. Many keywords per segment => many blocks per source.
	// Disjoint keyword ranges per segment keep ordering simple while still interleaving across cursors
	// at merge time (segment B's "b*" keys sort between A's and C's, etc., is NOT required — what
	// matters is each cursor advances across several of its own blocks during the merge).
	want := map[string]int64{} // keyword -> docid ground truth, for the differential net
	seal := func(prefix string, base int64, n int) {
		for i := 0; i < n; i++ {
			kw := kwf(prefix, i)
			docid := base + int64(i)
			s.addPostingForTest(tbl, kw, docid)
			want[kw] = docid
		}
		s.forceSpill(tbl)
	}
	seal("a", 1000, 40)
	seal("b", 2000, 40)
	seal("c", 3000, 40)

	if len(s.segs) != 3 {
		t.Fatalf("want 3 L0 segments before merge, got %d", len(s.segs))
	}

	if !s.mergeOneLevelForTest(t) {
		t.Fatal("expected a tiered merge with 3 L0 segments at Fanout 3")
	}
	if len(s.segs) != 1 {
		t.Fatalf("after merging 3 L0 segments want 1 segment, got %d", len(s.segs))
	}
	merged := s.segs[0]
	if len(merged.idx) < 3 {
		t.Fatalf("merged segment has only %d blocks; the test needs several to exercise the aliasing window", len(merged.idx))
	}

	// (1) The block index of the LIVE merged segment must be intact.
	assertBlockIndexIntact(t, merged)

	// (2) The block index PERSISTED to disk must be intact (reopen from the file: this is what every
	// future Open reads). Independent of the in-memory handle.
	reopened := openSegment(filepath.Join(s.dir, segFileName(merged.id)))
	defer reopened.close()
	assertBlockIndexIntact(t, reopened)

	// (3) DIFFERENTIAL NET (the test that MISSES the corruption): every keyword still resolves to its
	// docid. This must stay GREEN even with a corrupt-but-too-low firstKey, proving the integrity
	// assertions above — not this — are what discriminate C.2.
	for kw, docid := range want {
		r := s.Search(tbl, kw, 0, nil)
		if !hasDoc(r, docid) {
			t.Fatalf("differential: keyword %q lost docid %d after merge: %v", kw, docid, r.DocIds)
		}
	}
}
