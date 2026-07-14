package invertedstore

import (
	"testing"

	"github.com/codetrek/haystack/core/queue"
)

// newUpdateStore opens a fresh store with a created table for the Update/Batch tests. It uses a
// large CapBytes so a test only spills when it explicitly asks (forceSpill), unless it overrides.
func newUpdateStore(t *testing.T) (*Store, int) {
	t.Helper()
	return newUpdateStoreOpts(t, Options{})
}

func newUpdateStoreOpts(t *testing.T, opts Options) (*Store, int) {
	t.Helper()
	dir := t.TempDir()
	q := queue.NewMpsc("invupdate")
	q.Start()
	s, err := Open(dir, q, opts)
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}
	return s, tbl
}

// sync drains the worker so an async Update is observable. RunFunc enqueues an empty task and
// blocks until it (and therefore every earlier-enqueued Update) has run. It also settles any in-flight
// OFF-WORKER spill (F v5): an over-cap Update now detaches its head for an async encode+install, so a
// worker-only drain would return before the segment is installed — sync waits for that too, so a test
// that observes s.segs / merges after sync sees the same settled state the synchronous spill produced.
func (s *Store) sync() {
	s.q.RunFunc(func() error { return nil })
	s.WaitSpillsForTest() // settle in-flight off-worker spills (encode + install + any re-dispatch chain)
	s.q.RunFunc(func() error { return nil })
}

// forceSpill spills the table's head on the worker (synchronous) — reuses the P4c test seam.
func (s *Store) forceSpill(tbl int) { s.spillForTest(tbl) }

// --- after edits Search reflects adds + removals -----------------------------

// TestUpdate_SearchReflectsAddsAndRemovals: an initial Update then a second Update with a changed
// keyword set must add the new keyword and drop the removed one, observed through Search.
func TestUpdate_SearchReflectsAddsAndRemovals(t *testing.T) {
	s, tbl := newUpdateStore(t)
	defer s.CloseAndWait()

	s.Update(tbl, 10, []string{"alpha", "beta"})
	s.sync()

	if r := s.Search(tbl, "alpha", 0, nil); !hasDoc(r, 10) {
		t.Fatalf("after first Update, alpha must contain doc 10: %v", r.DocIds)
	}
	if r := s.Search(tbl, "beta", 0, nil); !hasDoc(r, 10) {
		t.Fatalf("after first Update, beta must contain doc 10: %v", r.DocIds)
	}

	// Spill so the first edit is sealed; the second edit must override it across segments.
	s.forceSpill(tbl)

	// Second edit: drop beta, keep alpha, add gamma.
	s.Update(tbl, 10, []string{"alpha", "gamma"})
	s.sync()

	if r := s.Search(tbl, "alpha", 0, nil); !hasDoc(r, 10) {
		t.Errorf("alpha (kept, full re-post) must still contain doc 10: %v", r.DocIds)
	}
	if r := s.Search(tbl, "gamma", 0, nil); !hasDoc(r, 10) {
		t.Errorf("gamma (added) must contain doc 10: %v", r.DocIds)
	}
	if r := s.Search(tbl, "beta", 0, nil); hasDoc(r, 10) {
		t.Errorf("beta (removed) must NOT contain doc 10 anymore: %v", r.DocIds)
	}
}

// TestUpdate_SearchReflectsRemovalAcrossSpill: the removal must hold even when the add lives in an
// OLDER sealed segment (per-keyword tombstone written to the newer segment, newest-wins at read).
func TestUpdate_SearchReflectsRemovalAcrossSpill(t *testing.T) {
	s, tbl := newUpdateStore(t)
	defer s.CloseAndWait()

	s.Update(tbl, 10, []string{"alpha", "beta"})
	s.sync()
	s.forceSpill(tbl) // alpha+beta sealed in seg A

	s.Update(tbl, 10, []string{"alpha"}) // drop beta
	s.sync()
	s.forceSpill(tbl) // tombstone for beta sealed in seg B (newer)

	if len(s.segs) != 2 {
		t.Fatalf("expected 2 sealed segments, got %d", len(s.segs))
	}
	if r := s.Search(tbl, "beta", 0, nil); hasDoc(r, 10) {
		t.Errorf("beta tombstone in newer segment must suppress doc 10, got %v", r.DocIds)
	}
	if r := s.Search(tbl, "alpha", 0, nil); !hasDoc(r, 10) {
		t.Errorf("alpha still live, doc 10 must be present, got %v", r.DocIds)
	}
}

