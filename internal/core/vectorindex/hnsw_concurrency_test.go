package vectorindex

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
)

func TestConcurrentInsertAndSearch(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithCosineDistance())

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
				docId := fmt.Sprintf("doc-%d-%d", gid, i)
				if err := idx.Insert(docId, vec); err != nil {
					t.Errorf("insert %s: %v", docId, err)
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
	idx := NewHNSWIndex(store, WithCosineDistance())

	dim := 32
	n := 100
	rng := rand.New(rand.NewSource(42))

	// Insert 100 vectors single-threaded.
	for i := 0; i < n; i++ {
		vec := make([]float32, dim)
		for d := range vec {
			vec[d] = rng.Float32()*2 - 1
		}
		if err := idx.Insert(fmt.Sprintf("doc-%d", i), vec); err != nil {
			t.Fatalf("insert doc-%d: %v", i, err)
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

func TestConcurrentInsertDeleteSearch(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithCosineDistance())

	dim := 32

	// Pre-insert 50 vectors to have something to delete and search.
	rng := rand.New(rand.NewSource(99))
	for i := 0; i < 50; i++ {
		vec := make([]float32, dim)
		for d := range vec {
			vec[d] = rng.Float32()*2 - 1
		}
		if err := idx.Insert(fmt.Sprintf("pre-%d", i), vec); err != nil {
			t.Fatalf("pre-insert pre-%d: %v", i, err)
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
				docId := fmt.Sprintf("ins-%d-%d", gid, i)
				if err := idx.Insert(docId, vec); err != nil {
					t.Errorf("insert %s: %v", docId, err)
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
				docId := fmt.Sprintf("pre-%d", i)
				if err := idx.Delete(docId); err != nil {
					t.Errorf("delete %s: %v", docId, err)
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
