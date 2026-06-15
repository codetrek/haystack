package vectorindex

import (
	"math/rand"
	"sync"
	"testing"
)

func TestConcurrentInsertAndSearch(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store)

	dim := 32
	var wg sync.WaitGroup

	// 10 goroutines inserting 10 vectors each (100 total).
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(gid)))
			for i := 0; i < 10; i++ {
				vec := make([]float32, dim)
				for d := range vec {
					vec[d] = rng.Float32()*2 - 1
				}
				docId := int64(gid*100 + i)
				if err := idx.Insert(docId, vec); err != nil {
					t.Errorf("insert %d: %v", docId, err)
					return
				}
			}
		}(g)
	}

	// 5 goroutines searching concurrently during inserts.
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(100 + gid)))
			for i := 0; i < 20; i++ {
				query := make([]float32, dim)
				for d := range query {
					query[d] = rng.Float32()*2 - 1
				}
				_, err := idx.Search(query, 5)
				if err != nil {
					t.Errorf("search: %v", err)
					return
				}
			}
		}(g)
	}

	wg.Wait()
}

func TestConcurrentSearchOnly(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store)

	dim := 32
	n := 100
	rng := rand.New(rand.NewSource(42))

	// Insert 100 vectors single-threaded.
	for i := 0; i < n; i++ {
		vec := make([]float32, dim)
		for d := range vec {
			vec[d] = rng.Float32()*2 - 1
		}
		if err := idx.Insert(int64(i), vec); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// 20 goroutines searching concurrently.
	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(gid)))
			for i := 0; i < 20; i++ {
				query := make([]float32, dim)
				for d := range query {
					query[d] = r.Float32()*2 - 1
				}
				results, err := idx.Search(query, 5)
				if err != nil {
					t.Errorf("search: %v", err)
					return
				}
				if len(results) == 0 {
					t.Errorf("expected non-empty results")
					return
				}
			}
		}(g)
	}

	wg.Wait()
}

func TestConcurrentBatchCommitsAndSearch(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store)

	const dim = 24
	var wg sync.WaitGroup

	// 8 goroutines each committing its own batches (no shared batch state).
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(gid)))
			for round := 0; round < 5; round++ {
				b := idx.NewBatch()
				for i := 0; i < 5; i++ {
					vec := make([]float32, dim)
					for d := range vec {
						vec[d] = rng.Float32()
					}
					// Use a unique docId per goroutine+round+item so no key collisions.
					docId := int64(gid*1000 + round*100 + i)
					b.Put(docId, vec)
				}
				if err := b.Commit(); err != nil {
					t.Errorf("commit g=%d round=%d: %v", gid, round, err)
					return
				}
			}
		}(g)
	}

	// 4 concurrent searchers.
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(1000 + gid)))
			for i := 0; i < 30; i++ {
				q := make([]float32, dim)
				for d := range q {
					q[d] = rng.Float32()
				}
				if _, err := idx.Search(q, 5); err != nil {
					t.Errorf("search: %v", err)
					return
				}
			}
		}(g)
	}

	wg.Wait()
}

func TestSearchNeverSeesPartialBatch(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithRand(rand.New(rand.NewSource(3))))

	const dim, batch = 16, 40
	rng := rand.New(rand.NewSource(9))
	q := make([]float32, dim)
	for d := range q {
		q[d] = rng.Float32()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		b := idx.NewBatch()
		for i := 0; i < batch; i++ {
			v := make([]float32, dim)
			for d := range v {
				v[d] = rng.Float32()
			}
			b.Put(int64(i), v)
		}
		_ = b.Commit() // one transaction; either all 40 visible or none
	}()

	// Hammer Search during the commit. Because Commit holds h.mu (write) and
	// Search holds h.mu (read), Search never overlaps a commit: result count is
	// the pre-state (0) or the post-state (batch), never in between.
	for {
		res, err := idx.Search(q, batch)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if n := len(res); n != 0 && n != batch {
			t.Fatalf("partial batch observed: %d results (want 0 or %d)", n, batch)
		}
		select {
		case <-done:
			final, err := idx.Search(q, batch)
			requireNoError(t, err)
			if len(final) != batch {
				t.Fatalf("after commit: %d results, want %d", len(final), batch)
			}
			return
		default:
		}
	}
}

func TestConcurrentInsertDeleteSearch(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store)

	dim := 32

	// Pre-insert 50 vectors to have something to delete and search.
	rng := rand.New(rand.NewSource(99))
	for i := 0; i < 50; i++ {
		vec := make([]float32, dim)
		for d := range vec {
			vec[d] = rng.Float32()*2 - 1
		}
		if err := idx.Insert(int64(i), vec); err != nil {
			t.Fatalf("pre-insert %d: %v", i, err)
		}
	}

	var wg sync.WaitGroup

	// 5 goroutines inserting.
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(200 + gid)))
			for i := 0; i < 10; i++ {
				vec := make([]float32, dim)
				for d := range vec {
					vec[d] = r.Float32()*2 - 1
				}
				docId := int64(1000 + gid*100 + i)
				if err := idx.Insert(docId, vec); err != nil {
					t.Errorf("insert %d: %v", docId, err)
					return
				}
			}
		}(g)
	}

	// 5 goroutines deleting pre-inserted vectors.
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := gid * 10; i < (gid+1)*10; i++ {
				docId := int64(i)
				if err := idx.Delete(docId); err != nil {
					t.Errorf("delete %d: %v", docId, err)
					return
				}
			}
		}(g)
	}

	// 5 goroutines searching.
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(300 + gid)))
			for i := 0; i < 20; i++ {
				query := make([]float32, dim)
				for d := range query {
					query[d] = r.Float32()*2 - 1
				}
				_, err := idx.Search(query, 5)
				if err != nil {
					t.Errorf("search: %v", err)
					return
				}
			}
		}(g)
	}

	wg.Wait()
}
