package invertedstore

import "testing"

// decodeInvertedEntryCount is the independent oracle for segMeta.Postings: it decodes every live
// segment's [I] records and sums their (add+del) docids — so the field is cross-checked against the
// real bytes, not a hand-computed constant.
func decodeInvertedEntryCount(t *testing.T, s *Store) int64 {
	t.Helper()
	s.mu.RLock()
	segs := append([]*segment(nil), s.segs...)
	s.mu.RUnlock()
	var n int64
	for _, seg := range segs {
		c := newMergeCursor(seg)
		for !c.done {
			if keyType(c.key) == ktInverted {
				adds, dels := splitInvertedValue(c.val)
				decodeDocs(adds, func(int64) { n++ })
				decodeDocs(dels, func(int64) { n++ })
			}
			c.advance()
		}
	}
	return n
}

// A spilled segment's Postings equals the inverted (add+del) entries it stores — verified by
// decoding the segment, not asserting a hand constant (spec §8.8).
func TestSegMetaPostings_DecodeCrossCheck(t *testing.T) {
	s, tid := newUpdateStore(t)
	s.applyForTest(tid, 1, []string{"a", "b", "c"})
	s.applyForTest(tid, 2, []string{"b", "c"})
	s.spillForTest(tid)

	var metaSum int64
	for _, sm := range s.SegmentsForTest() {
		metaSum += sm.Postings
	}
	if oracle := decodeInvertedEntryCount(t, s); metaSum != oracle {
		t.Fatalf("Σ segMeta.Postings = %d, decoded inverted entries = %d", metaSum, oracle)
	}
	if metaSum != 5 { // 3 (doc1) + 2 (doc2) adds, 0 dels
		t.Fatalf("Σ Postings = %d, want 5", metaSum)
	}
}

// A covering merge that reclaims everything (all docs deleted) writes an output segment with
// Postings 0 — the terminal state the deadFraction `written <= 0 → 0` guard rests on (spec §8.8).
func TestSegMetaPostings_EmptyCoveringOutput(t *testing.T) {
	s, tid := newUpdateStore(t)
	s.applyForTest(tid, 1, []string{"a", "b"})
	s.spillForTest(tid)
	s.Update(tid, 1, nil) // real delete path: reads old={a,b} from the segment, tombstones them
	s.sync()
	s.spillForTest(tid)
	s.coveringMergeForTest(t) // drops fully-tombstoned keys + forward-tombstone -> empty output

	for _, sm := range s.SegmentsForTest() {
		if sm.Postings != 0 {
			t.Fatalf("empty covering output Postings = %d, want 0", sm.Postings)
		}
	}
}
