package invertedstore

// This file provides test-only accessors (compiled only under `go test`) that drive the real
// worker-side apply/spill so store_test.go can exercise the head buffer + spill path WITHOUT the
// full Update path (P7). They run their work on the mpsc worker, exactly as the production write
// path does, so the concurrency contract (head mutated only on the worker) is preserved.

// applyForTest is a minimal stand-in for the P7 Update apply: for a cold doc (no diff against an
// older forward) it sets the doc's forward keyword set and adds a posting for each keyword. It
// runs on the worker and is synchronous (blocks until applied).
func (s *Store) applyForTest(tableId int, docid int64, keywords []string) {
	s.q.RunFunc(func() error {
		s.mu.Lock()
		h := s.head[tableId]
		if h == nil {
			h = newHeadTable()
			s.head[tableId] = h
		}
		if len(keywords) == 0 {
			h.deleteForward(docid)
		} else {
			h.setForward(docid, keywords)
			for _, kw := range keywords {
				h.addPosting(kw, docid)
			}
		}
		s.mu.Unlock()
		return nil
	})
}

// spillForTest forces a spill of the table's head on the worker (synchronous).
func (s *Store) spillForTest(tableId int) {
	s.q.RunFunc(func() error { return s.spill(tableId) })
}

// dropHeadCloseSegmentsForTest simulates a process crash for the recovery tests (T11/§9): it discards
// the volatile in-memory head (every apply that has NOT yet spilled to a sealed segment is LOST) and
// closes the open segment fds WITHOUT spilling the head, keeping the on-disk files so the next Open
// finds exactly the durable, MANIFEST-named segments. This is deliberately NOT CloseAndWait —
// CloseAndWait spills the head first (a clean close), which is the opposite of a crash. It mirrors
// CloseAndWait's segment teardown (stop the merge loop, publish emptySnapshot so no late reader
// acquires a ref, retireKeepFile each segment) but drops the head map instead of flushing it, leaving
// the store in the design §9 crash-consistency state: sealed segments durable, head volatile/lost.
func (s *Store) dropHeadCloseSegmentsForTest() {
	s.stopMergeLoop() // drain + stop the background merger before any fd is closed
	s.mu.Lock()
	s.head = map[int]*headTable{} // the crash: the volatile head is simply gone
	segs := s.segs
	s.segs = nil
	s.snap.Store(emptySnapshot) // drop the published set first so no late reader acquires a ref
	s.mu.Unlock()
	for _, seg := range segs {
		seg.retireKeepFile() // close the fd, keep the file (still live in the on-disk MANIFEST)
	}
}