// --- DELETE then re-read returns empty (no resurrection from an older segment) -

// TestUpdate_DeleteThenReadEmpty: Update with empty keywords deletes the doc; after a spill that
// seals the delete on top of an older segment carrying the live forward + postings, the doc must
// read EMPTY (forward-tombstone) and its postings must be gone from Search.
func TestUpdate_DeleteThenReadEmpty(t *testing.T) {
	s, tbl := newUpdateStore(t)
	defer s.CloseAndWait()

	s.Update(tbl, 10, []string{"alpha", "beta"})
	s.sync()
	s.forceSpill(tbl) // live forward + postings sealed in seg A

	s.Update(tbl, 10, nil) // DELETE
	s.sync()
	s.forceSpill(tbl) // forward-tombstone + per-keyword tombstones sealed in seg B (newer)

	// Postings gone.
	if r := s.Search(tbl, "alpha", 0, nil); hasDoc(r, 10) {
		t.Errorf("deleted doc 10 must be absent from alpha, got %v", r.DocIds)
	}
	if r := s.Search(tbl, "beta", 0, nil); hasDoc(r, 10) {
		t.Errorf("deleted doc 10 must be absent from beta, got %v", r.DocIds)
	}
	// Forward reads empty (deleted) — NO resurrection from the older non-empty record.
	words, deleted := s.forwardKeywords(tbl, 10)
	if !deleted {
		t.Errorf("forward of deleted doc 10 must report deleted, got words=%v deleted=%v", words, deleted)
	}
	if len(words) != 0 {
		t.Errorf("forward of deleted doc 10 must be empty, got %v", words)
	}
}

// TestUpdate_DeleteThenReUpdateResurrectsCleanly: after a DELETE, a later Update re-adds the doc;
// it must become present again (the forward-tombstone does not permanently brick the docid).
func TestUpdate_DeleteThenReUpdateResurrectsCleanly(t *testing.T) {
	s, tbl := newUpdateStore(t)
	defer s.CloseAndWait()

	s.Update(tbl, 10, []string{"alpha"})
	s.sync()
	s.forceSpill(tbl)

	s.Update(tbl, 10, nil) // delete
	s.sync()
	s.forceSpill(tbl)

	if r := s.Search(tbl, "alpha", 0, nil); hasDoc(r, 10) {
		t.Fatalf("after delete, doc 10 must be absent, got %v", r.DocIds)
	}

	s.Update(tbl, 10, []string{"alpha", "delta"}) // re-add
	s.sync()

	if r := s.Search(tbl, "alpha", 0, nil); !hasDoc(r, 10) {
		t.Errorf("re-Update after delete must make doc 10 present in alpha, got %v", r.DocIds)
	}
	if r := s.Search(tbl, "delta", 0, nil); !hasDoc(r, 10) {
		t.Errorf("re-Update after delete must make doc 10 present in delta, got %v", r.DocIds)
	}
	words, deleted := s.forwardKeywords(tbl, 10)
	if deleted {
		t.Errorf("re-added doc 10 must not report deleted, got deleted=%v", deleted)
	}
	if len(words) != 2 {
		t.Errorf("re-added doc 10 forward must be {alpha,delta}, got %v", words)
	}
}

// --- a docid repeated in one Batch -> last op wins ---------------------------

// TestBatch_RepeatedDocidLastWins: a Batch with two Updates of the SAME docid resolves to the
// LAST op's keyword set (applied in order on one apply task).
func TestBatch_RepeatedDocidLastWins(t *testing.T) {
	s, tbl := newUpdateStore(t)
	defer s.CloseAndWait()

	b := s.NewBatch()
	b.Update(tbl, 10, []string{"alpha", "beta"})
	b.Update(tbl, 10, []string{"gamma"}) // last op for doc 10
	b.Commit()
	s.sync()

	if r := s.Search(tbl, "gamma", 0, nil); !hasDoc(r, 10) {
		t.Errorf("last op {gamma} must win: gamma should contain doc 10, got %v", r.DocIds)
	}
	if r := s.Search(tbl, "alpha", 0, nil); hasDoc(r, 10) {
		t.Errorf("superseded op {alpha,beta} must NOT leave doc 10 in alpha, got %v", r.DocIds)
	}
	if r := s.Search(tbl, "beta", 0, nil); hasDoc(r, 10) {
		t.Errorf("superseded op {alpha,beta} must NOT leave doc 10 in beta, got %v", r.DocIds)
	}
	words, deleted := s.forwardKeywords(tbl, 10)
	if deleted || len(words) != 1 || words[0] != "gamma" {
		t.Errorf("forward after batch must be {gamma}, got words=%v deleted=%v", words, deleted)
	}
}

