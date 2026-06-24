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
