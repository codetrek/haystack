package invertedstore

import (
	"os"
	"path/filepath"
	"testing"
)

// On an adds-only keyword the op log holds only add-ops (no del cross-bookkeeping) and resolves to
// exactly those docids in adds, none in dels (item H — the slice replaces the lazy del-map).
func TestHeadFix_AddsOnlyResolvesToAdds(t *testing.T) {
	h := newHeadTable()
	h.addPosting("alpha", 1)
	h.addPosting("alpha", 2)
	pd := h.inv["alpha"]
	if pd.nOps() != 2 {
		t.Fatalf("nOps = %d, want 2 appended add-ops", pd.nOps())
	}
	adds, dels := resolveOps(pd)
	sortInt64Slice(adds)
	if !eqInt64s(adds, []int64{1, 2}) {
		t.Fatalf("adds = %v, want {1,2}", adds)
	}
	if len(dels) != 0 {
		t.Fatalf("dels = %v, want none on an adds-only keyword", dels)
	}
}

// add -> tombstone -> re-add on the same (kw,docid) must collapse to the survivor (PRESENT), exactly
// as the eager-map version did: the LAST op (the re-add) wins, so the docid is a live add, not a del.
func TestHeadFix_AddDelReaddResolves(t *testing.T) {
	h := newHeadTable()
	h.addPosting("k", 5)       // op: add 5
	h.tombstonePosting("k", 5) // op: del 5
	h.addPosting("k", 5)       // op: add 5  (latest = add)
	adds, dels := resolveOps(h.inv["k"])
	if !eqInt64s(adds, []int64{5}) {
		t.Fatalf("docid 5 should be a live add after add/del/re-add, adds = %v", adds)
	}
	if len(dels) != 0 {
		t.Fatalf("docid 5 should NOT be tombstoned after the final re-add, dels = %v", dels)
	}
	// tombstone-first path appends a del-op and resolves to a del (no add bookkeeping needed).
	h.tombstonePosting("t", 9) // op: del 9
	adds, dels = resolveOps(h.inv["t"])
	if len(adds) != 0 {
		t.Fatalf("tombstone-only keyword should have no adds, adds = %v", adds)
	}
	if !eqInt64s(dels, []int64{9}) {
		t.Fatalf("dels = %v, want {9}", dels)
	}
}

// sortInt64Slice is a tiny in-place ascending sort for stable assertions (resolveOps' adds are in
// docid-ascending order already, but sort defensively).
func sortInt64Slice(s []int64) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// TestFileSize_ExistingAndMissing: fileSize returns the on-disk byte length of an existing file and
// falls back to 0 when os.Stat errors (a missing file) — the segMeta.Size field must not propagate a
// stat error, only a best-effort size.
func TestFileSize_ExistingAndMissing(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.dat")
	body := []byte("0123456789") // 10 bytes
	if err := os.WriteFile(present, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := fileSize(present); got != int64(len(body)) {
		t.Fatalf("fileSize(present) = %d, want %d", got, len(body))
	}
	// A path that does not exist: os.Stat errors, so fileSize returns 0 (never a negative/garbage).
	if got := fileSize(filepath.Join(dir, "does-not-exist.dat")); got != 0 {
		t.Fatalf("fileSize(missing) = %d, want 0 on a stat error", got)
	}
}