// TestBatch_RepeatedDocidLastWinsDelete: an Update then a DELETE of the same docid in one Batch
// resolves to delete (last op wins).
func TestBatch_RepeatedDocidLastWinsDelete(t *testing.T) {
	s, tbl := newUpdateStore(t)
	defer s.CloseAndWait()

	b := s.NewBatch()
	b.Update(tbl, 10, []string{"alpha"})
	b.Update(tbl, 10, nil) // delete wins
	b.Commit()
	s.sync()

	if r := s.Search(tbl, "alpha", 0, nil); hasDoc(r, 10) {
		t.Errorf("delete (last op) must win: alpha must not contain doc 10, got %v", r.DocIds)
	}
	_, deleted := s.forwardKeywords(tbl, 10)
	if !deleted {
		t.Errorf("doc 10 must be deleted after batch ending in a delete")
	}
}

// TestBatch_MultipleDocsOneTask: a Batch over distinct docids applies them all (one apply task).
func TestBatch_MultipleDocsOneTask(t *testing.T) {
	s, tbl := newUpdateStore(t)
	defer s.CloseAndWait()

	b := s.NewBatch()
	for d := int64(1); d <= 5; d++ {
		b.Update(tbl, d, []string{"alpha"})
	}
	b.Commit()
	s.sync()

	r := s.Search(tbl, "alpha", 0, nil)
	for d := int64(1); d <= 5; d++ {
		if !hasDoc(r, d) {
			t.Errorf("doc %d missing after batch, got %v", d, r.DocIds)
		}
	}
}

// --- cold-build (all-new) path takes no forward read -------------------------

// TestUpdate_ColdBuildNoForwardRead: on a cold build (every doc new) the apply must NOT read the
// forward map (the read misses anyway). We assert this by counting forward reads through a hook:
// for an all-new batch the count stays 0.
func TestUpdate_ColdBuildNoForwardRead(t *testing.T) {
	s, tbl := newUpdateStore(t)
	defer s.CloseAndWait()

	var reads int
	s.onForwardRead = func() { reads++ }

	b := s.NewBatch()
	for d := int64(1); d <= 20; d++ {
		b.Update(tbl, d, []string{"alpha", "beta"})
	}
	b.Commit()
	s.sync()

	if reads != 0 {
		t.Errorf("cold build (all-new docids) must take no forward read, got %d reads", reads)
	}
	// sanity: the postings still landed.
	if r := s.Search(tbl, "alpha", 0, nil); len(r.DocIds) != 20 {
		t.Errorf("cold build should index 20 docs under alpha, got %d", len(r.DocIds))
	}
}

// TestUpdate_WarmEditTakesForwardRead: editing a doc the store has already seen DOES read the
// forward map (to diff old vs new) — the complement of the cold-build assertion.
func TestUpdate_WarmEditTakesForwardRead(t *testing.T) {
	s, tbl := newUpdateStore(t)
	defer s.CloseAndWait()

	s.Update(tbl, 10, []string{"alpha"})
	s.sync()

	var reads int
	s.onForwardRead = func() { reads++ }

	s.Update(tbl, 10, []string{"alpha", "beta"}) // doc 10 already known
	s.sync()

	if reads == 0 {
		t.Errorf("editing a known doc must read the forward map to diff, got 0 reads")
	}
}

// --- Update is a single-item Batch (spill mid-batch is fine) ------------------

// TestUpdate_SpillMidBatch: a CapBytes small enough to spill during a batch still applies every op
// correctly (the head + segment set are worker-owned; the apply runs to completion).
func TestUpdate_SpillMidBatch(t *testing.T) {
	// Tiny cap so the batch spills partway through.
	s, tbl := newUpdateStoreOpts(t, Options{CapBytes: 1})
	defer s.CloseAndWait()

	b := s.NewBatch()
	for d := int64(1); d <= 50; d++ {
		b.Update(tbl, d, []string{"alpha"})
	}
	b.Commit()
	s.sync()

	if len(s.segs) == 0 {
		t.Fatalf("a tiny CapBytes should have spilled at least one segment mid-batch, got %d", len(s.segs))
	}
	r := s.Search(tbl, "alpha", 0, nil)
	if len(r.DocIds) != 50 {
		t.Errorf("all 50 docs must be searchable across spilled segments + head, got %d: %v", len(r.DocIds), r.DocIds)
	}
}
