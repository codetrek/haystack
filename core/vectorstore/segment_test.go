package vectorstore

import "testing"

func TestSegment_AppendReadTombstone(t *testing.T) {
	s := newSegment(Cosine)
	slot0 := s.append(10, []float32{0.6, 0.8}, 5, []byte("a"))
	slot1 := s.append(11, []float32{0, 1}, 1, []byte("b"))
	if slot0 != 0 || slot1 != 1 {
		t.Fatalf("slots = %d,%d, want 0,1", slot0, slot1)
	}
	if got, ok := s.slotOfDoc(10); !ok || got != 0 {
		t.Fatalf("slotOfDoc(10) = %d,%v, want 0,true", got, ok)
	}
	v, n, pl, live := s.read(0)
	if !live || n != 5 || string(pl) != "a" || v[0] != 0.6 {
		t.Fatalf("read(0) = %v,%v,%q,%v", v, n, pl, live)
	}
	s.tombstone(0)
	if _, _, _, live := s.read(0); live {
		t.Fatal("slot 0 should be tombstoned")
	}
	if _, ok := s.slotOfDoc(10); ok {
		t.Fatal("slotOfDoc(10) should be gone after tombstone")
	}
}

func TestSegment_AppendCopiesBuffers(t *testing.T) {
	s := newSegment(DotProduct)
	v := []float32{1, 2}
	pl := []byte("x")
	s.append(1, v, 0, pl)
	v[0] = 99 // mutate caller buffers after append
	pl[0] = 'Z'
	gv, _, gpl, _ := s.read(0)
	if gv[0] != 1 || string(gpl) != "x" {
		t.Fatalf("segment must copy inputs: got %v,%q", gv, gpl)
	}
}

func TestSegment_LiveIter(t *testing.T) {
	s := newSegment(DotProduct)
	s.append(1, []float32{1, 0}, 0, nil)
	s.append(2, []float32{0, 1}, 0, nil)
	s.append(3, []float32{1, 1}, 0, nil)
	s.tombstone(1)
	var docs []int64
	s.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		docs = append(docs, docID)
	})
	if len(docs) != 2 || docs[0] != 1 || docs[1] != 3 {
		t.Fatalf("live docs = %v, want [1 3]", docs)
	}
}

func TestSegment_TombstoneOutOfRangeNoPanic(t *testing.T) {
	s := newSegment(Cosine)
	s.tombstone(-1)
	s.tombstone(99) // must not panic
}
