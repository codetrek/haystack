package vectorstore

import (
	"path/filepath"
	"testing"
)

// buildHeadSeg appends rows into a fresh in-memory segment (head) for sealing.
func buildHeadSeg(m Metric, rows []struct {
	doc int64
	v   []float32
	pl  []byte
}) *segment {
	seg := newSegment(m)
	for _, r := range rows {
		stored, norm := m.prepare(r.v)
		seg.append(r.doc, stored, norm, r.pl)
	}
	return seg
}

func TestSeal_WriteOpenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	head := buildHeadSeg(DotProduct, []struct {
		doc int64
		v   []float32
		pl  []byte
	}{
		{10, []float32{1, 0, 0}, []byte("p10")},
		{20, []float32{0, 1, 0}, []byte("p20")},
		{30, []float32{0, 0, 1}, nil},
	})
	// Tombstone slot 1 (docId 20) so the sealed segment carries a delete.
	head.tombstone(1)

	segDir := filepath.Join(dir, "seg-1-0")
	requireNoError(t, writeSealedSegment(segDir, head))

	ss, err := openSealedSegment(segDir, DotProduct)
	requireNoError(t, err)
	defer ss.close()

	if ss.dim != 3 {
		t.Fatalf("dim = %d, want 3", ss.dim)
	}
	if ss.count() != 3 {
		t.Fatalf("count = %d, want 3", ss.count())
	}
	// slot 0 live, docId 10, payload p10, vector {1,0,0}
	stored, _, pl, live := ss.read(0)
	if !live || ss.slotDoc(0) != 10 || string(pl) != "p10" || stored[0] != 1 {
		t.Fatalf("slot0 = live=%v doc=%d pl=%q v=%v", live, ss.slotDoc(0), pl, stored)
	}
	// slot 1 tombstoned (docId 20)
	if _, _, _, live := ss.read(1); live {
		t.Fatal("slot1 should be tombstoned")
	}
	// eachLive visits only slots 0 and 2
	var seenDocs []int64
	ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		seenDocs = append(seenDocs, docID)
	})
	if len(seenDocs) != 2 || seenDocs[0] != 10 || seenDocs[1] != 30 {
		t.Fatalf("eachLive docs = %v, want [10 30]", seenDocs)
	}
}
