package vectorstore

import (
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// TestSeal_TombstonePersistsAcrossReopen verifies a post-seal tombstone survives a
// close-and-reopen of the sealed segment. Since incr 3 the durable tombstone form is
// the bbolt tomb bucket (not a mmap'd + msync'd tomb.dat): markTombLocked flips the
// in-memory bit and the Store commits one tomb-bucket Put; on reopen the segment's
// in-memory bitmap is seeded from listTombSlots. This test drives that primitive
// directly (open segment under a control store, mark + commit, close, reopen reading
// the durable slots). It is cross-platform — bbolt's commit is durable everywhere,
// unlike the former tomb.dat mmap whose cross-view visibility was fragile on Windows.
func TestSeal_TombstonePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	head := buildHeadSeg(DotProduct, []struct {
		doc int64
		v   []float32
		pl  Payload
	}{
		{10, []float32{1, 0}, nil},
		{20, []float32{0, 1}, nil},
	})
	segDir := filepath.Join(dir, "seg-2-0")
	requireNoError(t, writeSealedSegment(segDir, head, nil))

	cs, err := openControlStore(dir)
	requireNoError(t, err)

	ss, err := openSealedSegment(segDir, DotProduct, 2, nil)
	requireNoError(t, err)
	// Delete docId 10 (slot 0) post-seal: flip the in-memory bit AND commit the
	// durable tomb-bucket entry — exactly what Store.Delete's sealed branch does.
	if !ss.markTombLocked(0) {
		t.Fatal("markTombLocked(0) reported no-op on a live slot")
	}
	requireNoError(t, cs.update(func(tx *bolt.Tx) error { return putTomb(tx, 2, 0) }))
	ss.close()

	// Reopen seeding the in-memory tomb from the durable tomb bucket.
	var tombSlots []int
	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		var lerr error
		tombSlots, lerr = listTombSlots(tx, 2)
		return lerr
	}))
	requireNoError(t, cs.Close())

	ss2, err := openSealedSegment(segDir, DotProduct, 2, tombSlots)
	requireNoError(t, err)
	defer ss2.close()
	if _, _, _, live := ss2.read(0); live {
		t.Fatal("post-seal tombstone of slot0 did not survive reopen")
	}
	if _, _, _, live := ss2.read(1); !live {
		t.Fatal("slot1 must still be live")
	}
}
