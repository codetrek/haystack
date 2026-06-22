package invertedindex

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/core/kv/pebblekv"
	"github.com/codetrek/haystack/core/queue"
)

// benchSearchEnv builds an index holding a single keyword "kw" with nDocs
// postings, flushed to one row. It exercises the Search decode-into-set hot
// path (the per-docid allocation that the int64 representation removes).
func benchSearchEnv(b *testing.B, nDocs int) (*Index, func()) {
	b.Helper()
	tempDir, err := os.MkdirTemp("", "haystack-inv-bench-*")
	if err != nil {
		b.Fatal(err)
	}
	db, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		b.Fatal(err)
	}
	q := queue.NewMpsc("BenchInvQueue")
	q.Start()
	idx, err := New(db, q, Options{})
	if err != nil {
		b.Fatal(err)
	}
	for i := range nDocs {
		idx.Update(1, makeDocID(fmt.Sprintf("d%07d", i)), []string{"kw"}, nil)
	}
	forceFlush(idx)
	cleanup := func() {
		idx.CloseAndWait()
		q.Stop()
		db.Close()
		os.RemoveAll(tempDir)
	}
	return idx, cleanup
}

// BenchmarkSearchCollect measures Search building the result set over a posting
// list. allocs/op is the deterministic signal for the docid-as-int64 change.
func BenchmarkSearchCollect(b *testing.B) {
	log.SetOutput(io.Discard) // silence the index's flush/merge logs so they don't corrupt benchmark output
	for _, n := range []int{50, 1000} {
		b.Run(fmt.Sprintf("docs=%d", n), func(b *testing.B) {
			idx, cleanup := benchSearchEnv(b, n)
			defer cleanup()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				res := idx.Search(1, "kw", -1, nil)
				if len(res.DocIds) != n {
					b.Fatalf("got %d docids, want %d", len(res.DocIds), n)
				}
			}
		})
	}
}
