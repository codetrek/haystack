package vectorstore

import (
	"math/rand"
	"testing"
)

// TestOpen_RejectsMetricMismatch is the red-proof for the unpersisted/unvalidated
// metric. The on-disk vector form is metric-dependent (cosine stores unit+|v|,
// dot/euclid store raw), so reopening sealed segments under a different metric
// silently mis-reads every vector. Options.Metric is operator-settable, so this
// is reachable by a config slip. The manifest must persist the metric and Open
// must reject a mismatch with a clear error rather than silently wrong-restore.
func TestOpen_RejectsMetricMismatch(t *testing.T) {
	kvStore := newTestKV(t)
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(13))
	dim := 8
	b := s.NewBatch()
	for i := 0; i < 20; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		b.Put("d-"+itoa(i), v, nil)
	}
	requireNoError(t, b.Commit())
	// Seal so the metric is persisted in the manifest.
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.Close())

	// Reopen under a DIFFERENT metric: must error, not silently mis-read.
	s2, err := Open(Options{Dir: dir, KV: kvStore, Metric: Euclidean})
	if err == nil {
		_ = s2.Close()
		t.Fatal("Open under Euclidean over a Cosine store should error (metric mismatch), got nil")
	}

	// Reopening under the SAME metric must still succeed.
	s3, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	t.Cleanup(func() { _ = s3.Close() })
	if s3.Metric() != Cosine {
		t.Fatalf("reopened metric = %v, want cosine", s3.Metric())
	}
}
