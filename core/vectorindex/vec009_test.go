package vectorindex

import (
	"fmt"
	"testing"
)

// TestFaultedStoreRejectsAbortedReads_VEC009 red-proofs VEC-009: after a partial
// transaction ABORT, the faulted MmapStore must not keep serving the aborted
// (never-committed) node's data or a stale entry point on the LIVE instance.
//
// PutNode/SetEntryPoint mutate s.meta in place at write time (TotalSlots bumped,
// Occupied byte set, EntryPoint moved); txnAbort only records the fault and does
// NOT roll those back. Before the fix, nodeLive (TotalSlots-bounded) and
// GetDocId/GetEntryPoint therefore surfaced the zombie node and aborted EP until
// the store was reopened. The contract is "recovery is via reopen", so a faulted
// store must reject reads rather than hand out uncommitted state.
func TestFaultedStoreRejectsAbortedReads_VEC009(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	// Commit node A (id 0, docId 100) as the entry point.
	if err := s.txnBegin(); err != nil {
		t.Fatal(err)
	}
	if err := s.PutNode(0, 0, []float32{1, 0, 0, 0}, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntryPoint(0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}

	// Partial transaction: write node B (id 1, docId 200) and move the EP to B,
	// then ABORT before committing. This faults the store with TotalSlots=2,
	// B's slot Occupied, and meta.EntryPoint=1 — none of it committed.
	if err := s.txnBegin(); err != nil {
		t.Fatal(err)
	}
	if err := s.PutNode(1, 0, []float32{0, 1, 0, 0}, 200); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntryPoint(1, 0); err != nil {
		t.Fatal(err)
	}
	_ = s.txnAbort(fmt.Errorf("injected mid-transaction failure"))

	// VEC-009: the live faulted store must NOT surface the aborted node B nor EP=B.
	if _, ok, err := s.GetDocId(1); ok && err == nil {
		t.Fatalf("VEC-009: GetDocId surfaced aborted node B (docId 200) on a faulted store")
	}
	if id, _, err := s.GetEntryPoint(); err == nil && id == 1 {
		t.Fatalf("VEC-009: GetEntryPoint returned the aborted entry point B on a faulted store")
	}
	if _, err := s.GetVectorRef(1); err == nil {
		t.Fatalf("VEC-009: GetVectorRef served aborted node B's vector on a faulted store")
	}
	if _, ok, err := s.GetNodeId(200); ok && err == nil {
		t.Fatalf("VEC-009: GetNodeId mapped aborted docId 200 on a faulted store")
	}

	// Reopen discards the unterminated transaction: B is gone, EP is back to A.
	if err := s.Close(); err != nil {
		t.Fatalf("Close (faulted): %v", err)
	}
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, ok, _ := s2.GetDocId(1); ok {
		t.Fatalf("VEC-009: aborted node B survived reopen")
	}
	if id, _, err := s2.GetEntryPoint(); err != nil || id != 0 {
		t.Fatalf("VEC-009: entry point after reopen = (%d, err=%v), want node A (0)", id, err)
	}
}
