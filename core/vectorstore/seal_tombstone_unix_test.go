//go:build !windows

package vectorstore

import (
	"path/filepath"
	"testing"
)

// TestSeal_TombstonePersistsAcrossReopen verifies a post-seal Delete (a write
// through the RW tomb.dat mmap + msync) survives a close-and-reopen.
//
// Guarded to non-Windows (adversarial review #1, CRITICAL refactor-safety): on
// Windows a write through one file-backed MapViewOfFile + FlushViewOfFile is not
// guaranteed visible to a *fresh* read-only MapViewOfFile of the same file until
// the original view is unmapped, and reopening a file already mapped elsewhere
// can hit ERROR_SHARING_VIOLATION. The store always Close()s a sealed segment
// (unmapping tomb.dat) before any reopen in production, so the durability
// guarantee holds there; this targeted unit test exercises the fragile bare
// reopen and is skipped on Windows with a tracked follow-up to add a
// Windows-safe variant (close-view-then-reopen) when Phase 2 lands on Windows.
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
	requireNoError(t, writeSealedSegment(segDir, head))

	ss, err := openSealedSegment(segDir, DotProduct)
	requireNoError(t, err)
	requireNoError(t, ss.tombstoneSlot(0)) // delete docId 10 post-seal, must be durable
	ss.close()

	ss2, err := openSealedSegment(segDir, DotProduct)
	requireNoError(t, err)
	defer ss2.close()
	if _, _, _, live := ss2.read(0); live {
		t.Fatal("post-seal tombstone of slot0 did not survive reopen")
	}
	if _, _, _, live := ss2.read(1); !live {
		t.Fatal("slot1 must still be live")
	}
}
