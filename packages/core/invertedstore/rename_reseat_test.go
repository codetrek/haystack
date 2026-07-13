package invertedstore

import (
	"os"
	"path/filepath"
	"testing"
)

// renameSegmentFile must reseat the segment's fd across a rename (close then reopen at the new
// name) so the install path is Windows-safe — os.Rename cannot move a file whose handle is still
// open there. This exercises the SUCCESS path directly (the off-worker spill tests hit it too, but
// this pins the contract): after the rename the file lives only at the new name and the segment is
// still readable through the reseated fd.
func TestRenameSegmentFileReseatsFdToNewName(t *testing.T) {
	seg := writeTestSeg(t, true)
	defer seg.close()
	from := seg.path
	to := filepath.Join(filepath.Dir(from), "seg-renamed.dat")

	if err := renameSegmentFile(seg, from, to); err != nil {
		t.Fatalf("renameSegmentFile(%q -> %q) = %v, want nil", from, to, err)
	}
	if seg.path != to {
		t.Fatalf("seg.path = %q, want %q after successful rename", seg.path, to)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Fatalf("source file still present at %q after rename (stat err = %v)", from, err)
	}
	// The reseated fd must read the file at its new name.
	if _, ok := seg.lookupForward(forwardKey(1, 10)); !ok {
		t.Fatal("segment not readable through the reseated fd after rename to the new name")
	}
}

// When the rename FAILS, renameSegmentFile must leave the segment live at its SOURCE path — the fd
// reopened at `from` and seg.path pointing back at `from` — so installSpill's bounded retry (and its
// eventual giveUpSpill cleanup) still sees a readable segment at the temp name it will re-rename /
// remove. This is the retry-safety invariant the install rollback paths depend on; it is otherwise
// unexercised (a real os.Rename failure is not injectable through the store), so cover it directly.
func TestRenameSegmentFileErrorKeepsSegmentAtSource(t *testing.T) {
	seg := writeTestSeg(t, true)
	defer seg.close()
	from := seg.path
	// Renaming into a directory that does not exist fails on every OS, and leaves the file at `from`.
	badTo := filepath.Join(filepath.Dir(from), "no-such-subdir", "seg.dat")

	err := renameSegmentFile(seg, from, badTo)
	if err == nil {
		t.Fatalf("renameSegmentFile(%q -> %q) = nil, want an error (target dir absent)", from, badTo)
	}
	if seg.path != from {
		t.Fatalf("seg.path = %q, want the source %q after a failed rename", seg.path, from)
	}
	if _, statErr := os.Stat(from); statErr != nil {
		t.Fatalf("source file missing at %q after a failed rename: %v", from, statErr)
	}
	// The fd must be reopened at `from` so a retry reads live data (never a lost segment).
	if _, ok := seg.lookupForward(forwardKey(1, 10)); !ok {
		t.Fatal("segment not readable at the source path after a failed rename — a retry would lose it")
	}
}
