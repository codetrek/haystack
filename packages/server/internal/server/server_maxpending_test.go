package server

import (
	"testing"

	"github.com/codetrek/haystack/packages/core/invertedindex"
	"github.com/codetrek/haystack/packages/core/kv"
	"github.com/codetrek/haystack/packages/core/queue"
)

// TestInvertedIndexInit_PassesRecommendedBound guards the PRODUCTION wiring: the
// server's invertedindexInit closure must construct the inverted index with the
// measured-good MaxPendingPostings bound, not the unbounded zero value. It
// overrides the newInvertedIndex seam to capture the Options the real closure
// passes, catching both revert vectors (dropping the bound, or editing the
// closure body back to Options{}).
func TestInvertedIndexInit_PassesRecommendedBound(t *testing.T) {
	var captured invertedindex.Options
	orig := newInvertedIndex
	// NOTE: the override's 2nd param is queue.Queue (NOT *queue.Mpsc).
	// newInvertedIndex is `var newInvertedIndex = invertedindex.New`, so it
	// inherits New's signature func(kv.Store, queue.Queue, Options) — a
	// *queue.Mpsc-typed func literal is NOT assignable to it (Go func types are
	// parameter-invariant). The production closure passing a concrete *queue.Mpsc
	// at the CALL site is fine (it satisfies the queue.Queue interface param).
	newInvertedIndex = func(_ kv.Store, _ queue.Queue, opts invertedindex.Options) (*invertedindex.Index, error) {
		captured = opts
		return nil, nil
	}
	defer func() { newInvertedIndex = orig }()

	if _, err := invertedindexInit(nil, nil); err != nil {
		t.Fatalf("invertedindexInit: %v", err)
	}
	if captured.MaxPendingPostings != invertedindex.RecommendedMaxPendingPostings {
		t.Fatalf("MaxPendingPostings = %d, want RecommendedMaxPendingPostings (%d)",
			captured.MaxPendingPostings, invertedindex.RecommendedMaxPendingPostings)
	}
}
