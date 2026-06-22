package vectorstore

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMerge_CorruptSealedPayload_AbortsFailClosed RED-proofs that a merge is
// fail-CLOSED on a corrupt sealed payload blob, matching the rest of the package
// (Get surfaces decodePayload errors — store.go ~695; see
// TestGet_MalformedPayload_Errors). packLiveDocs previously did
// `pl, _ := ss.payloadDecoded(slot)`, swallowing the decode error and packing an
// empty Payload — silently dropping that doc's attrs / non-declared fields into
// the merged output. The merge must instead surface the error and abort, so a
// corrupt blob can never be laundered into a clean-but-lossy segment.
//
// We seal a single-doc segment carrying a payload, flip the version byte of its
// on-disk payload blob (same recipe as TestGet_MalformedPayload_Errors), reopen,
// and drive an explicit merge of that segment. mergeNow plans under the lock —
// where packLiveDocs runs — so the decode error surfaces synchronously as
// mergeNow's return value. Before the fix mergeNow returns nil (silent loss);
// after the fix it returns the decode error.
func TestMerge_CorruptSealedPayload_AbortsFailClosed(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	requireNoError(t, s.Put("a", []float32{1, 0, 0}, Payload{"k": StringValue("v")}))
	requireNoError(t, s.Seal()) // one-row sealed segment (segId 1), no attr declared
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.Close())

	// payload.dat layout: [segPageSize header][n*4 lens][concatenated blobs]. With
	// n=1 the slot-0 blob begins at segPageSize+4 and its first byte is
	// payloadFmtVersion(1). Flip it to 0xFF so decodePayload rejects the blob, while
	// the lengths/header stay valid so openSealedSegment still succeeds.
	matches, _ := filepath.Glob(filepath.Join(dir, "seg-*", "payload.dat"))
	if len(matches) == 0 {
		t.Fatal("expected a payload.dat to corrupt")
	}
	f, err := os.OpenFile(matches[0], os.O_RDWR, 0644)
	requireNoError(t, err)
	if _, err := f.WriteAt([]byte{0xFF}, int64(segPageSize+4)); err != nil {
		t.Fatalf("corrupt write: %v", err)
	}
	requireNoError(t, f.Close())

	// Reopen: the segment opens fine (header/lens intact); only the blob content is
	// corrupt, so the failure surfaces at decode time during the merge pack.
	s2, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	defer s2.Close()
	requireNoError(t, s2.WaitForIndex())

	// Explicit merge of the corrupt segment. packLiveDocs runs under the lock in
	// planMergeLocked, so the decode error is mergeNow's synchronous return.
	if err := s2.mergeNow([]segID{1}); err == nil {
		t.Fatal("merge over a corrupt sealed payload must surface a decode error, not silently drop the doc's attrs")
	}
}
