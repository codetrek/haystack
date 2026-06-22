package idtable

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// benchKeys returns n realistic path-like keys.
func benchKeys(n int) [][]byte {
	keys := make([][]byte, n)
	for i := range keys {
		keys[i] = fmt.Appendf(nil, "src/pkg/dir%03d/file-%07d.go", i%256, i)
	}
	return keys
}

// BenchmarkGetId_CachedHit measures the LRU-hit fast path (backend-independent).
func BenchmarkGetId_CachedHit(b *testing.B) {
	a, err := Open(filepath.Join(b.TempDir(), "idtable.db"), Options{CommitInterval: time.Hour})
	if err != nil {
		b.Fatal(err)
	}
	defer a.Close()
	key := []byte("src/pkg/hot/file.go")
	a.GetId(key)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := a.GetId(key); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLookup_CommittedMiss measures the committed-read path (LRU miss →
// backend point read): bbolt B+tree walk vs the legacy pebble LSM Get. A tiny
// LRU forces a miss for every distinct key, isolating the backend read cost.
func BenchmarkLookup_CommittedMiss(b *testing.B) {
	const n = 50_000
	keys := benchKeys(n)
	a, err := Open(filepath.Join(b.TempDir(), "idtable.db"), Options{CommitInterval: time.Hour, LRUCacheSize: 1})
	if err != nil {
		b.Fatal(err)
	}
	defer a.Close()
	for _, k := range keys {
		if _, err := a.GetId(k); err != nil {
			b.Fatal(err)
		}
	}
	if err := a.Commit(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, found, err := a.Lookup(keys[i%n]); err != nil || !found {
			b.Fatalf("found=%v err=%v", found, err)
		}
	}
}
